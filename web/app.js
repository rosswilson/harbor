const $ = (s) => document.querySelector(s);
const esc = (v) =>
  String(v ?? "").replace(
    /[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c],
  );
let data,
  view = location.hash.slice(1) || "overview",
  token = sessionStorage.getItem("harbor-token") || "harbor-local-dev",
  submitAction;
const titles = {
  overview: "Overview",
  tenants: "Tenants & domains",
  deployments: "Deployments",
  releases: "Releases",
  activity: "Activity",
  integrations: "API & CLI",
};
const active = (r) =>
  ["awaiting_approval", "preparing", "activating", "observing", "reverting"].includes(r.status);
const badge = (s) =>
  `<span class="tag ${esc(s.replaceAll("_", "-"))}">${esc(s.replaceAll("_", " "))}</span>`;
const btn = (label, action, id = "", kind = "secondary small") =>
  `<button class="button ${kind}" data-action="${action}" data-id="${esc(id)}">${label}</button>`;
const time = (t) =>
  new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const options = (items, selected) =>
  items
    .map(
      ([value, label]) =>
        `<option value="${esc(value)}" ${value === selected ? "selected" : ""}>${esc(label)}</option>`,
    )
    .join("");
function toast(message) {
  $("#toast").textContent = message;
  $("#toast").classList.add("visible");
  setTimeout(() => $("#toast").classList.remove("visible"), 5000);
}
async function api(path = "", method = "GET", body) {
  const r = await fetch("/api/" + path, {
    method,
    headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const result = await r.json();
  if (!r.ok) {
    const e = new Error(result.error || "Request failed");
    e.status = r.status;
    throw e;
  }
  return result;
}
function auth() {
  $("#main").innerHTML =
    `<section class="panel auth"><div class="panel-body"><div class="eyebrow">LOCAL WORKSPACE</div><h1>Connect to Harbor</h1><p class="subtext">Enter the API token configured for this control plane.</p><form id="login"><label class="field">API token<input name="token" type="password" required autocomplete="current-password"></label><button class="button">Connect</button></form><p class="subtext">Default local token: <code>harbor-local-dev</code></p></div></section>`;
  $("#login").onsubmit = async (e) => {
    e.preventDefault();
    token = new FormData(e.target).get("token");
    sessionStorage.setItem("harbor-token", token);
    await refresh();
  };
}
async function refresh() {
  try {
    data = await api("state");
    $("#connection").textContent = "● Connected";
    if (!$("#modal").open) render();
  } catch (e) {
    $("#connection").textContent = "Disconnected";
    if (e.status === 401) auth();
    else if (!data) $("#main").innerHTML = `<div class="empty">${esc(e.message)}. Retrying…</div>`;
  }
}
function heading(title, subtitle, actions = "") {
  return `<div class="page-title"><div><div class="eyebrow">${view === "overview" ? "ENVIRONMENT AT A GLANCE" : "WORKSPACE / " + esc(view.toUpperCase())}</div><h1>${title}</h1><p class="subtext">${subtitle}</p></div><div class="actions">${actions}</div></div>`;
}
function stat(label, value, note, good = false) {
  return `<div class="stat"><div class="stat-label">${label}</div><div class="stat-value">${value}</div><div class="stat-note ${good ? "good" : ""}">${note}</div></div>`;
}
function events(limit = 100) {
  return (
    data.state.events
      .slice(0, limit)
      .map(
        (e) =>
          `<div class="event"><span class="event-dot"></span><div><div class="event-title">${esc(e.subject)} <span class="tag">${esc(e.kind)}</span></div><div class="event-message">${esc(e.message)}</div></div><time class="time">${time(e.time)}</time></div>`,
      )
      .join("") || '<div class="empty">No events yet.</div>'
  );
}
function tenantTable(compact = false) {
  return `<div class="table-scroll"><table><thead><tr><th>TENANT</th><th>DOMAINS</th><th>ASSIGNMENT</th>${compact ? "" : "<th>RELEASE POLICY</th>"}<th></th></tr></thead><tbody>${data.state.tenants
    .map((t) => {
      const ro = data.state.rollouts.find((r) => r.tenant === t.id && active(r));
      return `<tr><td><div class="tenant-name"><span class="avatar">${esc(t.name.slice(0, 2).toUpperCase())}</span><div>${esc(t.name)}<div class="tenant-detail">${esc(t.cell)}</div></div></div></td><td>${t.domains.map((d) => (d.endsWith(".localhost") ? `<a class="domain" href="http://${esc(d)}:8080" target="_blank" rel="noopener">${esc(d)} ↗</a>` : `<span class="domain custom" title="Configure local DNS or use curl --resolve">${esc(d)} <span class="tag">custom</span></span>`)).join("")}</td><td><span class="mono">${esc(t.deployment)}</span>${ro ? `<div class="tenant-detail">${badge(ro.status)}</div>` : ""}</td>${
        compact
          ? ""
          : `<td>${badge(t.approval ? "awaiting_approval" : "good")
              .replace("awaiting approval", "Approval required")
              .replace(">good<", ">Automatic<")}</td>`
      }<td><div class="actions">${btn("Release ↗", "release", t.id)}${compact ? "" : btn("Edit", "edit-tenant", t.id)}${!compact && t.previous ? btn("Rollback", "rollback", t.id) : ""}</div></td></tr>`;
    })
    .join("")}</tbody></table></div>`;
}
function gatewayNode(g) {
  const ack = Object.values(g.ack || {}).includes(data.xdsVersion);
  return `<div class="node"><div class="node-top"><h3>${esc(g.id)}</h3>${badge(g.ready ? "ready" : "offline")}</div><p>${g.connected ? "ADS stream connected" : "ADS stream disconnected"} · ${ack ? "config accepted" : "awaiting current config"}</p><div class="gateway-meta">${btn(g.ready ? "Stop gateway" : "Start gateway", g.ready ? "stop-gateway" : "start-gateway", g.id)}<span class="mono">:${g.id === "gateway-a" ? "18081" : "18082"}</span></div>${g.nack ? `<p class="error-note">${esc(g.nack)}</p>` : ""}</div>`;
}
function overview() {
  const s = data.state;
  const ready = data.gateways.filter((g) => g.ready).length;
  return (
    heading(
      "Your delivery control room",
      "Deploy capacity. Release to tenants. Watch the route change.",
      btn("+ New deployment", "new-deployment", "", ""),
    ) +
    `<div class="stats">${stat("Tenant assignments", s.tenants.length, `${s.tenants.reduce((n, t) => n + t.domains.length, 0)} registered hostnames`)}${stat("Ready deployments", s.deployments.filter((d) => d.status === "ready").length, `${s.deployments.reduce((n, d) => n + (d.instances || []).filter((i) => i.ready).length, 0)} healthy container replicas`)}${stat("Gateway availability", `${ready}<span class="subtext"> / 2</span>`, ready === 2 ? "Both gateways serving" : ready === 1 ? "One gateway serving" : "Waiting for gateways", ready === 2)}${stat("Active releases", s.rollouts.filter(active).length, `${s.rollouts.filter((r) => r.status === "awaiting_approval").length} awaiting approval`)}</div><section class="panel"><div class="panel-heading"><div><h2>Live traffic path</h2><p class="subtext">The control plane publishes configuration; application traffic stays in the data plane.</p></div><span class="tag mono">xDS ${esc(data.xdsVersion || "pending")}</span></div><div class="topology"><div class="topology-group"><div class="node control"><div class="node-top"><h3>Local edge</h3><span class="tag ready">HTTP</span></div><p>HAProxy · localhost:8080</p><p>Round robin + gateway health checks</p></div></div><div class="topology-group">${data.gateways.map(gatewayNode).join("")}</div><div class="topology-group">${s.cells.map((c) => `<div class="node"><div class="node-top"><h3>${esc(c.id)}</h3><span class="tag">${c.dedicated ? "Dedicated" : "Shared"}</span></div><p>${s.tenants.filter((t) => t.cell === c.id).length} tenants · ${s.deployments.filter((d) => d.cell === c.id && !d.retired).length} deployments</p><p class="mono">${esc(c.region)}</p></div>`).join("")}</div></div></section><div class="columns"><section class="panel"><div class="panel-heading"><h2>Tenant assignments <span class="count">${s.tenants.length}</span></h2><a href="#tenants" class="subtext">Manage →</a></div>${tenantTable(true)}</section><section class="panel"><div class="panel-heading"><h2>Recent activity</h2><a href="#activity" class="subtext">View all →</a></div><div class="timeline">${events(5)}</div></section></div>`
  );
}
function tenants() {
  return (
    heading(
      "Tenants & domains",
      "Each hostname follows its tenant’s release assignment.",
      btn("+ Add tenant", "new-tenant", "", ""),
    ) +
    `<section class="panel">${tenantTable()}</section><div class="hint"><strong>Local subdomains</strong> such as <code>betaco.localhost</code> work in modern browsers. Custom domains are exact routes too: test <code>portal.betaco.test</code> with <code>curl --resolve portal.betaco.test:8080:127.0.0.1 http://portal.betaco.test:8080</code>, or add a local hosts entry. This MVP serves HTTP; it does not provision DNS or certificates.</div>`
  );
}
function deployments() {
  const s = data.state;
  return (
    heading(
      "Deployments",
      "Run an immutable revision in a cell before assigning tenant traffic.",
      btn("Register revision", "new-revision") + btn("+ Deploy revision", "new-deployment", "", ""),
    ) +
    `<div class="deployment-grid">${s.deployments
      .filter((d) => !d.retired || d.status !== "retired")
      .map(
        (d) =>
          `<article class="deployment-card"><div class="node-top rollout-top"><h3>${esc(d.id)}</h3>${badge(d.status)}</div><p class="subtext">${esc(d.cell)}</p><dl><dt>Revision</dt><dd class="mono">${esc(d.revision)}</dd><dt>Replicas</dt><dd>${(d.instances || []).filter((i) => i.ready).length} / ${d.replicas}<div class="replicas">${Array.from({ length: d.replicas }, (_, i) => `<span class="replica ${d.instances?.[i]?.ready ? "up" : ""}"></span>`).join("")}</div></dd><dt>Serving</dt><dd>${
            s.tenants
              .filter((t) => t.deployment === d.id)
              .map((t) => esc(t.name))
              .join(", ") || "No tenant traffic"
          }</dd></dl><div class="instance-list">${(d.instances || []).map((i) => `${i.ready ? "●" : "○"} ${esc(i.name)} · ${esc(i.ip || "starting")}`).join("<br>")}</div>${d.error ? `<p class="error-note">${esc(d.error)}</p>` : ""}<div class="actions"><a class="button secondary small" href="http://${esc(d.id)}.preview.localhost:8080" target="_blank" rel="noopener">Preview ↗</a>${btn("Retire", "retire", d.id)}</div></article>`,
      )
      .join(
        "",
      )}</div><section class="panel"><div class="panel-heading"><h2>Revision registry</h2>${btn("+ Register", "new-revision")}</div><div class="table-scroll"><table><thead><tr><th>REVISION</th><th>IMAGE</th><th>PORT / READINESS</th><th>ENVIRONMENT</th></tr></thead><tbody>${s.revisions
      .map(
        (r) =>
          `<tr><td class="mono">${esc(r.id)}</td><td class="mono">${esc(r.image)}</td><td class="mono">${r.port} ${esc(r.healthPath)}</td><td class="mono">${esc(
            Object.entries(r.env || {})
              .map(([k, v]) => k + "=" + v)
              .join(" · ") || "—",
          )}</td></tr>`,
      )
      .join(
        "",
      )}</tbody></table></div></section><section class="panel"><div class="panel-heading"><h2>Compute cells</h2>${btn("+ Add cell", "new-cell")}</div><div class="table-scroll"><table><thead><tr><th>CELL</th><th>REGION</th><th>PLACEMENT POLICY</th></tr></thead><tbody>${s.cells.map((c) => `<tr><td class="mono">${esc(c.id)}</td><td>${esc(c.region)}</td><td>${c.dedicated ? "One tenant · dedicated containers" : "Multiple tenants · shared containers"}</td></tr>`).join("")}</tbody></table></div></section>`
  );
}
function releases() {
  return (
    heading(
      "Releases",
      "Prepare, activate, verify. The previous deployment remains available.",
      btn("+ New release", "release", "", ""),
    ) +
    `<section class="panel">${
      [...data.state.rollouts]
        .reverse()
        .map((r) => {
          const idx = ["preparing", "activating", "observing", "succeeded"].indexOf(r.status);
          return `<article class="rollout"><div class="rollout-top"><div class="rollout-title">${esc(r.tenant)} <span class="arrow">/</span><span class="mono">${esc(r.previous)} → ${esc(r.target)}</span></div>${badge(r.status)}</div><div class="steps">${[0, 1, 2, 3].map((i) => `<div class="step ${i <= idx ? "done" : ""} ${r.status === "rolled_back" ? "bad" : ""}"></div>`).join("")}</div><p>${esc(r.message)}</p><div class="rollout-meta"><span class="mono">${esc(r.id)} · ${r.samples} probes · ${r.failures} failed rounds · ${r.observeSeconds}s window</span><div class="actions">${r.status === "awaiting_approval" ? btn("Approve release", "approve", r.id, "small") : ""}${active(r) && r.status !== "reverting" ? btn("Cancel / revert", "cancel", r.id) : ""}</div></div></article>`;
        })
        .join("") ||
      `<div class="empty">No releases yet. Deploy r42, then assign it to a tenant.</div>`
    }</section><div class="hint">Release gates require both gateways to accept the current configuration. Three consecutive failed probe rounds trigger rollback. These are synthetic HTTP checks, not statistical canary analysis. Each release switches the whole tenant; percentage cohorts are a future extension.</div>`
  );
}
function integrations() {
  return (
    heading("API & CLI", "Use the same operations from your pipeline and your browser.") +
    `<div class="columns"><section class="panel"><div class="panel-heading"><h2>A pipeline-friendly release</h2></div><div class="panel-body"><p class="subtext">The CLI registers images, requests deployments and waits for a verified release. It exits non-zero on failure or rollback.</p><pre class="code">export HARBOR_URL=http://localhost:9090
export HARBOR_TOKEN=harbor-local-dev

./scripts/harborctl revision r43 \\
  --image ghcr.io/your-org/app@sha256:… \\
  --port 8080 --health-path /health

./scripts/harborctl deploy shared-r43 \\
  --revision r43 --cell shared-eu --replicas 2

./scripts/harborctl release betaco shared-r43 \\
  --key build-123-betaco --wait</pre><p class="subtext">Use an image digest in CI. Local image tags are resolved to an image ID at deployment creation. Enterprise releases wait for an operator’s approval.</p></div></section><section class="panel"><div class="panel-heading"><h2>HTTP API</h2></div><div class="panel-body"><pre class="code">POST /api/revisions
POST /api/deployments
POST /api/tenants
PATCH /api/tenants/{id}
POST /api/rollouts
GET  /api/rollouts/{id}
POST /api/rollouts/{id}/approve
POST /api/rollouts/{id}/cancel
POST /api/tenants/{id}/rollback
GET  /api/state</pre><p class="subtext">Bearer token authentication. Send <code>Idempotency-Key</code> when creating a rollout to safely retry the same pipeline request.</p><pre class="code">curl http://localhost:9090/api/rollouts \\
  -H "Authorization: Bearer $HARBOR_TOKEN" \\
  -H 'Content-Type: application/json' \\
  -H 'Idempotency-Key: demo-release-42' \\
  -d '{"tenant":"betaco",
       "deployment":"shared-r42",
       "observeSeconds":20}'</pre></div></section></div>`
  );
}
function render() {
  if (!data) return;
  if (!titles[view]) view = "overview";
  $("#breadcrumb").textContent = titles[view];
  document
    .querySelectorAll("nav a")
    .forEach((a) => a.classList.toggle("active", a.dataset.view === view));
  const renders = {
    overview,
    tenants,
    deployments,
    releases,
    integrations,
    activity: () =>
      heading("Activity", "A durable timeline of platform and operator decisions.") +
      `<section class="panel"><div class="timeline">${events()}</div></section>`,
  };
  $("#main").innerHTML = renders[view]();
}
function field(name, label, value = "", type = "text", hint = "") {
  const max = name === "port" ? 65535 : name === "replicas" ? 5 : 600;
  return `<label class="field">${label}<input name="${name}" type="${type}" value="${esc(value)}" ${type === "number" ? `min="1" max="${max}"` : ""} required>${hint ? `<small>${hint}</small>` : ""}</label>`;
}
function select(name, label, items, selected) {
  return `<label class="field">${label}<select name="${name}" required>${options(items, selected)}</select></label>`;
}
function openModal(title, fields, action, label = "Save") {
  submitAction = action;
  $("#modal-title").textContent = title;
  $("#fields").innerHTML = fields;
  $("#form-error").textContent = "";
  $("#submit").textContent = label;
  $("#modal").showModal();
}
$("#form").onsubmit = async (e) => {
  e.preventDefault();
  $("#submit").disabled = true;
  $("#form-error").textContent = "";
  try {
    await submitAction(Object.fromEntries(new FormData(e.target)));
    $("#modal").close();
    toast("Saved. The control plane is reconciling your change.");
    await refresh();
  } catch (err) {
    $("#form-error").textContent = err.message;
  } finally {
    $("#submit").disabled = false;
  }
};
document.addEventListener("click", async (e) => {
  const el = e.target.closest("[data-action]");
  if (!el) return;
  const action = el.dataset.action,
    id = el.dataset.id;
  const s = data?.state;
  try {
    if (action === "close") {
      $("#modal").close();
      return;
    }
    if (action === "logout") {
      token = "";
      sessionStorage.removeItem("harbor-token");
      auth();
      return;
    }
    if (action === "new-cell") {
      openModal(
        "Add compute cell",
        field("id", "Cell ID", "team-us") +
          field("region", "Region label", "us-local") +
          '<label class="field"><input type="checkbox" name="dedicated"> Dedicated: allow one tenant only</label>',
        (f) => api("cells", "POST", { ...f, dedicated: !!f.dedicated }),
      );
      return;
    }
    if (action === "new-revision") {
      let next = 43;
      while (s.revisions.some((r) => r.id === "r" + next)) next++;
      openModal(
        "Register immutable revision",
        field("id", "Revision ID", "r" + next) +
          field("image", "Docker image", "harbor-demo:local") +
          field("port", "Container port", "8080", "number") +
          field("healthPath", "Readiness path", "/health") +
          '<label class="field">Environment variables<textarea name="env" placeholder="REVISION=r43"></textarea><small>One KEY=value per line. Do not put secrets in this demo registry.</small></label>',
        (f) => {
          const env = {};
          for (const line of f.env.split("\n").filter(Boolean)) {
            const index = line.indexOf("=");
            if (index < 1) throw new Error("Environment entries must be KEY=value");
            env[line.slice(0, index)] = line.slice(index + 1);
          }
          return api("revisions", "POST", { ...f, port: Number(f.port), env });
        },
      );
      return;
    }
    if (action === "new-deployment") {
      const revision = [...s.revisions].reverse().find((r) => r.id !== "broken")?.id || "r41";
      let name = "shared-" + revision;
      let suffix = 2;
      while (s.deployments.some((d) => d.id === name)) name = "shared-" + revision + "-" + suffix++;
      openModal(
        "Deploy revision",
        field("id", "Deployment ID", name) +
          select(
            "cell",
            "Compute cell",
            s.cells.map((c) => [c.id, c.id]),
            "shared-eu",
          ) +
          select(
            "revision",
            "Revision",
            s.revisions.map((r) => [r.id, r.id + " · " + r.image]),
            revision,
          ) +
          field("replicas", "Replicas (1–5)", "2", "number"),
        (f) => api("deployments", "POST", { ...f, replicas: Number(f.replicas) }),
        "Deploy",
      );
      return;
    }
    if (action === "new-tenant" || action === "edit-tenant") {
      const t = s.tenants.find((t) => t.id === id);
      openModal(
        t ? "Edit tenant" : "Add tenant",
        (t ? "" : field("id", "Tenant ID", "newco")) +
          field("name", "Name", t?.name || "NewCo") +
          (t
            ? ""
            : select(
                "cell",
                "Cell",
                s.cells.map((c) => [c.id, c.id]),
                "shared-eu",
              ) +
              select(
                "deployment",
                "Initial deployment",
                s.deployments.filter((d) => !d.retired).map((d) => [d.id, d.id]),
                "shared-r41",
              )) +
          `<label class="field">Hostnames<textarea name="domains" required>${esc(t?.domains.join("\n") || "newco.localhost")}</textarea><small>One lowercase hostname per line. Domains must be unique across tenants.</small></label><label class="field"><input name="approval" type="checkbox" ${t?.approval ? "checked" : ""}> Require approval before releases</label>`,
        (f) =>
          api(t ? "tenants/" + t.id : "tenants", t ? "PATCH" : "POST", {
            ...f,
            domains: f.domains
              .split(/[\n,]/)
              .map((x) => x.trim())
              .filter(Boolean),
            approval: !!f.approval,
          }),
      );
      return;
    }
    if (action === "release") {
      const selected = s.tenants.find((t) => t.id === id) || s.tenants[0];
      openModal(
        "Release to tenant",
        select(
          "tenant",
          "Tenant",
          s.tenants.map((t) => [t.id, t.name]),
          selected?.id,
        ) +
          select(
            "deployment",
            "Target deployment",
            s.deployments
              .filter(
                (d) => !d.retired && d.cell === selected?.cell && d.id !== selected?.deployment,
              )
              .map((d) => [d.id, d.id]),
          ) +
          field("observeSeconds", "Observation window (seconds)", "20", "number") +
          '<div class="hint">A release switches this tenant’s complete route. Existing application versions remain running for other tenants and rollback.</div>',
        (f) => api("rollouts", "POST", { ...f, observeSeconds: Number(f.observeSeconds) }),
        "Request release",
      );
      $("#fields select[name=tenant]").onchange = (e) => {
        const t = s.tenants.find((t) => t.id === e.target.value);
        $("#fields select[name=deployment]").innerHTML = options(
          s.deployments
            .filter((d) => !d.retired && d.cell === t.cell && d.id !== t.deployment)
            .map((d) => [d.id, d.id]),
        );
      };
      return;
    }
    el.disabled = true;
    if (action === "approve" || action === "cancel")
      await api(`rollouts/${id}/${action}`, "POST", {});
    if (action === "rollback") await api(`tenants/${id}/rollback`, "POST", {});
    if (action === "retire") await api(`deployments/${id}`, "DELETE");
    if (action === "stop-gateway" || action === "start-gateway")
      await api(`gateways/${id}/${action === "stop-gateway" ? "stop" : "start"}`, "POST", {});
    toast("Action accepted");
    await refresh();
  } catch (err) {
    toast(err.message);
  } finally {
    el.disabled = false;
  }
});
window.addEventListener("hashchange", () => {
  view = location.hash.slice(1) || "overview";
  render();
});
refresh();
setInterval(refresh, 3000);
