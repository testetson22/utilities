# Deploy the OSAC Backend

Deploy a self-contained OSAC fulfillment-service backend on OCP, then run the DCM stack and OSAC service provider against it — no external OSAC/MOC credentials required.

## Why This Exists

The OSAC SP needs a real fulfillment-service backend to test against, but acquiring real MOC/OSAC dev credentials for an ephemeral test environment is undesirable (external dependency, credentials to house/rotate). Instead, `scripts/deploy-osac-backend.sh` adapts the OSAC SP's own Tier B e2e infrastructure (Postgres + Keycloak + fulfillment-service, all test-only credentials vendored from `dcm-project/osac-service-provider`) to run on an OCP cluster instead of `kind`. Nothing here talks to real MOC/OSAC infrastructure.

## Prerequisites

1. **OCP cluster access**: `oc` logged in with cluster-admin (run `oc_login_auto` first)
2. **Required tools**: `helm` (3.8+, OCI registry support), `yq`, `curl`, `jq`
3. **DCM stack tools**: same as `@deploy-dcm` (`git`, `podman`, `podman-compose`)

## Commands

### Fully turn-key (recommended for a one-shot test run)
```bash
./scripts/deploy-dcm.sh --deploy-osac-backend --environment-agent --osac-service-provider
```
`--deploy-osac-backend` runs `scripts/deploy-osac-backend.sh` before compose bring-up (idempotent — skips already-present cert-manager/namespace/etc. on reruns), then `deploy-dcm.sh` auto-detects the resulting `deploy/osac-backend.env` and wires in credentials plus the TLS CA overlay automatically. No manual `source` or `--compose-file` needed.

### Or as two separate steps (useful when reusing one backend across many stack up/down cycles)

**1. One-time backend setup:**
```bash
./scripts/deploy-osac-backend.sh
```

Custom namespace or chart version:
```bash
./scripts/deploy-osac-backend.sh --namespace my-osac-backend --chart-version 0.0.107
```

**2. Deploy the DCM stack against the backend:**
```bash
./scripts/deploy-dcm.sh --environment-agent --osac-service-provider
```
Same auto-detection of `deploy/osac-backend.env` applies here — the backend just already exists from step 1 instead of being deployed inline.

### Run OSAC SP tests
```bash
# Validation-only (health/schema checks — no real backend CRUD calls, always runs)
make test-osac-sp

# Full CRUD + NATS status events (needs fulfillment-service template IDs)
OSAC_E2E_CLUSTER_TEMPLATE_ID=<id> OSAC_E2E_VM_TEMPLATE_ID=<id> make test-osac-sp
```

### Tear down

`deploy-dcm.sh --tear-down` only tears down the compose stack — it does **not** remove the OSAC backend unless you also pass `--deploy-osac-backend`:

```bash
# Compose stack + OSAC backend together (mirrors how it was deployed above)
./scripts/deploy-dcm.sh --deploy-osac-backend --tear-down

# Or separately, if the backend was deployed standalone
./scripts/deploy-dcm.sh --tear-down
./scripts/deploy-osac-backend.sh --tear-down
```

## What Happens (`deploy-osac-backend.sh`)

1. Verifies `oc` login and required tools
2. Enables HTTP/2 on the OCP ingress controller (required for gRPC)
3. Creates the backend namespace (default: `osac-test-backend`)
4. Installs cert-manager if not already present (OLM subscription, falling back to Helm)
5. Applies a self-signed CA chain (`osac-ca` `ClusterIssuer`)
6. Deploys Postgres, then Keycloak (imports the vendored test-only `osac` realm)
7. Installs the pinned `fulfillment-service` Helm chart, filtering out `TLSRoute` (Gateway API) objects the chart renders that don't exist on stock OCP
8. Creates OCP passthrough Routes for Keycloak and the gRPC server
9. Writes `deploy/osac-backend.env` (credentials + endpoints) and `deploy/osac-ca.pem` (CA cert)

## Output

- `deploy/osac-backend.env` — `SP_OSAC_*` credentials + `OSAC_CA_CERT_FILE`, auto-sourced by `deploy-dcm.sh`
- `deploy/osac-ca.pem` — CA cert, auto-mounted into the OSAC SP container via `tests/compose-osac-sp-tls.yaml`
- Backend reachable at:
  - Keycloak: `https://ffs-keycloak.<namespace>.<cluster-domain>`
  - gRPC: `ffs-grpc.<namespace>.<cluster-domain>:443`

Both files are gitignored (`/deploy/`) — they are local, cluster-specific artifacts, not committed.

## Credentials

Test-only values vendored from `dcm-project/osac-service-provider`'s Tier B e2e infrastructure (`tests/osac-backend/realm.json`), committed to git on purpose (matches that repo's own NFR-TB-020 approach). **Never use these for a real deployment.**

| Client | Secret |
|--------|--------|
| `osac-admin` | `tierb-osac-admin-secret` |
| `osac-controller` | `tierb-osac-controller-secret` |

## Mac/Darwin: Accessing the OCP Backend via Port-Forward

The OSAC backend runs on an OCP cluster (`ocp-edge94`) whose internal service network (`192.168.30.x`) is not directly routable from a Mac, even over VPN. `make port-forward-osac` tunnels the required services through the OCP API server using `oc port-forward`, registered as persistent macOS launchd agents so they survive terminal exits.

### How it works

| Local port | OCP service | Purpose |
|-----------|-------------|---------|
| **8443** | `svc/ffs-keycloak:8443` | Keycloak OIDC — uses in-cluster hostname so token issuer matches fulfillment-service's trusted issuer |
| **19443** | `svc/fulfillment-grpc-server:8000` | gRPC backend |

The compose overlay `tests/compose-osac-sp-darwin-pf.yaml` is auto-injected by `deploy-dcm.sh` when it detects the port-forwards are active (via `lsof`). It sets:
- `SP_OSAC_OIDC_ISSUER_URL=https://ffs-keycloak.osac-test-backend.svc.cluster.local:8443/realms/osac` (in-cluster hostname — cert valid, issuer matches)
- `SP_OSAC_FULFILLMENT_ADDRESS=fulfillment-grpc-server.osac-test-backend:19443` (internal service hostname — cert valid)
- `SSL_CERT_FILE=/etc/osac/ca.pem` (Go TLS CA for all HTTPS including OIDC)
- `GRPC_ENFORCE_ALPN_ENABLED=false` (disables strict h2 ALPN for port-forward paths)
- `extra_hosts` resolving both hostnames to `192.168.127.254` (host.containers.internal)

### Workflow

```bash
# 1. Log into OCP (required before port-forwards work)
oc_login_auto

# 2. Start port-forward launchd agents (survives terminal close)
make port-forward-osac

# 3. Deploy stack — auto-detects port-forwards and injects darwin-pf overlay
./scripts/deploy-dcm.sh --environment-agent --osac-service-provider

# 4. Run tests
make test-osac-sp

# 5. Stop port-forwards when done
make stop-port-forward-osac
```

### Key networking details
- Port 8443 > 1024 — no sudo required on macOS
- launchd manages the restart loops: each loop script is written to `/tmp/osac-pf-*.sh` with the absolute `oc` path and `KUBECONFIG`, then registered as `com.osac.pf.keycloak` / `com.osac.pf.grpc`
- Logs: `/tmp/pf-ffs-keycloak.log`, `/tmp/pf-fulfillment-grpc-server.log`
- Token issuer alignment: Keycloak derives `iss` from the HTTP `Host` header; connecting via the in-cluster hostname on port 8443 produces `iss: https://ffs-keycloak.osac-test-backend.svc.cluster.local:8443/realms/osac`, which exactly matches the fulfillment-service's configured `auth.issuerUrl`

## Known Limitations

- `--deploy-osac-backend` is opt-in and heavy on first run (cert-manager install + Postgres/Keycloak/fulfillment-service rollout — a few minutes), matching the existing `--deploy-acm`/`--deploy-mce`/`--deploy-cnv` pattern. Reruns are fast (idempotent — skips already-present resources)
- `deploy-dcm.sh --tear-down` never removes the backend unless `--deploy-osac-backend` is also passed on that same invocation
- Phase 2 components (`osac-operator`, BMFO, `osac-aap-mock` — full provisioning lifecycle / routed dispatch) are **not** deployed here; that coverage is intentionally left to `dcm-project/osac-service-provider`'s own Tier B CI, not this repo
- `OSAC_E2E_CLUSTER_TEMPLATE_ID` / `OSAC_E2E_VM_TEMPLATE_ID` are not auto-discovered — obtain them from the fulfillment-service admin API/CLI against this backend and export them before running full CRUD tests
- Full CRUD dispatch requires the `osac-service-provider:main` image to have real Create/Get support implemented — check [FLPATH-4459](https://redhat.atlassian.net/browse/FLPATH-4459) if CRUD tests fail unexpectedly against `:main`
- The fulfillment-service backend does not enforce `max_page_size > 100` per AEP-132; the corresponding E2E test skips gracefully when the backend is reachable and returns 200

## Troubleshooting

### "cert-manager operator did not become ready"
The OLM `community-operators` catalog may be unavailable on this cluster. Skip the auto-install and do it via Helm yourself:
```bash
helm upgrade cert-manager oci://quay.io/jetstack/charts/cert-manager \
    --install --version v1.20.0 --namespace cert-manager --create-namespace \
    --set crds.enabled=true --wait
./scripts/deploy-osac-backend.sh --skip-cert-manager
```

### "'yq' is required to filter the fulfillment-service chart manifests"
```bash
brew install yq
# or
go install github.com/mikefarah/yq/v4@latest
```

### `Certificate` never becomes `Ready`
```bash
oc describe certificate ffs-keycloak -n osac-test-backend
oc get clusterissuer osac-ca -o yaml
```

### gRPC Route unreachable / TLS handshake failure
```bash
# Confirm HTTP/2 is enabled on the ingress controller
oc get ingresses.config.openshift.io cluster -o jsonpath='{.metadata.annotations}'

# Confirm Route and Service ports line up
oc get route ffs-grpc -n osac-test-backend -o yaml
oc get service fulfillment-grpc-server -n osac-test-backend -o yaml
```

### fulfillment-service pods not starting
```bash
oc get pods -n osac-test-backend
oc logs -n osac-test-backend -l app.kubernetes.io/instance=ffs-fulfillment-service --tail=100
```

### `deploy-dcm.sh` says OSAC credentials are missing
The backend env file wasn't found or didn't export cleanly. Confirm it exists and re-source manually to debug:
```bash
cat deploy/osac-backend.env
source deploy/osac-backend.env && env | grep SP_OSAC
```
If it's missing entirely, re-run `./scripts/deploy-osac-backend.sh`.

## Nuclear Option: Full Reset

```bash
./scripts/deploy-dcm.sh --deploy-osac-backend --tear-down
./scripts/deploy-dcm.sh --deploy-osac-backend --environment-agent --osac-service-provider
```
