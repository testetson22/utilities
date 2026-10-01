# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Repo Is

DCM Utilities — a shared repository for common scripts and tooling used across the [dcm-project](https://github.com/dcm-project) ecosystem. Houses the E2E deploy script and the E2E test suite.

This repo contains **bash scripts and a Go-based E2E test suite**. The shell scripts have no build step; the Go tests in `tests/e2e/` are compiled on-demand by Ginkgo. It also contains E2E test plans and results under `test-plans/`.

## Cursor Integration

This repo includes `.cursor/` with rules, prompts, and agents for Cursor IDE. When using Cursor, context is loaded automatically from `.cursor/rules/` and task-specific prompts are available via `@<prompt-name>`. See `.cursor/prompts/README.md` for the full list.

## Important: Keep Docs Up to Date

When making changes in a PR, always check whether `CLAUDE.md`, `README.md`, and relevant `.cursor/` files need updating to reflect the change. This includes new flags, changed behavior, new scripts, or modified conventions. Update all affected files as part of the same PR.

## Linting

```bash
shellcheck scripts/*.sh scripts/kind/*.sh scripts/compose/*.sh scripts/kubevirt/*.sh tests/*.sh
```

CI runs ShellCheck on changed `*.sh` files via `.github/workflows/lint.yaml` (only on PRs/pushes to `main`, only on changed files). Always validate locally before pushing.

## Key Script: `scripts/deploy-dcm.sh`

Deploys the full DCM stack for E2E testing by cloning control-plane (`deploy/compose.yaml`), running `podman-compose up`, and polling health endpoints until all services respond 2xx.

**Flow:** clone control-plane → bootstrap `deploy/.env` → `podman-compose up -d` → verify containers running → poll `/api/v1alpha1/health` → collect container versions from Quay.io API → write `dcm-versions.json`.

**Compose credentials:** After clone, the script copies `deploy/.env.example` to `deploy/.env` when missing and upserts DB/auth keys (lab defaults unless overridden by shell env). Control-plane compose reads these via `env_file: .env`. Pass `--auth-enabled` or set `AUTH_DISABLED=false` to add the compose `auth` profile (Keycloak) and write auth credentials into `.env`.

**Modes:** The script has three mutually exclusive modes:
- **Deploy** (default): full clone + bring-up + health check. Pass `--cleanup-on-failure` to auto-teardown on error (default leaves partial state for debugging).
- `--running-versions`: query already-running containers, resolve git SHAs via Quay.io API, write `dcm-versions.json`
- `--tear-down`: stop containers, remove volumes, delete deploy directory

**Version pinning:** Pass `--version <TAG>` to pin all DCM service images to a specific version. Three modes:
- `--version main` — use `:main` images (the default)
- `--version v0.1.0-rc.1` — pin all images to an explicit tag
- `--version release` — auto-resolve the latest semver tag from Quay.io

When a non-main version is specified, `--control-plane-branch` is auto-derived to the corresponding release branch (e.g. `v0.1.0-rc.1` → `release/v0.1.0`) unless explicitly passed.

**Service providers:** Configured via `providers/*.conf` files (see "Provider Registry" below). Enable with `--<label>-service-provider` or `--all-service-providers`.

**ACM/MCE deployment:** Pass `--deploy-acm` or `--deploy-mce` to install Red Hat ACM or MCE on the OCP cluster before starting the DCM stack. This clones the [acm-cluster-service-provider](https://github.com/dcm-project/acm-cluster-service-provider) repo and runs its `hack/deploy-acm-mce.sh` script. Can take 10–20 minutes. Requires `oc` and `jq`. These are opt-in flags, not enabled by default.

**Cluster authentication:** When any provider is enabled, the script resolves cluster access in priority order: explicit `--kubeconfig`, existing `oc`/`kubectl` session, or `oc login` via `--cluster-api` + `--cluster-password`.

**Control-plane authentication:** Pass `--auth-enabled` (or set `AUTH_DISABLED=false`) to start Keycloak and enable JWT validation. Use the same flag on `--tear-down` when tearing down an auth-enabled stack. The E2E suite currently supports unauthenticated test runs only.

**GitOps reconciliation:** Pass `--gitops` to add the separate published `dcm-gitops` reconciler container. It uses the same PostgreSQL database as control-plane and persists cloned repositories in the Compose `gitops_data` volume. Set `DCM_GITOPS_VERSION` to pin only that image, or use `--version` to pin all DCM images.

Run `./scripts/deploy-dcm.sh --help` for all flags and environment variable overrides.

## Local dev scripts

| Path | Purpose |
|------|---------|
| `scripts/kind/` | Kind + compose networking (kubeconfig, connect/disconnect) |
| `scripts/compose/` | Compose network teardown (not Kind-specific) |
| `scripts/kubevirt/` | KubeVirt install on any cluster (`kubectl` context) |

See each directory's `README.md` for env vars. Consumer repos set `UTILITIES_DIR ?= ../utilities`.

**Operational behavior (keep docs in sync when changing these scripts):**

- `install-kubevirt.sh` — Best-effort skip when `kv` CRs are visible; reminds the operator to
  verify KubeVirt/CNV is not already installed before running.
- `kind-disconnect.sh` — Uses `kind_try_resolve_from_context` from `kind-env.sh`; exits 0 when
  the current context is not Kind.
- `network-teardown.sh` — Explicit `CONTAINER_ENGINE` is never overridden by auto-detect; the
  `remove` step may run after compose has deleted networks on that runtime. Auto-detect only when
  `CONTAINER_ENGINE` is unset.

### Provider Registry

Service providers are defined declaratively in `providers/*.conf` files. Each conf file specifies:

| Key | Purpose |
|-----|---------|
| `PROVIDER_LABEL` | Short name for display and flag generation |
| `PROVIDER_FLAG` | CLI flag name (e.g. `kubevirt-service-provider`) |
| `COMPOSE_PROFILE` | Compose profile name from control-plane deploy compose (if applicable) |
| `COMPOSE_OVERRIDE` | Compose override file relative to repo root (if applicable) |
| `CLI_REQUIREMENT` | CLI tool needed: `oc`, `oc-or-kubectl`, or empty |
| `NAMESPACE_FLAG` / `NAMESPACE_ENV` / `NAMESPACE_DEFAULT` | Namespace configuration |
| `KUBECONFIG_EXPORT` / `NAMESPACE_EXPORT` | Env var names for compose substitution |
| `VALIDATE_HOOK` | Function name for provider-specific validation |

**To add a new provider:** drop a `.conf` file in `providers/` and (if needed) add a validation hook function in `deploy-dcm.sh`. No other changes to the deploy script are required — flags, usage, arg parsing, and env exports are all generated from the registry.

Current providers: `kubevirt`, `k8s-container`, `k8s-storage`, `acm-cluster`, `three-tier-app-demo`, `three-tier-app-demo-2`, `three-tier-app-demo-3`.

Host ports published for direct SP access (compose overrides): KubeVirt **8081**, k8s-container **8082**, ACM cluster **8083**, three-tier **8084**–**8086**, k8s-container-2/3 **8087**–**8088**, k8s-storage **8089**.

### Script Structure

The script is organized into sections separated by comment banners. Key functions:

| Function | Purpose |
|----------|---------|
| `load_providers` | Scans `providers/*.conf` and populates parallel arrays |
| `validate_deploy_dir` | Guards against `rm -rf` on system paths |
| `check_required_tools` | Verifies `git`, `podman`, `curl`, `jq`, etc. are installed |
| `tear_down` | Stops containers, removes volumes, deletes deploy dir |
| `resolve_kubeconfig` | Resolves cluster credentials (kubeconfig file, existing session, or `oc login`) |
| `validate_kubevirt_provider` | Checks CNV CRDs and creates namespace via `oc` |
| `ensure_provider_namespace` | Ensures a namespace exists via `oc` or `kubectl` |
| `validate_k8s_container_provider` | Validates k8s container SP prerequisites |
| `validate_k8s_storage_provider` | Validates k8s storage SP prerequisites |
| `validate_acm_cluster_provider` | Validates ACM cluster SP prerequisites |
| `ensure_deploy_env` | Bootstraps `deploy/.env` from `.env.example` and upserts credentials |
| `upsert_deploy_env_var` | Idempotently sets a key in `deploy/.env` |
| `resolve_provider_cli` | Resolves `oc`/`kubectl` per provider's `CLI_REQUIREMENT` |
| `collect_provider_compose` | Collects compose profiles/overrides for an enabled provider |
| `verify_health` | Confirms all compose services are running, then polls health endpoints with timeout |
| `resolve_git_sha` | Queries Quay.io tag API to map image digest → git commit SHA |
| `get_running_versions` | Iterates running containers, calls `resolve_git_sha`, writes JSON |

Argument parsing happens inline (not in a function) via a `while/case` loop. Provider flags are matched dynamically via `match_provider_flag` against the loaded registry.

## Shell Conventions

- Scripts use `set -euo pipefail` and `bash` (not POSIX sh).
- Constants are `readonly` at the top of the file.
- Logging helpers: `log()` for section headers (`==>`), `info()` for indented details, `err()` for stderr.
- Argument parsing uses a `while/case` loop with `require_arg` validation; flags take precedence over environment variables of the same name.
- Compose profiles are passed via array expansion: `${COMPOSE_PROFILES[@]+"${COMPOSE_PROFILES[@]}"}` (safe for empty arrays under `set -u`).

## `test-plans/`

E2E test plans and results for DCM service providers. Each file is named by Jira ticket (e.g. `FLPATH-3014-container-sp-api.md`). Test results are in `e2e-test-results-<date>.md`.

Test plans include:
- Scope and tier breakdowns (what's testable at each infrastructure level)
- Cross-references to upstream repo test plans (`.ai/test-plans/`) to avoid duplicating unit/integration coverage
- Code-verified behavior notes from actual PR implementations

## E2E Test Suite

The `tests/` directory contains the Ginkgo/Gomega E2E test framework.

### Structure

```
tests/
  run-e2e.sh                         # Test harness: deploy → resolve CLI → test → teardown
  compose-sp-test.yaml               # Compose override: publishes container SP port (auto-injected by provider registry)
  compose-acm-cluster-sp.yaml        # Compose override: adds ACM cluster SP service (auto-injected by provider registry)
  e2e/
    go.mod                            # Standalone Go module
    internal/resolve/                 # Plain Go package (no e2e build tag): pure STI-resolution
                                       # logic with real go test unit coverage — see Conventions below
    suite_test.go                     # Ginkgo bootstrap
    api_helpers_test.go               # HTTP helpers, env config, BeforeSuite connectivity check
    cli_helpers_test.go               # CLI binary execution helper (runDCM)
    sp_helpers_test.go                # Container SP direct-API + NATS + kubectl/podman helpers
    sp_acm_cluster_helpers_test.go    # ACM Cluster SP HTTP helpers + init/require guards
    api_health_test.go                # Health endpoint smoke tests (Label: "smoke")
    api_providers_test.go             # Provider CRUD lifecycle tests (API)
    api_policies_test.go              # Policy CRUD lifecycle tests (API)
    sp_container_api_test.go          # Container SP CRUD tests (Label: "sp", "container")
    sp_container_status_test.go       # Container SP NATS status events (Label: "sp", "container", "nats")
    sp_acm_cluster_api_test.go        # ACM Cluster SP API tests (Label: "sp", "acm-cluster")
    core_platform_test.go             # Core platform provisioning happy path (Label: "core", "platform")
    cli_version_test.go               # CLI version command test (Label: "smoke", "cli")
    cli_providers_test.go             # CLI sp provider read tests (Label: "cli")
    cli_policy_test.go                # CLI policy CRUD tests (Label: "cli")
    rehydration_helpers_test.go        # Rehydration types, provider discovery, lifecycle helpers
    rehydration_happy_path_test.go     # Core rehydration flow (Label: "rehydration", "happy-path")
    rehydration_failover_test.go       # Failover + deferred delete (Label: "rehydration", "failover", "disruptive")
    rehydration_policy_test.go         # Sovereignty + intent (Label: "rehydration", "policy")
    rehydration_negative_test.go       # Error paths + concurrency (Label: "rehydration", "negative")
    rehydration_data_integrity_test.go # Integrity + regressions (Label: "rehydration", "integrity")
    rehydration_api_contract_test.go   # RFC 7807 response shapes (Label: "rehydration", "contract")
    rehydration_cli_test.go            # CLI rehydrate commands (Label: "rehydration", "cli")
    rehydration_persistence_test.go    # SPRM restart, ServiceType (Label: "rehydration", "disruptive")
```

### Running Tests

```bash
make test-e2e              # Run all E2E tests (stack must be running)
make test-smoke            # Run smoke tests only (health checks + CLI version)
make test-cli              # Run CLI tests only
make test-sp               # Run container SP tests (SP must be deployed)
make test-acm-sp           # Run ACM cluster SP tests (ACM SP must be deployed)
make test-osac-sp          # Run OSAC SP tests (--environment-agent --osac-service-provider required)
make test-core             # Run core platform tests (full control plane provisioning flow)
make test-rehydration      # Run all rehydration tests (multi-provider + podman required)
make test-rehydration-safe # Run non-disruptive rehydration tests only
make test-rehydration-cli  # Run rehydration CLI tests only
make test-e2e-full         # Full lifecycle: deploy → test → teardown
make download-cli          # Download latest DCM CLI from GitHub releases
make deploy-osac-backend   # Deploy OSAC fulfillment-service backend on OCP (one-time setup)
make teardown-osac-backend # Remove the OSAC backend from OCP
```

The test harness (`tests/run-e2e.sh`) supports `--skip-deploy`, `--skip-teardown`, `--skip-cli`, `--dcm-cli-path`, `--label-filter`, `--gateway-url`, `--junit-report`, and service provider flags (`--k8s-container-service-provider`, `--all-service-providers`, `--kubeconfig`, `--cluster-api`, `--cluster-password`, etc.).

All test targets support JUnit XML output: `make test-e2e JUNIT_REPORT=results.xml`

### Test Layers

| Layer | What it tests | Label |
|-------|--------------|-------|
| **Core platform tests** | Full provisioning flow through control plane | `core`, `platform` |
| **API tests** | HTTP CRUD operations against the control plane | (none) |
| **SP tests** | Container SP direct API + NATS status events | `sp`, `container` |
| **ACM SP tests** | ACM Cluster SP API (health, registration, validation, CRUD) | `sp`, `acm-cluster` |
| **Cluster tests** | Tests requiring `kubectl`/`oc` cluster access | `cluster` |
| **Disruptive tests** | Tests that stop/start infrastructure (e.g. NATS) | `disruptive` |
| **CLI tests** | DCM CLI binary against the live stack | `cli` |
| **Smoke tests** | Health checks + CLI version (quick validation) | `smoke` |
| **Rehydration tests** | Rehydration lifecycle, failover, policy, integrity | `rehydration` |
| **Rehydration subtypes** | happy-path, failover, policy, negative, integrity, contract | see file headers |
| **OSAC SP tests** | OSAC SP cluster/VM API + NATS status events | `sp`, `osac` |

### OSAC SP Backend (fulfillment-service on OCP)

OSAC SP tests that go beyond input validation (CRUD lifecycle, NATS events) require
a real `fulfillment-service` backend. No external OSAC/MOC credentials are needed —
the backend is a self-contained stack deployed on the edge94 OCP cluster using
test-only credentials vendored from `dcm-project/osac-service-provider`'s own Tier B
e2e infrastructure (`tests/osac-backend/`).

**Fully turn-key (requires `oc`, `helm`, `yq`, `openssl` — `deploy-dcm.sh` deploys the backend for you):**

```bash
oc_login_auto                       # log in to edge94
./scripts/deploy-dcm.sh --deploy-osac-backend --environment-agent --osac-service-provider
```

`--deploy-osac-backend` runs `scripts/deploy-osac-backend.sh` before compose bring-up
(idempotent — skips already-present cert-manager/namespace/etc. on reruns), then
`deploy-dcm.sh` auto-detects the resulting `deploy/osac-backend.env` and wires in
credentials plus the TLS CA overlay (`tests/compose-osac-sp-tls.yaml`) automatically —
no manual `source` or `--compose-file` needed. It validates the CA as a readable, non-empty
PEM X.509 certificate before Compose starts. On macOS, the combined command also starts or
reuses the launchd-managed Keycloak/gRPC port-forwards before provider validation, then
injects the Darwin routing overlay.

Use `--osac-aap-mode real` with the combined command to additionally deploy the disposable
AAP 2.7 Controller/Gateway integration, activate it from `OSAC_AAP_MANIFEST`, and configure
the OSAC operator to use the Gateway. The default `--osac-aap-mode mock` remains available
for fast contract tests; real mode does not remove the mock deployment.

**Or as two steps (useful when reusing one backend across many stack up/down cycles):**

```bash
oc_login_auto                       # log in to edge94
make deploy-osac-backend            # one-time: deploys Postgres + Keycloak + fulfillment-service
                                    # writes deploy/osac-backend.env + deploy/osac-ca.pem
./scripts/deploy-dcm.sh --environment-agent --osac-service-provider
```

**Running OSAC SP E2E tests:**

```bash
# Validation-only (no real backend needed — always runs):
make test-osac-sp

# Full CRUD + NATS (requires backend + template IDs):
OSAC_E2E_CLUSTER_TEMPLATE_ID=<id> \
OSAC_E2E_VM_TEMPLATE_ID=<id> \
make test-osac-sp
```

The backend namespace is `osac-test-backend` by default. Teardown — `deploy-dcm.sh --tear-down`
does **not** remove the backend on its own (it only tears down the compose stack); either
couple it explicitly or use the standalone target:

**Tier B++ dispatch boundary:** The opt-in `tier-b-dispatch` suite is an OCP-backed
orchestration-boundary tier between the self-contained Tier B API tests and real Tier C
infrastructure. It validates OpenShift authentication/resource discovery, exact SP ID to
ClusterOrder linkage, request translation, NATS correlation, and the observable operator
stop point. It does not prove Agent allocation, AAP execution, BMC/Ironic access, real
bare-metal provisioning, `ACTIVE` state, kubeconfig usability, VM networking, or real
infrastructure cleanup. The current real-AAP Phase 2 result is TBP-010/TBP-020/TBP-030
passing; the linked order reaches `NamespaceCreated=True` and `Progressing=True` with
`PreparingInfrastructure`, then stops because no Agent/BMI/HostedCluster exists. This is
not a Tier C substitute. TBP-040/050 remain disabled until suitable Agent resources or a
contract-faithful simulator makes those paths reachable.

```bash
./scripts/deploy-dcm.sh --deploy-osac-backend --tear-down   # tears down compose stack + OSAC backend
# or, if the backend was deployed separately:
make teardown-osac-backend
```

Credentials are static test-only values committed to git (`tests/osac-backend/realm.json`).
The `osac-admin` client secret is `tierb-osac-admin-secret`. Never use these for real deployments.
These follow the same convention as the upstream `dcm-project/osac-service-provider` repo, which
commits identical `tierb-*` credentials in `test/e2e/manifests-tierb/` (public repo). Our
`tests/osac-backend/` is a vendored copy of those manifests. If the credential convention
ever needs to change (e.g. secret-scanning policy), it must be coordinated with the upstream.

**Mac/Darwin — OCP backend requires port-forwarding:**

The OCP cluster's internal service network (`192.168.30.x`) is not directly routable from a Mac.
`make port-forward-osac` tunnels the required services via `oc port-forward`, registered as
persistent macOS launchd agents (`com.osac.pf.keycloak`, `com.osac.pf.grpc`). The combined
deploy command starts them automatically after deploying the backend:

```bash
oc_login_auto
./scripts/deploy-dcm.sh --deploy-osac-backend --environment-agent --osac-service-provider
make test-osac-sp
make stop-port-forward-osac    # when done
```

If the backend is already deployed separately, run `make port-forward-osac` before
`deploy-dcm.sh --environment-agent --osac-service-provider`.

Key design: Keycloak is accessed via its in-cluster hostname (`ffs-keycloak.osac-test-backend.svc.cluster.local:8443`) — this makes Keycloak issue tokens with the issuer URL that matches fulfillment-service's configured `auth.issuerUrl`. Port 8443 does not need sudo on macOS (> 1024).

Full runbook and troubleshooting: `.cursor/prompts/deploy-osac-backend.md` (`@deploy-osac-backend` in Cursor).

### CLI Binary Resolution

CLI tests require the `dcm` binary. Resolution order:
1. `DCM_CLI_PATH` env var or `--dcm-cli-path` flag
2. `dcm` in `$PATH`
3. Previously downloaded binary in `bin/dcm` (from `make download-cli`)
4. Auto-download from GitHub releases (`dcm-project/cli`, requires `gh`)

CLI tests are skipped (not failed) if no binary is available.

### Conventions

- All test files use `//go:build e2e` build tag, **except** `tests/e2e/internal/*` packages (e.g. `internal/resolve`), which hold pure, non-network logic with no `e2e` tag and real unit-test coverage. CI runs `go test ./internal/...` (unlike the e2e-tagged suite, which CI only vets/compiles — see `validate-tests.yaml`)
- API tests use raw `net/http` (no generated clients) for independence from service repos
- CLI tests use `os/exec` to run the actual binary (not in-process Cobra)
- `DCM_GATEWAY_URL` env var overrides the control plane API endpoint (default: `http://localhost:8080/api/v1alpha1`)
- `DCM_CONTAINER_SP_URL` env var overrides the container SP endpoint (default: `http://localhost:8082/api/v1alpha1`)
- `DCM_STORAGE_SP_URL` env var overrides the storage SP endpoint (default: `http://localhost:8089/api/v1alpha1`)
- `DCM_ACM_CLUSTER_SP_URL` env var overrides the ACM cluster SP endpoint (default: `http://localhost:8083/api/v1alpha1`)
- `DCM_NATS_URL` env var overrides the NATS server (default: `nats://localhost:4222`)
- `DCM_CLI_PATH` env var specifies the CLI binary path
- `DCM_CONTAINER_PROVIDER_NAME` env var overrides which container provider to target in core platform tests (default: first `service_type=container` provider found)
- Ginkgo labels (`smoke`, `cli`, `sp`, `container`, `acm-cluster`, `nats`, `cluster`, `disruptive`, `core`, `platform`, `rehydration`, `happy-path`, `failover`, `policy`, `negative`, `integrity`, `contract`) enable selective test runs via `--label-filter`
- SP tests skip gracefully if the container SP or ACM cluster SP isn't reachable (no hard failure)
- Cluster tests skip gracefully if `kubectl`/`oc` is unavailable or the cluster is unreachable
- Disruptive tests skip if `podman` is unavailable; exclude from normal runs with `--label-filter '!disruptive'`

## `dcm-versions.json`

Artifact produced by the deploy script (both deploy mode and `--running-versions`). Maps container image names to their digest and the git commit SHA that produced the image (resolved via Quay.io tag API). The file is gitignored at the repo root; the copy under `scripts/` is the authoritative output location.
