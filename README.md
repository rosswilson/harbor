# Harbor

A local orchestration and tenant-release laboratory. Real Docker containers, two real Envoy gateways, streaming xDS v3, a web control panel, and a CI-friendly API/CLI.

## Start

Requirements: Docker Desktop (or a local Linux Docker daemon), Docker Compose v2. Python 3 is needed only for the CLI and acceptance tests. Go 1.25+ is needed only for host-side development tests.

```sh
./scripts/up.sh
```

Open **http://localhost:9090**. The UI connects automatically using the local demo token `harbor-local-dev`. To use a different token, put `HARBOR_TOKEN=your-token` in `.env` before starting, then use **Change API token** in the UI. Export that same token when using the CLI.

First startup builds the control plane and demo image, pulls Envoy and HAProxy, and creates six backend replicas. Wait for all three initial deployments and both gateways to become ready.

| Address                                  | Purpose                                             |
| ---------------------------------------- | --------------------------------------------------- |
| http://localhost:9090                    | Management UI and API                               |
| http://smallco.localhost:8080            | SmallCo, initially shared revision 41               |
| http://betaco.localhost:8080             | BetaCo, initially shared revision 41                |
| http://acme.localhost:8080               | Acme, dedicated deployment, approval required       |
| http://shared-r42.preview.localhost:8080 | Revision 42 preview, no tenant assignment needed    |
| http://localhost:18081                   | Gateway A directly; supply a registered Host header |
| http://localhost:18082                   | Gateway B directly; supply a registered Host header |

`.localhost` names resolve to loopback in modern browsers. For command-line clients without that behaviour, use `curl --resolve`.

### Custom domains

The seed tenants also own `portal.betaco.test` and `app.acme.test`. These are real exact-hostname routes. DNS is deliberately separate:

```sh
curl --resolve portal.betaco.test:8080:127.0.0.1 \
  http://portal.betaco.test:8080/api/info

curl --resolve app.acme.test:8080:127.0.0.1 \
  http://app.acme.test:8080/api/info
```

For browser use, you may add these entries to your machine's hosts file:

```text
127.0.0.1 portal.betaco.test app.acme.test
```

Harbor does not modify your hosts file, provision public DNS, verify domain ownership, or issue certificates. Hostnames with ports work because Envoy ignores the port while matching the hostname. Unknown hostnames return 404. The `.preview.localhost` namespace is reserved.

## Try the product

1. Open **Deployments**. Revision 42 is already running in `shared-r42`, with no tenant traffic. Open its preview.
2. Open **Tenants & domains**, click BetaCo's **Release**, and select `shared-r42`.
3. Open **Releases**. Watch preparation, activation, observation and success. BetaCo now sees r42; SmallCo remains on r41.
4. Deploy r42 into `acme-eu`, with deployment ID `acme-r42`. Request Acme's release. It stays **awaiting approval** until you approve it.
5. Deploy the registered `broken` revision into `shared-eu` as `shared-broken`. Release it to BetaCo. Its readiness endpoint succeeds but real requests return 503. Three consecutive failed probe rounds restore BetaCo's previous assignment.
6. On **Overview**, stop one gateway. After health detection, refresh a tenant page. The other gateway serves it. Start the gateway again when finished.

The acceptance suite creates `acme-r42` and `shared-broken` if needed, so they may already appear in your workspace.

The example application displays its tenant, revision, cell and container hostname. `/api/info` returns the same information as JSON. Envoy adds `x-harbor-gateway`, `x-harbor-deployment` and `x-harbor-tenant` response headers. Forged tenant/deployment request headers are overwritten at the gateway.

The demo's r41/r42 use one image with different immutable environment settings for an obvious visual comparison. Real revisions can use different images or digests.

## CI/CD integration

The dependency-free `scripts/harborctl` uses the same HTTP API as the UI:

```sh
export HARBOR_URL=http://localhost:9090
export HARBOR_TOKEN=harbor-local-dev

./scripts/harborctl state
./scripts/harborctl revision r43 --image harbor-demo:local \
  --port 8080 --health-path /health --env REVISION=r43
./scripts/harborctl deploy shared-r43 --revision r43 --cell shared-eu --replicas 2
./scripts/harborctl release betaco shared-r43 --key pipeline-123-betaco --wait
```

For a real pipeline, build and push an image first, register its **digest**, deploy it, then request release. For example, use `--image ghcr.io/your-org/app@sha256:...`. Public images can be pulled by the controller. For private images in this MVP, pre-pull them into the local daemon; registry credential management is not implemented.

Revision metadata cannot be overwritten. The first deployment resolves the image to a Docker image ID; subsequent deployments of that revision reuse the recorded image. Using a digest at registration avoids a mutable-tag window before the first deployment.

`release --wait` exits 0 only on success. Failed, cancelled or rolled-back releases exit 1. A timeout also exits 1 but **does not cancel** the operation. Approval-required releases wait until an operator approves them. Use `--timeout 600` or separate request and wait pipeline steps.

`Idempotency-Key` applies to release creation: retries with the same tenant, target and observation window return the same operation; conflicting reuse is rejected. Revision/deployment IDs are unique, so duplicate creation returns an error rather than modifying existing records.

Other CLI commands:

```sh
./scripts/harborctl approve rel-EXAMPLE
./scripts/harborctl wait rel-EXAMPLE
./scripts/harborctl cancel rel-EXAMPLE
./scripts/harborctl rollback betaco
./scripts/harborctl retire unused-deployment
```

## API

All `/api/` requests require `Authorization: Bearer <token>`. JSON request bodies are strictly decoded. Cross-origin browser requests are rejected. The example token grants local operator access; it is not a multi-user identity system.

| Method / endpoint                                         | Request body / behaviour                                                                                                    |
| --------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/state`                                          | Desired/observed state, events, gateway ACKs, configuration version                                                         |
| `POST /api/cells`                                         | `{"id":"team-us","region":"us-local","dedicated":false}`                                                                    |
| `POST /api/revisions`                                     | `{"id":"r43","image":"harbor-demo:local","port":8080,"healthPath":"/health","env":{"REVISION":"r43"}}`                      |
| `POST /api/deployments`                                   | `{"id":"shared-r43","cell":"shared-eu","revision":"r43","replicas":2}`                                                      |
| `DELETE /api/deployments/{id}`                            | Retire an unreferenced deployment and remove its containers                                                                 |
| `POST /api/tenants`                                       | `{"id":"newco","name":"NewCo","cell":"shared-eu","deployment":"shared-r41","domains":["newco.localhost"],"approval":false}` |
| `PATCH /api/tenants/{id}`                                 | Replace name/domains/approval; placement and assignment remain unchanged                                                    |
| `POST /api/rollouts`                                      | `{"tenant":"betaco","deployment":"shared-r42","observeSeconds":20}`                                                         |
| `GET /api/rollouts/{id}`                                  | Operation state and observation counters                                                                                    |
| `POST /api/rollouts/{id}/approve`                         | Approve a waiting operation                                                                                                 |
| `POST /api/rollouts/{id}/cancel`                          | Cancel before activation, or restore the previous route afterwards                                                          |
| `POST /api/tenants/{id}/rollback`                         | Create a recovery release to the previous assignment; bypasses the normal approval gate                                     |
| `POST /api/gateways/{gateway-a\|gateway-b}/{start\|stop}` | Operate the two lab gateways for failure experiments                                                                        |

Example:

```sh
curl http://localhost:9090/api/rollouts \
  -H 'Authorization: Bearer harbor-local-dev' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: beta-release-42' \
  -d '{"tenant":"betaco","deployment":"shared-r42","observeSeconds":20}'
```

## Architecture

```text
UI / CLI / pipeline
        │ HTTP + JSON
        ▼
Go control plane ─── durable desired state + operation history
        │                         /data/state.json
        ├── Docker Runtime adapter → labelled backend containers
        │
        └── Envoy configuration compiler → go-control-plane snapshot cache
                                               ↕ ADS / protobuf / gRPC
Clients → HAProxy :8080 → Envoy A or B → assigned backend replicas
```

Envoys initiate the gRPC streams. LDS configures the listener; RDS maps tenant and preview domains; CDS defines a pool per deployment; EDS supplies ready container IPs. The controller publishes a snapshot whenever routing inputs change. Gateway ACKs are visible in the UI, and rollouts additionally probe real application routes on both gateways.

`Runtime` in `internal/platform/docker.go` is the seam for an ECS or VM adapter. It ensures an immutable deployment's replicas and retires unused deployments. Tenant policy and the xDS compiler do not need to know which compute provider produced those endpoints.

The controller reconciles every two seconds. State is written using a temporary file, file sync, atomic rename and directory sync on a Docker volume. It is a **single-writer controller**; do not run multiple controller instances against the same file. Desired operations survive restarts. Each controller process uses a new xDS version epoch so reconnecting gateways acknowledge a fresh snapshot even when its content is unchanged. During a management outage, existing Envoys retain their configuration. A newly started Envoy needs the controller to bootstrap.

Release states:

```text
awaiting_approval → preparing → activating → observing → succeeded
                                 │             │
                                 └── reverting ←┘ → rolled_back
```

Preparation requires ready capacity and convergence of both gateways. Observation counts two HTTP probes per round; `failures` counts failed rounds, not individual failed requests. Three consecutive failed rounds trigger rollback. Missing gateway convergence pauses observation; it never counts as a successful sample. A preparation timeout leaves the previous route active.

Rollback changes routing and verifies the previous assignment. It cannot undo application side effects. Old deployments are retained, and retirement is rejected while a tenant uses them or retains them as its previous assignment.

## Verification

### Formatting and static analysis

Development-only tools are pinned in `package-lock.json`, `requirements-dev.txt`, and the Makefile. They are not needed to run the Docker application.

Install Node.js/npm, Go, Python 3.10+, and ShellCheck, then prepare the tools:

```sh
npm ci
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r requirements-dev.txt
```

```sh
make format       # apply Go, web/config, Python and shell formatting
make quality      # check formatting, run all linters, run Go race tests
```

`make quality` includes gofmt, go vet, Staticcheck, Prettier, ESLint, Stylelint, Ruff, ShellCheck, shfmt, yamllint, Docker Compose validation and Dockerfile build checks. The Makefile downloads the pinned Go-based tools on first use. Docker must be available for configuration checks; this target does not restart or modify the running lab.

For individual checks, use `make format-check`, `make lint`, or `make test`. `make integration` is separate because it deliberately exercises outages and release changes in the local lab.

### Runtime acceptance tests

```sh
make test
python3 tests/integration.py
```

The Docker acceptance suite exercises custom-domain routing through both gateways and the edge, unknown-host rejection, forged-header replacement, isolated tenant release, enterprise approval, automatic rollback, control-plane outage, gateway failover, and durable recovery. It restores initial demo assignments and leaves the operation history for inspection. Run it only in this local lab: it deliberately stops its own gateway/control-plane containers.

## Stop and restart

```sh
./scripts/down.sh          # stop lab and backend containers; preserve desired state
./scripts/up.sh            # recreate and reconcile from saved state
./scripts/down.sh --reset  # remove the lab's state volume as well
```

Use these scripts instead of only `docker compose down`: backend containers are created dynamically and are not Compose services. Cleanup is restricted to the `harbor.owner=harbor-lab` label. Names, ports and the network are fixed; run one copy of this lab per Docker daemon.

## MVP boundaries

- HA demonstrates **gateway process failure on one machine**. HAProxy, Docker and the host remain shared failure points. Requests already in flight during a crash may fail; this is not a blanket zero-downtime guarantee.
- Cells enforce placement policy and dedicated backend containers, not separate networks/accounts or security boundaries. The demo application has no customer database or authentication. Tenant routing is not application authorisation.
- HTTP only. xDS is unencrypted inside the local Docker network, with no host-published xDS or admin port. Node IDs are an allowlist, not cryptographic identities. Production requires mTLS, trusted gateway identity and scoped configuration.
- The UI/API binds to host loopback. The controller has Docker-socket access and therefore substantial authority over the local Docker daemon. Keep this development lab local.
- Whole-tenant releases and synthetic checks only. Weighted cohorts, production metric analysis, maintenance windows, asset retention, background-worker versioning and database migrations are future work.
- One controller loop serializes reconciliation. Slow image pulls can delay other work. Durable distributed work queues and independent rollout analysis are the next scaling step.
- Replicas are chosen at creation (1–5); autoscaling, deployment resizing, tenant data migration and unbounded version support are not implemented. Changing a revision means creating a new revision/deployment.
- Backend images must serve HTTP on the specified port and work with the restricted container configuration. No host mounts or arbitrary Docker run options are exposed.

## Code map

- `internal/platform/model.go`: state, persistence, domain objects.
- `internal/platform/api.go`: validation, tenant ownership, release operations.
- `internal/platform/controller.go`: runtime reconciliation and rollout state machine.
- `internal/platform/xds.go`: Envoy resource compiler, ADS server and ACK tracking.
- `internal/platform/docker.go`: Docker runtime adapter and readiness probes.
- `web/`: embedded management UI, no frontend build dependency.
- `cmd/demo/`: observable revision-aware example destination.
- `scripts/harborctl`: portable Python CLI.
- `infra/`: Envoy bootstrap and HAProxy configuration.

Protocol references: [Envoy xDS](https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol), [go-control-plane](https://github.com/envoyproxy/go-control-plane), [HAProxy health checks](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/reliability/health-checks/).
