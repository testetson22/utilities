# Test Plan: FLPATH-3227 — DCM Network Provider (K8s Network SP)

**Epic:** [FLPATH-3227](https://redhat.atlassian.net/browse/FLPATH-3227) — DCM network provider
**QE Task:** [FLPATH-4865](https://redhat.atlassian.net/browse/FLPATH-4865) — TESTING: DCM Network provider
**Status:** Updated — embedded agent E2E coverage
**Assignee:** Vlad Kolodny
**SUT:** [dcm-project/environment-agent](https://github.com/dcm-project/environment-agent) — embedded `network` SP (`AGENT_EMBEDDED_SPS=network`)
**Enhancement:** [k8s-network-sp.md](https://github.com/dcm-project/enhancements/blob/main/enhancements/k8s-network-sp/k8s-network-sp.md) ([PR #65](https://github.com/dcm-project/enhancements/pull/65))

> **Architecture note:** Network is no longer deployed as a standalone
> `k8s-network-service-provider` container. It is embedded in the environment-agent
> binary. [FLPATH-4881](https://redhat.atlassian.net/browse/FLPATH-4881) (missing Quay
> image for the standalone SP) is **Obsolete**. The utilities
> `--k8s-network-service-provider` flag / compose override is **legacy** — do not use
> for new QE.

## Summary

The K8s Network SP implements CREATE, READ, and DELETE for the DCM `network` service
type. It maps requests to Kubernetes `Service` resources (ClusterIP, NodePort,
LoadBalancer). Service type is inferred from `routing_level` and
`provider_hints.kubernetes.node_ports`.

**v1 non-goals:** UPDATE/day-2 ops, Ingress, NetworkPolicy, DNS, service mesh.

## Scope of this document

This plan defines **automated utilities E2E** (Ginkgo) for the embedded network SP —
control-plane + environment-agent deploy, agent registration, catalog, and thin
happy-path checks against a real cluster (Kind or OCP) and DCM stack.

**Execution mode:** Ginkgo tests in `tests/e2e/sp_network_api_test.go`, run via
`tests/run-e2e.sh` (see [Test implementation](#test-implementation)). Each E2E-*
case below maps to one or more `It` blocks. Manual curl/kubectl steps are
reference only for debugging — not the deliverable.

> **Delivery note:** The automated suite is implemented in
> `tests/e2e/sp_network_api_test.go`. Execution evidence is recorded in
> [FLPATH-4865](https://redhat.atlassian.net/browse/FLPATH-4865).

It does **not** duplicate unit/integration coverage owned by the environment-agent
(and any leftover standalone SP repo plans). See [Test layer ownership](#test-layer-ownership).

**Auth / tenant isolation:** Out of scope for FLPATH-4865. Lab runs with
`AUTH_DISABLED=true`. Allowed/denied auth coverage lives in
[FLPATH-3254](./FLPATH-3254-dcm-authentication-test-plan.md) (and related auth
stories such as FLPATH-4645).

### API conventions (match existing utilities E2E)

- **DCM agents (not providers):** Control-plane uses `GET /api/v1alpha1/agents`.
  Filter `service_types` for `network` (`discoverAgentByServiceType` in
  `tests/e2e/api_helpers_test.go`).
- **Agent-local providers:** Environment-agent still exposes
  `GET /api/v1alpha1/providers` (embedded + external SPs on that agent). Use this
  to confirm `network` is enabled on the agent (`localhost:8081`).
- **Service types:** `GET /api/v1alpha1/service-types`, filter `results` for
  `service_type: network` (same pattern as `core_platform_test.go`).
- **Creates:** Prefer control-plane catalog / instance APIs (and NATS routing to
  the agent). There is **no** standalone host port **8090** network SP HTTP API
  in the embedded model.

**Related (out of scope here):** [FLPATH-4762](https://redhat.atlassian.net/browse/FLPATH-4762) UI
integration (ON_QA) — optional follow-up smoke.

## Test layer ownership

Each concern has **one primary owner**. Do not re-implement the same scenario in
another layer unless the row below says otherwise.

| Concern | Unit (agent / SP code, fake K8s) | Integration (agent, real cluster) | E2E (utilities, this plan) |
|---------|----------------------------------|-----------------------------------|----------------------------|
| Config / defaults | **Owner** — agent embedded network | — | — |
| Request validation matrix | **Owner** | — | Smoke — E2E-08, E2E-11 |
| Service type inference (all v1 paths) | **Owner** | Verify on cluster | **Owner** — E2E-04, E2E-06, E2E-09–E2E-13 |
| Status mapping (PENDING/READY/DELETED) | **Owner** | Verify on cluster | — |
| Handler error mapping (409, 404, etc.) | **Owner** | **Owner** — real K8s | — |
| Registration / embedded enablement | Agent startup | Agent + CP | E2E-01, E2E-02 |
| Health | Agent `/health` + provider list | — | E2E-01 |
| CloudEvents + informer | **Owner** | **Owner** | — |
| CP + agent deploy (Kind/compose) | — | — | **Owner** — E2E-01 |
| Catalog `network` service type | — | — | **Owner** — E2E-03 |
| CatalogItem → Instance → K8s Service | — | — | **Owner** — E2E-04, E2E-09, E2E-10, E2E-12, E2E-13 |
| LoadBalancer PENDING (no LB controller) | — | **Owner** | E2E-06 smoke (`no-lb-controller`) |
| LoadBalancer READY (MetalLB / cloud LB) | — | **Owner** | E2E-12 (`requires-lb-controller`) |

### Upstream plans (source of truth for non-E2E)

| Layer | Location |
|-------|----------|
| Embedded network implementation | `environment-agent/internal/embedded/network/`, `internal/openshift/network/` |
| Agent deploy | `environment-agent/deploy/DEPLOY.md`, `deploy/docs/compose-kind.md` |
| Standalone SP repo (legacy) | `k8s-network-service-provider` — not the QE deploy path |

## Prerequisites

- Go 1.23+, Ginkgo/Gomega (same as existing utilities E2E)
- Kind or OpenShift/Kubernetes; kubeconfig on deploy host
- Cluster CLI: `CLUSTER_CLI` = `oc` or `kubectl`
- Sibling repos: `control-plane`, `environment-agent`, `utilities` (Kind helpers)
- DCM control-plane compose with **auth disabled** (`AUTH_DISABLED=true` — default)
- Environment-agent with `AGENT_EMBEDDED_SPS` including `network`
- Namespace for network Services (default or e.g. `dcm-network-test`)
- Host ports **8080** (control-plane) and **8081** (environment-agent) free

> **CI note:** On Ecosystem Jenkins `flightpath-dcm-deploy`, control-plane host
> port may be remapped **8080 → 9080** (FLPATH-4421). Set
> `DCM_GATEWAY_URL=http://localhost:9080/api/v1alpha1` in CI.

### Local deploy (no auth) — reference

```bash
# Control-plane (AUTH_DISABLED=true by default)
cd ../control-plane && make compose-up
curl -sf http://localhost:8080/api/v1alpha1/health

# Environment-agent + embedded network
cd ../environment-agent
cp deploy/.env.example deploy/.env
# In deploy/.env set at least:
#   AGENT_EMBEDDED_SPS=network
#   DCM_REGISTRATION_URL=http://host.docker.internal:8080
#   AGENT_MESSAGING_URL=nats://host.docker.internal:4222

make kubeconfig-for-compose
make compose-up          # or compose-up-with-nats
make kind-connect
make deploy-verify

curl -sf http://localhost:8081/api/v1alpha1/health
curl -s http://localhost:8081/api/v1alpha1/providers   # expect network
curl -s http://localhost:8080/api/v1alpha1/agents       # expect service_types: network
```

### Run automated tests

```bash
# Stack already up (preferred while deploy wiring catches up to agent)
./tests/run-e2e.sh --skip-deploy --skip-teardown --label-filter 'sp && network'

# Future: harness deploy that starts CP + agent with AGENT_EMBEDDED_SPS=network
```

**Phase B gate (E2E-04+):** Environment-agent image/binary with embedded network
CRUD available (agent `main` / Quay `environment-agent:main`). No standalone
network SP Quay image required.

## Port map

| Service | Host URL |
|---------|----------|
| DCM control-plane API | `http://localhost:8080/api/v1alpha1/` |
| Environment-agent API | `http://localhost:8081/api/v1alpha1/` |
| Agent health | `http://localhost:8081/api/v1alpha1/health` |
| Agent providers (embedded SPs) | `http://localhost:8081/api/v1alpha1/providers` |
| DCM agents | `http://localhost:8080/api/v1alpha1/agents` |

If host 8080 is occupied, use compose override `9080:8080` and set
`DCM_GATEWAY_URL=http://localhost:9080/api/v1alpha1`.

> Lab compose uses **HTTP only** by design; TLS is out of scope for FLPATH-4865.

## Service type inference (reference)

Network status vocabulary:

The Network E2E reads the Network SP status through the control-plane
`/service-type-instances/{id}` endpoint. That endpoint persists the Network SP
CloudEvent status in lowercase. Do not apply the container/VM `RUNNING`
lifecycle expectation to Network resources.

| `routing_level` | `node_ports` | K8s Service type | STI status through control-plane | Expected condition |
|-----------------|--------------|------------------|----------------------------------|--------------------|
| omitted | No | ClusterIP | `ready` | Service is usable |
| omitted | Yes | NodePort | `ready` | Service is usable |
| `network` | No | LoadBalancer | `pending` without an LB controller; `ready` with MetalLB/cloud LB | External address determines readiness |
| `network` | Yes | LoadBalancer | same as row above | External address determines readiness |
| `application` | — | — | `failed` | Ingress is not supported in v1 |

PENDING is **not** a universal pass or fail — it depends on LB availability and
provider/NATS wiring (see KB `facts/flightpath/dcm.md`).

## E2E test cases (utilities)

Ginkgo labels (mirror container SP: `Label("sp", "container")`):

| Label | Meaning |
|-------|---------|
| `sp`, `network` | All network SP tests |
| `lab-default` | Any cluster with kubeconfig |
| `crud` | Requires embedded network CRUD via CP/agent (Phase B) |
| `no-lb-controller` | No cloud LB / MetalLB — E2E-06 only |
| `cluster` | Needs `$CLUSTER_CLI` cluster assertions |
| `contract` | RFC 9457 API contract (E2E-08) |

### Phase 1 — Deploy and DCM wiring (P0)

#### E2E-01: Deploy stack with embedded network `lab-default`

**Ginkgo:** `BeforeSuite` / deploy precondition + `Context("health")` in
`sp_network_api_test.go`.

| Step | Action | Expected |
|------|--------|----------|
| 0 | Verify host ports free: `8080`, `8081` | No conflicting listener |
| 1 | Bring up control-plane (`AUTH_DISABLED=true`) + environment-agent with `AGENT_EMBEDDED_SPS=network` + Kind connect | Exit 0 |
| 2 | `curl -sf localhost:8080/api/v1alpha1/health` | DCM healthy |
| 3 | `curl -sf localhost:8081/api/v1alpha1/health` | Agent healthy |
| 4 | `GET localhost:8081/api/v1alpha1/providers` | Entry for service type `network` |

#### E2E-02: Network agent registered in DCM `lab-default`

| Step | Action | Expected |
|------|--------|----------|
| 1 | `GET /api/v1alpha1/agents` (page if needed) | HTTP 200 |
| 2 | Find agent where `service_types` contains `network` | Agent present |
| 3 | Check agent endpoint / metadata | Registration reflects `network` capability |

Reference: `discoverAgentByServiceType("network", "")` in `api_helpers_test.go`.

#### E2E-03: Catalog network service type `lab-default`

| Step | Action | Expected |
|------|--------|----------|
| 1 | `GET /api/v1alpha1/service-types` | HTTP 200, `results` array |
| 2 | Filter `results` for `service_type: network` | Entry found |
| 3 | Inspect schema for that entry | Portable fields `ports`, `routing_level`, and `endpoints` are present. Kubernetes-specific provider hints are exercised through provisioning tests. |

### Phase 2 — Provisioning smoke (P0 — `crud` label)

Creates go through the **control-plane** (catalog / instances → agent), not a
direct `:8090` SP HTTP API. Covers each **v1 service-type inference** path once:

| Inference case | E2E ID |
|----------------|--------|
| ClusterIP (no `routing_level`, no `node_ports`) | E2E-04 |
| NodePort (`node_ports` present, no `routing_level`) | E2E-09 |
| LoadBalancer (`routing_level: network`, no `node_ports`) | E2E-06 |
| LoadBalancer + specified `node_ports` | E2E-10 |
| `routing_level: application` (unsupported v1) | E2E-11 |
| Headless ClusterIP (`cluster_ip: None`) | E2E-13 |

#### E2E-04: Create and get ClusterIP `lab-default`, `crud`, `cluster`

Provision via catalog item instance with no `routing_level`, `node_ports`, or
`cluster_ip` (`metadata.name`: `e2e-clusterip-smoke`, port 80 → 8080).

| Step | Action | Expected |
|------|--------|----------|
| 1 | Create catalog item + routing policy + instance | HTTP 201 |
| 2 | Poll the service-type instance until ready (or timeout) | STI status `ready` |
| 3 | GET service-type instance | Spec matches create; status `ready` |
| 4 | `$CLUSTER_CLI get svc e2e-clusterip-smoke -n <ns>` | `TYPE=ClusterIP`, allocated (not `None`) ClusterIP, DCM labels |
| 5 | Spec round-trip on instance / cluster object | `protocol: TCP`, `port: 80`, `target_port: 8080` |

#### E2E-05: Delete smoke `lab-default`, `crud`, `cluster`

| Step | Action | Expected |
|------|--------|----------|
| 1 | Delete instance / network resource from E2E-04 | HTTP 2xx / 204 |
| 2 | `$CLUSTER_CLI get svc -n <ns>` | Service removed |
| 3 | GET same resource id | HTTP 404 |

#### E2E-09: Create and get NodePort `lab-default`, `crud`, `cluster`

No `routing_level`; `provider_hints.kubernetes.node_ports` maps port name → NodePort
(30000–32767). Unique `metadata.name` and unused NodePort.

```json
{
  "spec": {
    "service_type": "network",
    "metadata": { "name": "e2e-nodeport-smoke" },
    "ports": [{ "name": "http", "protocol": "TCP", "port": 80, "target_port": 8080 }],
    "provider_hints": {
      "kubernetes": {
        "node_ports": { "http": 30080 }
      }
    }
  }
}
```

| Step | Action | Expected |
|------|--------|----------|
| 1 | Provision via CP | HTTP 201; poll service-type instance until `ready` |
| 2 | GET service-type instance | Spec includes `node_ports`; status `ready` |
| 3 | `$CLUSTER_CLI get svc e2e-nodeport-smoke -n <ns>` | `TYPE=NodePort`; `protocol: TCP`, `port: 80`, `targetPort: 8080`, `nodePort` 30080 |
| 4 | Cleanup | Service + CP resources deleted |

#### E2E-10: LoadBalancer with specified node_ports `lab-default`, `crud`, `cluster`

`routing_level: "network"` with explicit `node_ports`.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Provision via CP | HTTP 201; poll service-type instance according to the controller mode |
| 2 | GET service-type instance | Spec includes `routing_level` + `node_ports` |
| 3 | `$CLUSTER_CLI get svc … -o yaml` | `type: LoadBalancer`; `protocol: TCP`, `port: 80`, `targetPort: 8080`, `nodePort` as requested |
| 4 | Service-type instance status | If labeled `no-lb-controller`: stay `pending` (~2 min). If MetalLB/cloud: poll until `ready`. Do **not** accept either on every cluster. |
| 5 | Cleanup | Service + CP resources deleted |

#### E2E-13: Headless ClusterIP `lab-default`, `crud`, `cluster`

`cluster_ip: None` is explicitly supplied. This is separate from E2E-04 so
the regular ClusterIP default and the headless behavior cannot mask each other.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Provision with `cluster_ip: None` and no `routing_level` or `node_ports` | HTTP 201; poll until `READY` |
| 2 | `$CLUSTER_CLI get svc` | `TYPE=ClusterIP`; `clusterIP: None`; DCM labels |
| 3 | GET service-type instance | `agent_name` matches the selected agent; status `ready` |

#### E2E-11: `routing_level: application` fails provisioning `lab-default`, `crud`, `contract`

| Step | Action | Expected |
|------|--------|----------|
| 1 | Attempt provision with `routing_level: application` | Create may be accepted by the asynchronous CP API |
| 2 | Poll service-type instance | Terminal `failed` status |
| 3 | `$CLUSTER_CLI get svc …` | Service not created |

### Phase 3 — LoadBalancer environment variants (P1)

#### E2E-06: LoadBalancer stays PENDING without LB controller `no-lb-controller`, `crud`, `cluster`

**Precondition:** No cloud LB / MetalLB (bare Kind). Skip on EKS/GKE/ROSA.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Provision with `routing_level: "network"`, no `node_ports` | HTTP 201; service-type instance remains `pending` |
| 2 | `$CLUSTER_CLI get svc` | `TYPE=LoadBalancer`, no external IP |
| 3 | Poll service-type instance ~2 min | Still `pending` (**pass** only on `no-lb-controller`) |

#### E2E-12: Controller-backed LoadBalancer reaches READY `requires-lb-controller`, `crud`, `cluster`

**Precondition:** A load-balancer controller is available. MetalLB is detected
automatically; cloud environments must set `DCM_NETWORK_LB_MODE=cloud`.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Provision with `routing_level: "network"`, no `node_ports` | HTTP 201; poll until `READY` |
| 2 | `$CLUSTER_CLI get svc` | `TYPE=LoadBalancer` with an external IP or hostname |
| 3 | GET service-type instance | `agent_name` matches the selected agent; status `ready` |

### Full DCM flow coverage

E2E-04, E2E-09, E2E-10, E2E-12, and E2E-13 each exercise the complete
catalog item → routing policy → catalog-item instance → selected agent →
Kubernetes Service flow. They assert the selected agent name and Kubernetes
Service labels, so no duplicate E2E-07 case is needed.

### Phase 5 — API contract smoke (P2)

#### E2E-08: Validation error returns RFC 9457 problem body `lab-default`, `crud`, `contract`

**E2E owns one contract smoke only.** Full validation matrix (bad service name,
duplicate/invalid ports, NodePort out of 30000–32767, duplicate NodePort,
malformed `provider_hints`, create conflict) stays in **unit** (agent/SP) —
see [Test layer ownership](#test-layer-ownership).

Concrete payload: `POST` catalog-item-instance (or create path) with body `{}`
(missing required `service_type` / `metadata.name`).

| Step | Action | Expected |
|------|--------|----------|
| 1 | POST create via CP with body `{}` | HTTP 400 |
| 2 | `Content-Type` | `application/problem+json` |
| 3 | Body | RFC 9457 fields: `type`, `title`, `status` |

## Monitoring (deferred)

NATS CloudEvents and informer behavior — agent/SP integration ownership.
Do not add parallel TCs in utilities until monitoring stories close.

## Cleanup

**Automated:** ordered-context `AfterAll` and per-test cleanup in
`sp_network_api_test.go`.

- Prefer a **dedicated test namespace** (e.g. `dcm-network-e2e`) or, if using
  `default`, a **unique run label** such as
  `dcm.project/e2e-run=<suite-uuid>` on every catalog item, policy, instance,
  and Kubernetes Service created by the suite.
- Delete by that label (or by recorded UIDs/names) — **not** a bare
  `managed-by=dcm` wipe of the whole namespace.
- Verify removal of: catalog items, policies, catalog-item-instances, and
  Kubernetes Services created by the run.
- Stack teardown: agent `make compose-down`, control-plane `make compose-down`.

## FLPATH-4865 exit criteria

**Automated** — `run-e2e.sh` with `--label-filter 'sp && network'` (stack pre-deployed or harness-updated).

Phase A is a **wiring gate** only (deploy / register / catalog). It does **not**
exercise network CREATE/DELETE. **FLPATH-4865 remains incomplete until Phase B
(E2E-04–E2E-13, with the environment-applicable LoadBalancer variant) passes**
(or is explicitly waived with linked follow-up).

| Level | Required tests | Notes |
|-------|----------------|-------|
| **Phase A (wiring)** | E2E-01–E2E-03 green | CP + agent deploy, agent registration, catalog — **not** sufficient alone for FLPATH-4865 |
| **Phase B (CRUD + inference)** | E2E-04–E2E-11, E2E-13 green; E2E-06 or E2E-12 as applicable | Required for FLPATH-4865 complete; includes E2E-08/11 contract smokes |
| **Deferred** | UI (FLPATH-4762); alternate LB environment | Run E2E-06 without an LB controller or E2E-12 with MetalLB/cloud LB; auth → FLPATH-3254 |

## Key configuration

| Variable | Default / example | Purpose |
|----------|-------------------|---------|
| `AUTH_DISABLED` | `true` | Lab QE default; auth/tenant out of scope (see FLPATH-3254) |
| `AGENT_EMBEDDED_SPS` | `network` (or `container,network`, …) | Enable embedded network SP |
| `DCM_REGISTRATION_URL` | `http://host.docker.internal:8080` | Agent → CP registration base URL |
| `AGENT_MESSAGING_URL` | `nats://host.docker.internal:4222` | Agent NATS (CP NATS on host) |
| `SP_K8S_NAMESPACE` | `dcm-network-e2e` (prefer) or `default` | Namespace for Services; pair with unique `e2e-run` label |
| `AGENT_KUBECONFIG_HOST` | `.kube/config` | Compose-friendly kubeconfig (`https://kubernetes:6443`) |
| `DCM_GATEWAY_URL` | `http://localhost:8080/api/v1alpha1` | Ginkgo → control-plane |
| `DCM_AGENT_URL` | `http://localhost:8081/api/v1alpha1` | Ginkgo → environment-agent (health/providers) |
| `DCM_NETWORK_LB_MODE` | auto-detect MetalLB; `cloud` for a cloud LB | Select `none`, `metallb`, or `cloud` expectations |

## Test implementation (utilities)

| Item | Path / action |
|------|----------------|
| **Test file** | `tests/e2e/sp_network_api_test.go` — `Describe("Network SP API", Label("sp", "network"), ...)` |
| **Helpers** | Extend `sp_helpers_test.go` / agent helpers: require agent with `network`, provision via CP APIs |
| **Control-plane** | Reuse `doRequest`, `discoverAgentByServiceType("network", ...)`, `expectRFC9457Problem` |
| **Cluster asserts** | Use `$CLUSTER_CLI` with the **same** namespace as agent `SP_K8S_NAMESPACE` (prefer `dcm-network-e2e`). Do **not** reuse bare `initKubectl()` — it reads `K8S_CONTAINER_SP_NAMESPACE` / `default` and will miss network Services. Pass ns explicitly or extend helpers for network. |
| **Run** | After [FLPATH-4914](https://redhat.atlassian.net/browse/FLPATH-4914): `--skip-deploy --label-filter 'sp && network'` (stack pre-deployed until harness starts agent) |
| **Rollout** | Phase A (E2E-01–03) first; Phase B when embedded CRUD path is stable |
| **Legacy** | Do not rely on `--k8s-network-service-provider` / Quay `k8s-network-service-provider` |

### Phase A vs B (implementation order)

| Phase | E2E IDs | Depends on |
|-------|---------|------------|
| **A** | E2E-01–03 | CP + environment-agent with `AGENT_EMBEDDED_SPS=network` |
| **B** | E2E-04–13 | Embedded network CRUD via CP → agent |

## Dev implementation backlog (agent — not utilities)

| Area | Notes |
|------|--------|
| Unit — validation, handlers, inference | `environment-agent/internal/openshift/network/` |
| Integration — CRUD on Kind | Agent deploy docs + Kind |
| NATS / informer | Agent monitoring paths |
