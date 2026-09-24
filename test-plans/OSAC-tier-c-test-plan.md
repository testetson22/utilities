# OSAC SP — Tier C Test Plan (Real Infrastructure)

| Field | Value |
|---|---|
| **Epic** | [FLPATH-4758](https://redhat.atlassian.net/browse/FLPATH-4758) — [Test Plan] Testing for DCM: OSAC provider |
| **Product epic** | [FLPATH-4459](https://redhat.atlassian.net/browse/FLPATH-4459) — DCM: OSAC service provider |
| **Status** | Draft — plan only; Tier C Ginkgo cases not yet implemented |
| **Depends on** | Upstream Tier B (`TC-TB-*`, kind stack in `osac-service-provider`) + utilities self-contained OCP backend (Tier B stack port) + `make test-osac-sp` API lifecycle |

### Where utilities sits today

Upstream Tier B is **kind** + real Postgres/Keycloak/fulfillment-service (`TC-TB-*` in `osac-service-provider`). Tier C (this plan) is **real MOC/AAP/BMC**. Utilities today is in between: an **OCP port of the Tier B stack** (`deploy-osac-backend.sh`, same `tierb-*` creds) plus DCM-stack `sp_osac_*` API/NATS tests — not kind, and not real MOC. Phase 2 pieces (operator, BMFO, aap-mock, BMH fixtures) extend that middle ground toward provisioning realism without crossing into Tier C.

### References

| Ticket | Role | Notes |
|--------|------|-------|
| [FLPATH-4758](https://redhat.atlassian.net/browse/FLPATH-4758) | Test Plan epic | Parent for OSAC SP E2E work in utilities / osac-sp |
| [FLPATH-4760](https://redhat.atlassian.net/browse/FLPATH-4760) | Closest existing story | Originally “OCP + real OSAC backend E2E”. Utilities’ OCP Tier B stack port + API lifecycle already covers a large middle slice of that; **Tier C** is the remaining real-MOC/AAP/BMC path. Prefer updating 4760’s DoD to that split, or filing a new child under 4758 for Tier C |
| [FLPATH-4759](https://redhat.atlassian.net/browse/FLPATH-4759) | Sibling (out of scope) | Tier A — kind + mock-provider; lives primarily in `osac-service-provider` |
| [FLPATH-4924](https://redhat.atlassian.net/browse/FLPATH-4924) | Product blocker (under 4459) | `GET /clusters/{id}` returns 500 for ACTIVE clusters (`GetKubeconfig` removed). Blocks TC-TC-130 / TC-TC-135 until fixed |
| *(file under 4758)* | Tier C CI pipeline | Replaces the former `FLPATH-TBD` placeholder in Future CI Integration — create when MOC CI env is available |

## Overview

This document covers the **Tier C** (real infrastructure) test suite for the OSAC Service Provider
(osac-service-provider). Tier C is additive to Tiers A and B — it does not replace them.

### What Distinguishes Tier C

| Dimension | Tier A (mock) | Tier B (kind / utilities OCP port) | Tier C (real infra) |
|-----------|--------------|-------------------------------------|-------------------|
| Host | kind + in-process/mock backend | Upstream: kind + real ffs stack; utilities: OCP + same stack port | Dedicated MOC / real OSAC env |
| AAP | None (in-process mock) | `osac-aap-mock` when Phase 2 present (instant success); absent in Phase 1-only | Real AAP instance; real job templates execute |
| Bare metal | None | Static `BareMetalHost` fixtures when Phase 2 present; no real BMC | Real BareMetalHost objects backed by real BMC/Ironic; actual hardware boots |
| Cluster nodes | None | `HostedCluster` stub; never becomes Ready | Real OpenShift nodes come up |
| Kubeconfig | Never populated | Never populated (cluster stays PROGRESSING) | Real kubeconfig; `kubectl` connects to the provisioned cluster |
| Network | None | Not exercised (cluster path only; no NetworkClass) | Real CIDR allocation; actual network provisioning path |
| VM compute | None | Blocked — no NetworkClass in test backend | Pre-seeded NetworkClass + VirtualNetwork; VM provisioning path is real |
| Credentials | Static in-code mock | Static test-only `tierb-*` values (committed to git) | Real MOC/OSAC credentials (external; must be injected at runtime) |
| Provisioning time | Milliseconds | Seconds–minutes (BMFO reconciles; AAP mock returns instantly) | **10–30 minutes** per cluster; VM provisioning similarly slow |
| Test IDs | Tier A / mock E2E in osac-sp | `TC-TB-*` (osac-sp); utilities `sp_osac_*` (not `TC-TB-*`) | `TC-TC-*` (this plan; not yet in Go) |

### When to Run

Tier C is **not a per-PR gate**. Run it:

- **Nightly** against a dedicated MOC test environment, when one is available
- **Before a milestone or release** to validate end-to-end flow on real hardware
- **When AAP templates, BMFO logic, or the osac-operator change** in ways that Tier B cannot catch
- **Ad-hoc** when debugging a suspected real-infrastructure regression

Tier A and Tier B continue to run on every PR (Tier A) and on demand/nightly (Tier B).

---

## Infrastructure Prerequisites

### MOC / OSAC Environment

| Resource | Requirement | Notes |
|----------|-------------|-------|
| MOC OpenShift cluster | Dedicated Tier C test project/namespace | Must not share state with other test runs |
| `fulfillment-service` | The same build under test, deployed as in Tier B | Deployed to `osac-tierc-backend` (or equivalent) namespace; TLS via cert-manager |
| Keycloak | Real `osac` realm with a test client | May reuse Tier B `ffs-keycloak` deployment wired to real OIDC issuer |
| PostgreSQL | Real Postgres instance (not shared with production) | Same as Tier B deployment pattern |
| `osac-operator` | Deployed and healthy | Watches `ClusterOrder` CRs; drives the provisioning FSM |
| BMFO | Deployed and healthy | Manages `BareMetalInstance` CRs; communicates with Ironic |
| Ironic | Real Ironic instance with BMC access | Connected to the bare metal pool below |
| NetworkClass | Pre-seeded by infra admin | Required for `CreateComputeInstance`; cannot be created via public API |
| VirtualNetwork | Pre-seeded per project/namespace | Provides `ipv4_cidr`/`ipv6_cidr` for VM provisioning |

### Bare Metal Pool

| Resource | Requirement |
|----------|-------------|
| Physical servers | ≥ 3 hosts available and `Available` in Ironic for cluster provisioning; ≥ 1 for VM |
| BMC access | Each host reachable via IPMI or Redfish from the Ironic service |
| `BareMetalHost` CRs | Pre-registered in the BMFO namespace; initial state `Available` |
| Network | Provisioning network configured; nodes can PXE-boot and reach the machine network |

### Ansible Automation Platform

| Resource | Requirement |
|----------|-------------|
| AAP instance | Dedicated Tier C AAP (or isolated project within shared AAP) |
| Cluster job template | Job template ID pre-configured in fulfillment-service; provisions an OCP cluster |
| VM job template | Job template ID pre-configured for compute instance provisioning |
| Service account | Credentials used by fulfillment-service to launch AAP jobs |
| Inventory | Pre-configured with the bare metal pool; dynamic inventory populated by BMFO |

### Test Runtime

Variables are grouped by source. Variables generated by `deploy-osac-backend.sh` (written to
`deploy/osac-backend.env`) are consumed automatically by `deploy-dcm.sh`; do **not** set them
manually unless you are connecting to a non-Tier-B fulfillment-service.

**Already produced by `deploy-osac-backend.sh` / `deploy-dcm.sh` (Tier B → Tier C carry-over):**

| Variable | Description | Produced by |
|----------|-------------|-------------|
| `SP_OSAC_FULFILLMENT_ADDRESS` | gRPC endpoint (host:443) | `deploy/osac-backend.env` |
| `SP_OSAC_OIDC_ISSUER_URL` | Keycloak issuer URL | `deploy/osac-backend.env` |
| `SP_OSAC_OIDC_CLIENT_ID` | OIDC client ID (default `osac-admin`) | `deploy/osac-backend.env` |
| `SP_OSAC_OIDC_CLIENT_SECRET` | OIDC client secret | `deploy/osac-backend.env` |
| `OSAC_CA_CERT_FILE` | Path to CA cert PEM | `deploy/osac-backend.env` |

**Set by the operator at test time:**

| Variable | Description | Example |
|----------|-------------|---------|
| `OSAC_E2E_CLUSTER_TEMPLATE_ID` | ClusterTemplate ID from fulfillment-service | `default-hcp` (Tier B); real template ID for Tier C |
| `OSAC_E2E_VM_TEMPLATE_ID` | VM template ID (required for VM tests) | `12` |
| `OSAC_E2E_GUEST_OS_TYPE` | Guest OS for VM tests (default `rhel-9`) | `rhel-9` |
| `OSAC_E2E_INSTANCE_TYPE` | VM instance type (default `standard-4-16`) | `standard-4-16` |
| `DCM_OSAC_SP_URL` | OSAC SP HTTP endpoint (from DCM stack) | `http://localhost:8091/api/v1alpha1` |
| `DCM_NATS_URL` | NATS server | `nats://localhost:4222` |

**Tier C–only (proposed; not yet implemented in Go test code):**

| Variable | Description | Default |
|----------|-------------|---------|
| `OSAC_TIER_C_CLUSTER_PROVISION_TIMEOUT` | Poll timeout for cluster reaching ACTIVE | `30m` |
| `OSAC_TIER_C_VM_PROVISION_TIMEOUT` | Poll timeout for VM reaching RUNNING | `20m` |

### Credential Management

Real MOC/OSAC credentials **must not** be committed to git.
- Store in HashiCorp Vault or a secrets manager; inject via environment at runtime.
- Tier B `tierb-*` static credentials are never used in Tier C.
- Rotate Tier C credentials on the same schedule as the MOC environment's policy.

---

## Relationship to Tier B

**Tier B remains valid.** Tier B tests are not superseded by Tier C — they run faster,
require no external credential management, and provide deterministic coverage of the
API contract. Tier C is additive: it verifies the full end-to-end path that Tier B
deliberately fakes.

The table in each test case below explicitly calls out whether the case:

- **Supersedes** a Tier B case (the same scenario but with real infrastructure; Tier B
  continues to run its version too — they are complementary, not exclusive)
- **Extends** a Tier B case (same starting scenario, but asserts on outcomes that are
  unreachable in Tier B due to AAP mock / static fixtures)
- **Net-new** (tests scenarios that have no Tier B equivalent at all)

---

## Out of Scope

The following are deliberately excluded from Tier C (already covered at lower tiers):

| Topic | Covered by | Rationale |
|-------|-----------|-----------|
| Input validation (missing fields, wrong types, empty body) | Tier A + Tier B | Pure SP logic; independent of infrastructure |
| RFC 9457 error shape | Tier A + Tier B | SP-layer behavior; independent of backend state |
| AEP-132 list response shape (`results` key, non-null array) | Tier B | Response contract tested against real backend at Tier B |
| Idempotency (`?id=`, first-write-wins) | Tier B | Contract is SP-layer; does not require a running cluster |
| Delete idempotency (204 on re-delete) | Tier B | SP-layer contract |
| `max_page_size` query validation | Tier B | SP/backend contract; no real provisioning required |
| OIDC token endpoint claims | Tier B (TC-TB-020) | Keycloak config is identical; tested once at Tier B |
| CRD registration checks | Tier B (TC-TB-060) | Operator bootstrap; unaffected by AAP / bare metal |
| CloudEvent envelope schema (`specversion`, `type`, `source`, `datacontenttype`) | Tier B | Format contract; content is backend-independent |
| DCM authentication (Keycloak OIDC for the control-plane) | Tier B + `FLPATH-3254` | Separate test plan |
| ACM SP, KubeVirt SP, container SP | Their own test plans | Out of scope for this document |
| Performance benchmarking / SLA | TBD | Would require a dedicated perf environment |
| Chaos / fault injection | TBD | Not planned for initial Tier C |

---

## Test Cases

Test case IDs follow the pattern `TC-TC-NNN` (Tier C). Where a case has a Tier B counterpart,
the `Tier B ref` column cites the upstream `TC-TB-NNN` ID from
`dcm-project/osac-service-provider/.ai/test-plans/osac-sp-e2e-tier-b.test-plan.md`.

### Group 1 — Environment Setup and Connectivity

| TC ID | Test Name | Validates | Tier B ref | Description |
|-------|-----------|-----------|-----------|-------------|
| TC-TC-010 | OSAC SP reachable | `GET /clusters/health` returns 200 with `status: healthy` | Extends TC-TB-020 | Same as Tier B health check, but confirms the SP is connected to a real fulfillment-service and real Keycloak; health response `detail` must be absent (DD-010) |
| TC-TC-015 | VM health endpoint reachable | `GET /vms/health` returns 200 with `status: healthy` | Extends TC-TB-020 | Companion to TC-TC-010; verifies dual service-type health (REQ-DUAL-010) |
| TC-TC-020 | Environment-agent provider registration | `osac-sp-cluster` + `osac-sp-vm` appear in environment-agent `/providers` | Extends TC-TB-030 | Confirms providers registered with real fulfillment-service endpoint (not localhost mock) |
| TC-TC-025 | Keycloak token valid for fulfillment-service | OSAC SP can authenticate to fulfillment-service | Supersedes TC-TB-020 | Implicitly verified if TC-TC-010 returns `status: healthy`; explicit check: list clusters returns 200, not 401/502 |
| TC-TC-030 | osac-operator deployed and watching | `ClusterOrder` CRDs registered on cluster | Extends TC-TB-060 | Extends Tier B CRD check to confirm the operator is running and its controller is ready (not just CRDs registered) |
| TC-TC-035 | BMFO deployed and watching | `BareMetalInstance` CRDs registered; at least one `BareMetalHost` in `Available` state | Extends TC-TB-060 | Confirms bare metal inventory is available before running provisioning tests; skip block if pool is empty |
| TC-TC-040 | AAP reachable from fulfillment-service | Cluster provisioning test submission succeeds (not rejected with 502) | Net-new | Validates that fulfillment-service can reach AAP and launch a job; not testable in Tier B |

### Group 2 — Cluster Provisioning Lifecycle

> **Timing note:** Tests in this group poll for status transitions. Use `OSAC_TIER_C_CLUSTER_PROVISION_TIMEOUT`
> (default 30 min) for ACTIVE transitions. Earlier state transitions (CREATED → PROGRESSING) should
> complete within 2–3 minutes; fail the test early if this intermediate transition doesn't occur within 5 min.

| TC ID | Test Name | Validates | Tier B ref | Description |
|-------|-----------|-----------|-----------|-------------|
| TC-TC-100 | Cluster create returns 201 with ID | `POST /clusters?id=<name>` → 201 with `id` field | Supersedes TC-TB-200 (partial) | Same create call as Tier B CRUD, but against real AAP + real bare metal; asserts 201 + non-empty `id` |
| TC-TC-105 | Cluster reaches PROGRESSING within 5 minutes | Status transitions from PROVISIONING/PROGRESSING | Extends TC-TB-090 | In Tier B, ClusterOrder transitions happen within seconds via the aap-mock. In Tier C, AAP job starts are slower; assert PROGRESSING (or PROVISIONING) within 5 min, or fail early |
| TC-TC-110 | BareMetalHost moves to Provisioning | At least one `BareMetalHost` transitions from `Available` → `Provisioning` | Net-new | No Tier B equivalent — static fixtures don't exercise the BMC/Ironic provisioning path. Verify via `oc get baremetalhosts -n <bmfo-ns>` |
| TC-TC-115 | BareMetalInstance reaches Ready | `BareMetalInstance` CR reaches `Ready` phase in BMFO | Extends TC-TB-110/120 | TC-TB-110/120 uses static fixtures; TC-TC-115 verifies real BMFO reconciliation with real BMC |
| TC-TC-120 | ClusterOrder reaches Ready phase | `ClusterOrder` CR reaches `Ready` phase via real osac-operator | Extends TC-TB-090 | Same OSP condition as Tier B, but driven by real AAP job completion rather than the mock |
| TC-TC-125 | Cluster reaches ACTIVE status | `GET /clusters/<id>` returns `status: ACTIVE` within provisioning timeout | Net-new | The central Tier C assertion; not achievable in Tier B. Poll with `Eventually` up to `OSAC_TIER_C_CLUSTER_PROVISION_TIMEOUT` |
| TC-TC-130 | Kubeconfig present and non-empty when ACTIVE | `GET /clusters/<id>` → `kubeconfig` field is populated | Net-new | REQ-GET-030: kubeconfig must be non-empty when status is ACTIVE. In Tier B, clusters never reach ACTIVE so this can only be fully tested here. **Blocked by [FLPATH-4924](https://redhat.atlassian.net/browse/FLPATH-4924)** until SP migrates off `GetKubeconfig` |
| TC-TC-135 | Kubeconfig connects to a real cluster | `kubectl --kubeconfig=<decoded> get nodes` succeeds | Net-new | Go beyond shape assertion: decode the returned kubeconfig, write it to a temp file, run `kubectl get nodes`, assert at least one node is `Ready`. **Blocked by [FLPATH-4924](https://redhat.atlassian.net/browse/FLPATH-4924)** (same as TC-TC-130) |
| TC-TC-140 | CloudEvent published on `dcm.cluster` with ACTIVE status | NATS `dcm.cluster` subject carries `status: ACTIVE` event | Extends TC-TB-200 (NATS) | Tier B verifies a NATS event is published after create; TC-TC-140 specifically verifies an event with `status: ACTIVE` arrives (requires real provisioning to complete) |
| TC-TC-145 | Cluster list includes ACTIVE cluster | `GET /clusters` → created cluster appears with `status: ACTIVE` | Extends TC-TB-200 | Verifies list reflects real-time ACTIVE status from the backend |
| TC-TC-150 | Cluster delete initiates DELETING transition | `DELETE /clusters/<id>` → 204; subsequent GET shows DELETING or 404 | Supersedes TC-TB-200 (delete) | Same contract as Tier B delete; additionally asserts DELETING intermediate state is observable via NATS event before resource disappears |
| TC-TC-155 | Cluster reaches DELETED after delete | Eventually `GET /clusters/<id>` returns 404 (or final `status: DELETED` before 404) | Extends TC-TB-200 (delete) | In Tier B, delete is near-instant (aap-mock tears down immediately). In Tier C, real cluster deprovisioning takes minutes; poll to confirm |
| TC-TC-160 | Cluster NATS event on deletion | `dcm.cluster` carries a `status: DELETING` event after DELETE call | Extends TC-TB-200 (NATS) | Tier B only tests create-triggered events; TC-TC-160 tests the delete lifecycle event |

### Group 3 — VM Provisioning Lifecycle

> **Precondition:** A `NetworkClass` and at least one `VirtualNetwork` must be pre-seeded by an infra
> admin before this group can run. These are admin-only resources not createable via the public
> fulfillment-service API. Skip this group if `OSAC_E2E_VM_TEMPLATE_ID` is unset.

> **Timing note:** VM provisioning typically takes 10–20 minutes. Use
> `OSAC_TIER_C_VM_PROVISION_TIMEOUT` (default 20 min).

| TC ID | Test Name | Validates | Tier B ref | Description |
|-------|-----------|-----------|-----------|-------------|
| TC-TC-200 | VM create succeeds when NetworkClass is present | `POST /vms?id=<name>` → 201 with `id` | Net-new | In Tier B this is skipped because no `NetworkClass` exists. TC-TC-200 is the first time this path is tested end-to-end |
| TC-TC-205 | VM reaches RUNNING status | `GET /vms/<id>` returns `status: RUNNING` within VM provisioning timeout | Net-new | The central Tier C VM assertion; equivalent to TC-TC-125 for clusters |
| TC-TC-210 | VM has non-empty IP addresses when RUNNING | `internal_ip_address` and `external_ip_address` are non-empty in GET response | Net-new | REQ-VMGET-030: IP fields must be populated once the VM is RUNNING. Only verifiable with real network provisioning |
| TC-TC-215 | IP addresses appear in list results | `GET /vms` → VM entry has non-empty IP fields | Net-new | REQ-VMLIST-030: same IP population requirement applies to list entries; extends Tier B list-shape test |
| TC-TC-220 | CloudEvent published on `dcm.vm` with RUNNING status | NATS `dcm.vm` subject carries `status: RUNNING` event | Extends Tier B (NATS) | Tier B VM NATS test is skipped (no `OSAC_E2E_VM_TEMPLATE_ID`); TC-TC-220 tests the first real RUNNING event |
| TC-TC-225 | VM delete returns 204; VM eventually 404 | `DELETE /vms/<id>` → 204; GET eventually returns 404 | Extends Tier B VM CRUD | Same Tier B delete contract, but real deprovisioning path exercised |
| TC-TC-230 | NATS event on VM deletion | `dcm.vm` carries `status: DELETING` event after DELETE call | Net-new | Delete-lifecycle event for VMs; counterpart to TC-TC-160 |

### Group 4 — Failure and Recovery Paths

| TC ID | Test Name | Validates | Tier B ref | Description |
|-------|-----------|-----------|-----------|-------------|
| TC-TC-300 | Cluster reaches FAILED on AAP job failure | `GET /clusters/<id>` returns `status: FAILED`; `message` field non-empty | Net-new | Submit a cluster create with an intentionally invalid template ID or a template that will fail. Confirm FAILED status and non-empty `message` (DD-010 detail propagation). Must clean up via DELETE |
| TC-TC-305 | NATS event carries FAILED status | `dcm.cluster` publishes `status: FAILED` event with non-empty `message` | Net-new | Extends TC-TC-300; verifies failure information propagates through the NATS event pipeline |
| TC-TC-310 | VM reaches FAILED on provisioning error | `GET /vms/<id>` returns `status: FAILED`; `message` field non-empty | Net-new | Same as TC-TC-300 for the VM path; requires a VM template known to fail (or intentional misconfiguration) |
| TC-TC-315 | Delete of FAILED cluster returns 204 | `DELETE /clusters/<id>` on a FAILED cluster returns 204 | Net-new | Idempotent delete contract (REQ-DELETE-020) must hold even when the resource is in FAILED state; the backend must handle deprovisioning a partially-provisioned cluster |

### Group 5 — Concurrency and State Isolation

| TC ID | Test Name | Validates | Tier B ref | Description |
|-------|-----------|-----------|-----------|-------------|
| TC-TC-400 | Two simultaneous cluster creates complete independently | Both clusters eventually reach ACTIVE | Net-new | Submit two cluster creates in parallel (different `?id=` values); assert both eventually reach ACTIVE or FAILED independently, with no cross-contamination of IDs or kubeconfigs |
| TC-TC-405 | Idempotency preserved under real backend | Retry of identical `?id=` + identical body returns original resource while provisioning is in-flight | Extends Tier B idempotency | TC-TB idempotency is tested against a live backend but with mock-speed completions; TC-TC-405 tests the same contract while the cluster is still PROGRESSING |

---

## Timing Expectations

| Operation | Expected Duration | Test Timeout | Notes |
|-----------|------------------|-------------|-------|
| SP health check | < 5 s | 30 s | Should be instant once SP is running |
| Cluster create → PROGRESSING | 1–5 min | 5 min | AAP job start latency; fail early if not seen within 5 min |
| PROGRESSING → ACTIVE | 10–30 min | `OSAC_TIER_C_CLUSTER_PROVISION_TIMEOUT` (default 30 min) | Dominated by bare metal boot time + OCP install |
| Total cluster lifecycle test | 35–60 min | 60 min | Includes delete |
| VM create → RUNNING | 10–20 min | `OSAC_TIER_C_VM_PROVISION_TIMEOUT` (default 20 min) | VM compute is faster than full OCP cluster |
| Cluster delete → 404 | 5–15 min | 20 min | Real cluster deprovisioning |
| VM delete → 404 | 2–5 min | 10 min | VM teardown is faster |

All poll-based assertions should use `Eventually` with a polling interval of **30 seconds**
(not shorter — the backend does not benefit from faster polling and it increases noise).

---

## Running Tier C Tests

**Note:** The `make test-osac-sp` target runs the **existing** Tier A/B OSAC SP E2E tests
(input validation, API lifecycle, NATS events). The Tier C provisioning-lifecycle tests
described in this plan (TC-TC-100 through TC-TC-405) are **not yet implemented** in Go
test code. Today, `make test-osac-sp` can validate connectivity and API contract against
a Tier C backend, but will not automatically wait for ACTIVE/RUNNING or exercise the
real provisioning path. Implementing the Tier C Ginkgo test cases is tracked as future work.

```bash
# Prerequisites:
oc_login_auto          # log in to the OCP cluster
# If using a real (non-Tier-B) backend, verify bare metal pool has Available hosts

# --- Option A: Tier B backend (default; same as CLAUDE.md runbook) ---
make deploy-osac-backend             # one-time; writes deploy/osac-backend.env
./scripts/deploy-dcm.sh --environment-agent --osac-service-provider

# Run existing OSAC SP tests (API contract, CRUD lifecycle, NATS events):
OSAC_E2E_CLUSTER_TEMPLATE_ID=default-hcp \
  make test-osac-sp

# --- Option B: Real Tier C backend (when infrastructure is available) ---
# source deploy/osac-backend.env  — if using Tier B osac-backend.env, OR:
# export SP_OSAC_FULFILLMENT_ADDRESS=<real-grpc-host:443>
# export SP_OSAC_OIDC_ISSUER_URL=https://<keycloak>/realms/osac
# export SP_OSAC_OIDC_CLIENT_ID=<client-id>
# export SP_OSAC_OIDC_CLIENT_SECRET=<from-vault>     # never commit
./scripts/deploy-dcm.sh --environment-agent --osac-service-provider

OSAC_E2E_CLUSTER_TEMPLATE_ID=<real-template-id> \
OSAC_E2E_VM_TEMPLATE_ID=<real-vm-template-id> \
  make test-osac-sp                  # existing API tests run against real backend

# JUnit output:
OSAC_E2E_CLUSTER_TEMPLATE_ID=<id> \
  make test-osac-sp JUNIT_REPORT=tier-c-results.xml
```

When the Tier C Ginkgo tests are implemented, a label filter will separate the
long-running provisioning tests from the fast API-contract tests:

```bash
# (Future) Only Tier C provisioning lifecycle tests:
DCM_GINKGO_EXTRA_FLAGS="--label-filter='tier-c'" make test-osac-sp
```

---

## Future CI Integration

To run Tier C in CI the following would be required:

| Requirement | Detail |
|-------------|--------|
| Dedicated CI environment | A MOC project/namespace reserved for Tier C CI; must not be shared with developer runs |
| Bare metal pool reservation | A mechanism to claim/release BareMetalHosts so parallel CI runs don't contend; TBD whether this is handled by the fulfillment-service itself or a CI-layer reservation system |
| Credential injection | Real MOC/OSAC credentials injected via CI secrets (e.g. Vault + Jenkins credential binding, or OpenShift Secrets synced from Vault); never in git |
| Timeout budgets | CI pipeline must allow ≥ 90 min for a full Tier C cluster lifecycle run (create → ACTIVE → delete); standard PR pipelines have 30-min budgets and are not suitable |
| Post-run cleanup | CI must delete all test clusters/VMs even on failure; a cleanup hook (or a nightly sweep job) is needed to avoid exhausting the bare metal pool |
| Nightly schedule | Recommended schedule: once per night, off-hours, with Slack notification on failure; not on every commit |
| Result tracking | JUnit XML via `make test-osac-sp JUNIT_REPORT=<file>` is already supported; the CI pipeline should archive and publish the XML |
| MOC quota | Cluster provisioning consumes real hardware resources; coordinate with MOC ops to ensure quota for ≥ 2 concurrent clusters (for TC-TC-400 concurrency test) + 1 VM |
| Tracking ticket | File under [FLPATH-4758](https://redhat.atlassian.net/browse/FLPATH-4758) when MOC CI env exists — "Add Tier C OSAC SP CI pipeline" (no ticket yet) |

---

## Traceability

| Requirement ID | Description | Covered by |
|---------------|-------------|-----------|
| REQ-GET-020 | Kubeconfig absent when status is not ACTIVE | TC-TC-100 (pre-ACTIVE assertion), TC-TC-125 |
| REQ-GET-030 | Kubeconfig present and non-empty when ACTIVE | TC-TC-130 (blocked: [FLPATH-4924](https://redhat.atlassian.net/browse/FLPATH-4924)) |
| REQ-VMGET-030 | IP addresses present in GET /vms/{id} when RUNNING | TC-TC-210 |
| REQ-VMLIST-030 | IP addresses present in GET /vms (list) when RUNNING | TC-TC-215 |
| REQ-DELETE-020 | DELETE idempotent; 204 even when resource not found | TC-TC-150, TC-TC-225, TC-TC-315 |
| REQ-PUBLISH-030 | CloudEvent envelope fields (specversion, type, source, id, datacontenttype) | TC-TC-140, TC-TC-220 (extended from Tier B) |
| REQ-DUAL-010 | Both `/clusters/health` and `/vms/health` respond 200 | TC-TC-010, TC-TC-015 |
| DD-010 | Health always returns HTTP 200; detail absent when healthy, present when degraded | TC-TC-010, TC-TC-300 (detail on FAILED) |
| DD-080 / REQ-DELETE-020 | OSAC SP tolerates NotFound from backend on DELETE | TC-TC-155 (implicitly, after DELETED state) |
| TBD | AAP job template invocation via fulfillment-service | TC-TC-040, TC-TC-100, TC-TC-200 |
| TBD | BMFO BareMetalInstance lifecycle | TC-TC-110, TC-TC-115 |
| TBD | Real network/IP allocation for VMs | TC-TC-210, TC-TC-215 |
