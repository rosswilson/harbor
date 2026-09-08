#!/usr/bin/env python3
"""Real Docker/Envoy acceptance checks. Run against the local lab only.

Leaves example deployments and audit history available in the UI. Restores
gateway/control-plane processes even when an assertion fails.
"""

import json
import os
import subprocess
import threading
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:9090/api/"
TOKEN = os.getenv("HARBOR_TOKEN", "harbor-local-dev")


def api(path, payload=None, key=None):
    headers = {"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(
        BASE + path,
        headers=headers,
        data=None if payload is None else json.dumps(payload).encode(),
    )
    with urllib.request.urlopen(req, timeout=10) as response:
        return json.load(response)


def eventually(fn, timeout=90):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (urllib.error.URLError, ConnectionError) as error:
            last = error
        time.sleep(1)
    raise AssertionError("Timed out: " + str(last))


def route(host, port=8080):
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/api/info",
        headers={
            "Host": host,
            "X-Harbor-Tenant": "forged",
            "X-Harbor-Deployment": "forged",
        },
    )
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.load(response), dict(response.headers)


def deployment(id, cell, revision):
    if not any(d["id"] == id for d in api("state")["state"]["deployments"]):
        api(
            "deployments", {"id": id, "cell": cell, "revision": revision, "replicas": 2}
        )
    eventually(
        lambda: any(
            d["id"] == id and d["status"] == "ready"
            for d in api("state")["state"]["deployments"]
        )
    )


def release(tenant, target, expected="succeeded", approve=False):
    r = api("rollouts", {"tenant": tenant, "deployment": target, "observeSeconds": 6})
    if approve:
        assert r["status"] == "awaiting_approval"
        api("rollouts/" + r["id"] + "/approve", {})
    result = eventually(
        lambda: (
            (v if v["status"] in ("succeeded", "rolled_back", "failed") else None)
            if (v := api("rollouts/" + r["id"]))
            else None
        )
    )
    assert result["status"] == expected, result
    return result


def docker(*args):
    subprocess.run(["docker", *args], check=True, stdout=subprocess.DEVNULL)


def main():
    eventually(
        lambda: all(g["ready"] and g["connected"] for g in api("state")["gateways"])
    )
    smallco_revision = eventually(lambda: route("smallco.localhost")[0]["Revision"])
    print("PASS initial routing", flush=True)
    for port in (8080, 18081, 18082):
        assert route("portal.betaco.test", port)[0]["Tenant"] == "betaco"
        assert route("app.acme.test", port)[0]["Tenant"] == "acme"
    print("PASS custom domains through both gateways and edge", flush=True)
    try:
        route("unregistered.test")
        raise AssertionError("Unknown host should be rejected")
    except urllib.error.HTTPError as error:
        assert error.code == 404
    current = route("betaco.localhost")[0]["Revision"]
    if current != "r41":
        release("betaco", "shared-r41")
    stop = threading.Event()
    samples, errors = [], []

    def traffic():
        while not stop.is_set():
            try:
                samples.append(route("betaco.localhost")[0]["Revision"])
            except Exception as error:
                errors.append(str(error))
            stop.wait(0.05)

    load = threading.Thread(target=traffic)
    load.start()
    try:
        release("betaco", "shared-r42")
    finally:
        stop.set()
        load.join()
    assert not errors and len(samples) > 20 and set(samples) == {"r41", "r42"}, (
        samples,
        errors,
    )
    print(
        f"PASS {len(samples)} uninterrupted requests across tenant release", flush=True
    )
    assert route("betaco.localhost")[0]["Revision"] == "r42"
    assert route("smallco.localhost")[0]["Revision"] == smallco_revision
    print("PASS tenant release isolation", flush=True)
    deployment("acme-r42", "acme-eu", "r42")
    if route("acme.localhost")[0]["Revision"] != "r41":
        release("acme", "acme-r41", approve=True)
    r = api(
        "rollouts",
        {"tenant": "acme", "deployment": "acme-r42", "observeSeconds": 6},
        "acceptance-" + str(time.time()),
    )
    assert r["status"] == "awaiting_approval"
    time.sleep(3)
    assert route("acme.localhost")[0]["Revision"] == "r41"
    api("rollouts/" + r["id"] + "/approve", {})
    eventually(lambda: api("rollouts/" + r["id"])["status"] == "succeeded")
    print("PASS enterprise approval gate", flush=True)
    deployment("shared-broken", "shared-eu", "broken")
    release("betaco", "shared-broken", expected="rolled_back")
    assert route("betaco.localhost")[0]["Revision"] == "r42"
    print("PASS automatic rollback on application failure", flush=True)
    before = api("state")["state"]
    try:
        docker("stop", "harbor-controlplane")
        for _ in range(10):
            assert route("betaco.localhost")[0]["Revision"] == "r42"
        print("PASS existing traffic survives control-plane outage", flush=True)
        docker("stop", "harbor-gateway-a")
        time.sleep(3)
        for _ in range(20):
            _, headers = route("betaco.localhost")
            assert headers.get("x-harbor-gateway") == "gateway-b", headers
        print("PASS gateway failover with control plane offline", flush=True)
    finally:
        docker("start", "harbor-controlplane", "harbor-gateway-a")
    eventually(
        lambda: all(g["ready"] and g["connected"] for g in api("state")["gateways"])
    )
    assert api("state")["state"]["tenants"] == before["tenants"]
    assert route("betaco.localhost", 18081)[0]["Revision"] == "r42"
    print("PASS durable state and gateway resubscription", flush=True)
    release("betaco", "shared-r41")
    release("acme", "acme-r41", approve=True)
    pending = api(
        "rollouts",
        {"tenant": "betaco", "deployment": "shared-r42", "observeSeconds": 12},
    )
    eventually(lambda: api("rollouts/" + pending["id"])["status"] == "observing")
    try:
        docker("stop", "harbor-controlplane")
        assert route("betaco.localhost")[0]["Revision"] == "r42"
    finally:
        docker("start", "harbor-controlplane")
    recovered = eventually(
        lambda: (
            (v if v["status"] == "succeeded" else None)
            if (v := api("rollouts/" + pending["id"]))
            else None
        )
    )
    assert recovered["observedSeconds"] >= 12
    print("PASS in-progress rollout resumes after controller restart", flush=True)
    release("betaco", "shared-r41")
    print("ALL ACCEPTANCE CHECKS PASSED; BetaCo and Acme restored to r41", flush=True)


if __name__ == "__main__":
    main()
