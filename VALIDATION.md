# MVP verification — 8 September 2026

Tested on the local Apple Silicon Docker Desktop runtime with two Envoy 1.33.2 processes, HAProxy 3.0, the Go control plane and actual Docker-managed demo replicas.

## Automated checks

- `go test -race ./...`: passed. Covers hostname ownership, dedicated-cell restrictions, immutable operation state, release approval, idempotency conflicts, cross-cell release rejection, snapshot routing, retirement guards, persistence, authentication and cross-origin rejection.
- `node --check web/app.js`: passed.
- `python3 tests/integration.py`: passed all scenarios. The final run made **170 successful requests spanning r41 and r42 during release**, with no request errors.
- Custom domains passed through the edge and each gateway directly. Backend identity matched the resolved tenant despite forged request headers.
- BetaCo changed revision independently of SmallCo.
- Acme remained on its old revision until approval.
- The deliberately failing revision triggered automatic restoration of the previous route.
- Traffic continued with the control plane stopped.
- With the control plane still stopped, stopping gateway A left gateway B serving requests after the health-detection interval.
- Persistent assignments survived restart; gateways resubscribed successfully.
- A rollout interrupted during observation resumed and completed after the controller restarted.
- `harborctl release --wait` progressed through preparation, activation and observation, then exited successfully. A subsequent CLI release restored the tested assignment.

## Browser checks

- Inspected the dashboard and deployment/tenant management screens in the actual browser.
- Registered r43 using the web form.
- Created `shared-r43` using the web form and observed its containers becoming ready.
- Created the Sandbox tenant with `sandbox.localhost` and `sandbox.example.test`.
- Opened `sandbox.localhost:8080` and verified the rendered page showed revision r43 and the correct tenant/deployment.
- Verified its custom domain using `curl --resolve`.

## Findings fixed during verification

1. HAProxy's initial HTTP/1.0 health probe received 426 from Envoy. The final configuration sends HTTP/1.1 with a Host header.
2. Route-configuration header removal ran after virtual-host header insertion. Final virtual-host configuration overwrites untrusted tenant/deployment headers directly.
3. Gateway failure detection and connection retry were too slow for the initial acceptance threshold. Bounded checks and connection redispatch now exercise failover successfully.
4. Reconnecting Envoys with a matching cached configuration could omit fresh ACKs, blocking an in-progress release. A per-process xDS version epoch forces a fresh acknowledged snapshot.

These checks demonstrate local process-level behaviour, not host-level availability or a guarantee that every in-flight request survives a crash. See README for the MVP boundaries.
