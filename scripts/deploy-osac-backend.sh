#!/usr/bin/env bash
# deploy-osac-backend.sh — Deploy or tear down the OSAC fulfillment-service
# backend on an OCP cluster for E2E testing of the OSAC service provider.
#
# Deploys a self-contained test stack (Postgres + Keycloak + fulfillment-service)
# into a dedicated OCP namespace using test-only credentials vendored from
# dcm-project/osac-service-provider's own Tier B e2e infrastructure.
# No external/real OSAC credentials are required.
#
# After a successful deploy the script writes:
#   deploy/osac-backend.env   — credentials + endpoints (auto-sourced by deploy-dcm.sh)
#   deploy/osac-ca.pem        — CA cert (auto-mounted by deploy-dcm.sh via the TLS overlay)
#
# deploy-dcm.sh auto-detects deploy/osac-backend.env, so once this script has
# run, deploying the DCM stack against the backend is turn-key:
#   ./scripts/deploy-dcm.sh --environment-agent --osac-service-provider
# No manual `source` or `--compose-file` is required.
#
# Usage:
#   ./scripts/deploy-osac-backend.sh [OPTIONS]
#
# Options:
#   --tear-down              Stop and remove the OSAC backend namespace
#   --namespace NS           OCP namespace (default: osac-test-backend)
#   --chart-version VER      fulfillment-service chart version to pin
#                            (default: 0.0.107)
#   --skip-cert-manager      Skip cert-manager install check/install
#   --help                   Show this help and exit
#
# Prerequisites:
#   oc       — logged in to the target OCP cluster (run oc_login_auto first)
#   helm     — v3.8+ for OCI registry support
#   yq, curl, jq — standard utilities
#
# See .cursor/prompts/deploy-osac-backend.md for the full runbook.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly REPO_ROOT
readonly MANIFESTS_DIR="${REPO_ROOT}/tests/osac-backend"
readonly DEPLOY_DIR="${REPO_ROOT}/deploy"

# --- Defaults ----------------------------------------------------------------

OSAC_BACKEND_NAMESPACE="${OSAC_BACKEND_NAMESPACE:-osac-test-backend}"
FULFILLMENT_SERVICE_CHART_VERSION="${FULFILLMENT_SERVICE_CHART_VERSION:-0.0.107}"
OSAC_OPERATOR_CHART_VERSION="${OSAC_OPERATOR_CHART_VERSION:-0.0.18}"
BMFO_CHART_VERSION="${BMFO_CHART_VERSION:-0.0.12}"
# Upstream commit SHA for downloading unmodified Phase 2 manifests, CRDs, and
# Helm values at deploy time. Only aap-mock.yaml is committed locally (we changed
# the image reference); everything else is fetched from this pin to avoid
# duplicating floaty upstream files in this repo.
OSAC_SP_UPSTREAM_REF="${OSAC_SP_UPSTREAM_REF:-eb1473848b9f8b130b3f315f00f800c102062d3e}"
readonly OSAC_SP_UPSTREAM_RAW="https://raw.githubusercontent.com/dcm-project/osac-service-provider"
TEAR_DOWN=false
SKIP_CERT_MANAGER=false
SKIP_PHASE2=false

# --- Logging -----------------------------------------------------------------

log()  { echo "==> $*"; }
info() { echo "    $*"; }
err()  { echo "ERROR: $*" >&2; }

# --- Usage -------------------------------------------------------------------

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Deploy or tear down the OSAC fulfillment-service backend on OCP for E2E testing.

Options:
  --tear-down                 Remove the backend namespace and all its resources
  --namespace NS              OCP namespace to deploy into (default: ${OSAC_BACKEND_NAMESPACE})
  --chart-version VER         fulfillment-service Helm chart version (default: ${FULFILLMENT_SERVICE_CHART_VERSION})
  --skip-cert-manager         Skip cert-manager install; assume it is already present
  --skip-phase2               Skip Phase 2 components (osac-operator, BMFO, aap-mock, fixtures)
                              Phase 2 allows clusters to advance beyond PROGRESSING; chart 0.0.18+
                              (proto-compatible with fulfillment-service 0.0.107, OSAC-2928).
                              Full ACTIVE delivery (kubeconfig) requires Tier C (real Agents).
  --help                      Show this help message

Environment variables:
  OSAC_BACKEND_NAMESPACE             Override the default namespace
  FULFILLMENT_SERVICE_CHART_VERSION  Override the fulfillment-service chart version pin
  OSAC_OPERATOR_CHART_VERSION        Override the osac-operator chart version (default: ${OSAC_OPERATOR_CHART_VERSION})
  BMFO_CHART_VERSION                 Override the BMFO chart version (default: ${BMFO_CHART_VERSION})

After a successful deploy, deploy-dcm.sh auto-detects deploy/osac-backend.env:
  ./scripts/deploy-dcm.sh --environment-agent --osac-service-provider

Credentials: test-only values committed to git — do not use for any real deployment.
  OIDC client: osac-admin / tierb-osac-admin-secret
  Postgres:    postgres / tierb-postgres-password
EOF
}

# --- Argument parsing --------------------------------------------------------

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tear-down)           TEAR_DOWN=true; shift ;;
        --namespace)           OSAC_BACKEND_NAMESPACE="$2"; shift 2 ;;
        --chart-version)       FULFILLMENT_SERVICE_CHART_VERSION="$2"; shift 2 ;;
        --skip-cert-manager)   SKIP_CERT_MANAGER=true; shift ;;
        --skip-phase2)         SKIP_PHASE2=true; shift ;;
        --help)                usage; exit 0 ;;
        *)                     err "Unknown option: $1"; usage; exit 1 ;;
    esac
done

readonly NS="${OSAC_BACKEND_NAMESPACE}"

# --- Tool checks -------------------------------------------------------------

check_tools() {
    local missing=()
    # python3 is required for Phase 2 ownership checks (label parsing) on both
    # deploy and tear-down paths, and for OIDC token parsing during Phase 2.
    for tool in oc helm curl jq python3; do
        command -v "${tool}" &>/dev/null || missing+=("${tool}")
    done
    # Phase 2 fixture registration needs these; skip when tearing down or
    # when --skip-phase2 is set (Phase 1-only deploy).
    if [[ "${TEAR_DOWN}" != true && "${SKIP_PHASE2}" != true ]]; then
        for tool in grpcurl base64; do
            command -v "${tool}" &>/dev/null || missing+=("${tool}")
        done
    fi
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing required tools: ${missing[*]}"
        exit 1
    fi
}

# oc_apply_filtered runs `oc apply` with the given args, filters "unchanged"
# noise from stdout, and returns oc's real exit status (does not mask failures).
oc_apply_filtered() {
    local out rc=0
    out="$(oc apply "$@" 2>&1)" || rc=$?
    # Filter noise; never let grep's exit status (no match) override oc's.
    echo "${out}" | grep -v "unchanged" || true
    return "${rc}"
}

# --- OCP login guard ---------------------------------------------------------

check_oc_login() {
    if ! oc whoami &>/dev/null; then
        err "Not logged in to OCP — run oc_login_auto first"
        exit 1
    fi
    info "Logged in as: $(oc whoami)"
}

# --- Cluster health guard ----------------------------------------------------

# check_cluster_health verifies that all nodes are Ready and no cluster operator
# is degraded. Exits with an error if the cluster appears unhealthy.
check_cluster_health() {
    log "Checking cluster health before proceeding"

    local not_ready
    not_ready=$(oc get nodes --no-headers 2>/dev/null \
        | awk '$2 != "Ready" {print $1, $2}')
    if [[ -n "${not_ready}" ]]; then
        err "One or more nodes are not Ready — resolve before modifying the cluster:"
        echo "${not_ready}" >&2
        exit 1
    fi
    info "All nodes are Ready"

    local degraded
    degraded=$(oc get clusteroperators --no-headers 2>/dev/null \
        | awk '$5 == "True" {print $1}')
    if [[ -n "${degraded}" ]]; then
        err "One or more cluster operators are Degraded — resolve before modifying the cluster:"
        echo "${degraded}" >&2
        exit 1
    fi
    info "No cluster operators are Degraded"
}

# --- Tear down ---------------------------------------------------------------

# Label applied to Phase 2 resources we create (Agent CRD stub, hardware-inventory
# namespace, BareMetalHost fixtures). Teardown deletes only resources bearing this
# label — never unlabeled pre-existing Assisted Installer / shared resources.
# Survives re-deploys and a missing ownership file (labels live on the cluster).
readonly PHASE2_PART_OF_LABEL="osac-sp-e2e-tierb"
readonly PHASE2_PART_OF_KEY="app.kubernetes.io/part-of"

# BareMetalHost fixtures applied into namespace `default` by deploy_phase2.
# Must be deleted explicitly — namespace teardown of ${NS} does not touch them.
readonly BMH_FIXTURE_NAMES=(
    tierb-bmh-always
    tierb-bmh-cleanup
    tierb-bmh-contended
    tierb-bmh-ineligible
    tierb-bmh-unset
)

# phase2_owned RESOURCE_TYPE NAME [NAMESPACE]
# Returns 0 if the resource carries our Phase 2 part-of label.
phase2_owned() {
    local kind="$1" name="$2" ns="${3:-}"
    local label
    if [[ -n "${ns}" ]]; then
        label="$(oc get "${kind}" "${name}" -n "${ns}" -o json 2>/dev/null \
            | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('metadata',{}).get('labels',{}).get('${PHASE2_PART_OF_KEY}',''))" \
            2>/dev/null || true)"
    else
        label="$(oc get "${kind}" "${name}" -o json 2>/dev/null \
            | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('metadata',{}).get('labels',{}).get('${PHASE2_PART_OF_KEY}',''))" \
            2>/dev/null || true)"
    fi
    [[ "${label}" == "${PHASE2_PART_OF_LABEL}" ]]
}

tear_down() {
    log "Tearing down OSAC backend (namespace: ${NS})"

    # The osac-operator console-proxy creates a cluster-scoped APIService for
    # console.osac.openshift.io/v1alpha1.  When the namespace is deleted, the
    # APIService backend disappears but the APIService object remains, causing
    # the namespace to hang in Terminating (NamespaceDeletionDiscoveryFailure).
    # Delete it first so the namespace termination completes cleanly.
    if oc get apiservice v1alpha1.console.osac.openshift.io &>/dev/null; then
        log "Removing console.osac.openshift.io APIService (prevents namespace hang)"
        oc delete apiservice v1alpha1.console.osac.openshift.io --ignore-not-found
    fi

    # Strip finalizers from any remaining ClusterOrders before deleting the
    # namespace so the operator (already stopping) doesn't block deletion.
    local co
    for co in $(oc get clusterorder -n "${NS}" -o name 2>/dev/null); do
        oc patch "${co}" -n "${NS}" \
            --type=json \
            -p='[{"op":"remove","path":"/metadata/finalizers"}]' \
            2>/dev/null || true
    done

    if oc get namespace "${NS}" &>/dev/null; then
        oc delete namespace "${NS}" --wait=true
        info "Namespace ${NS} deleted"
    else
        info "Namespace ${NS} not found — nothing to delete"
    fi

    # Remove cluster-scoped resources owned by this backend (Phase 1).
    log "Removing cluster-scoped resources"
    oc delete clusterissuer osac-ca --ignore-not-found
    info "ClusterIssuer osac-ca removed"

    # Remove BareMetalHost fixtures from `default` only if they carry our label.
    log "Removing owned BareMetalHost fixtures from namespace 'default'"
    local bmh removed_bmh=0
    for bmh in "${BMH_FIXTURE_NAMES[@]}"; do
        if phase2_owned baremetalhost "${bmh}" default; then
            oc delete baremetalhost "${bmh}" -n default --ignore-not-found
            removed_bmh=$((removed_bmh + 1))
        fi
    done
    info "Removed ${removed_bmh} owned BareMetalHost fixture(s)"

    # Remove Phase 2 cluster-scoped resources only when labeled as ours.
    if phase2_owned crd agents.agent-install.openshift.io; then
        oc delete crd agents.agent-install.openshift.io --ignore-not-found
        info "Agent CRD stub removed (labeled ${PHASE2_PART_OF_KEY}=${PHASE2_PART_OF_LABEL})"
    else
        info "Agent CRD absent or not owned by this deploy — leaving in place"
    fi

    if phase2_owned namespace hardware-inventory; then
        oc delete namespace hardware-inventory --wait=false --ignore-not-found
        info "hardware-inventory namespace deletion requested (owned)"
    else
        info "hardware-inventory absent or not owned by this deploy — leaving in place"
    fi
    # Leave cert-manager itself in place — other workloads may depend on it.

    # Restore the OCP ingress HTTP/2 annotation that deploy() set.
    # Removing the annotation returns the ingress controller to its default
    # (HTTP/2 disabled), which is the safest post-teardown state.
    log "Restoring OCP ingress HTTP/2 setting"
    if oc get ingresses.config.openshift.io cluster \
            -o jsonpath='{.metadata.annotations.ingress\.operator\.openshift\.io/default-enable-http2}' \
            2>/dev/null | grep -q "true"; then
        oc annotate ingresses.config.openshift.io cluster \
            ingress.operator.openshift.io/default-enable-http2- \
            --overwrite 2>/dev/null || true
        info "HTTP/2 annotation removed (ingress controller returns to default)"
    else
        info "HTTP/2 annotation was not set — no change needed"
    fi

    log "Tear-down complete"
    rm -f "${DEPLOY_DIR}/osac-backend.env" "${DEPLOY_DIR}/osac-ca.pem" \
        "${DEPLOY_DIR}/osac-phase2-owned.env"
    info "Cleaned up deploy/osac-backend.env and deploy/osac-ca.pem"
}

# --- cert-manager readiness --------------------------------------------------

# Wait for all three cert-manager deployments to be Available and the webhook
# to be reachable. Must be called before applying any cert-manager CRDs.
wait_cert_manager_ready() {
    info "Waiting for all cert-manager deployments to be fully available..."
    local d
    local retries

    # The three standard cert-manager deployments (OLM and Helm both create them)
    local deploys=(cert-manager cert-manager-webhook cert-manager-cainjector)
    for d in "${deploys[@]}"; do
        retries=0
        until oc get deployment "${d}" -n cert-manager &>/dev/null; do
            sleep 5
            retries=$((retries + 1))
            [[ ${retries} -gt 36 ]] && { err "deployment/${d} did not appear in cert-manager namespace in 3m"; exit 1; }
        done
        info "  deployment/${d} exists — waiting for Available..."
        oc rollout status "deployment/${d}" -n cert-manager --timeout=5m
    done

    # Give the webhook a moment to finish syncing with the API server's admission
    # registration — pod Ready does not guarantee the apiserver has picked up the endpoint yet.
    info "Waiting for cert-manager webhook endpoints to be populated..."
    retries=0
    until oc get endpoints -n cert-manager --no-headers 2>/dev/null \
        | grep -E "cert-manager-webhook" | grep -qv "<none>"; do
        sleep 5
        retries=$((retries + 1))
        [[ ${retries} -gt 24 ]] && { err "cert-manager webhook endpoints never became ready in 2m"; exit 1; }
    done
    info "cert-manager webhook is ready"
}

# --- cert-manager install ----------------------------------------------------

ensure_cert_manager() {
    if oc get deployment cert-manager -n cert-manager &>/dev/null; then
        info "cert-manager already installed — verifying readiness"
        wait_cert_manager_ready
        return 0
    fi

    log "Installing cert-manager ${CERT_MANAGER_VERSION}"
    # Try OLM subscription first (preferred on OCP), fall back to Helm
    if oc get packagemanifest cert-manager -n openshift-marketplace &>/dev/null 2>&1; then
        info "Installing cert-manager via OLM OperatorHub"

        # OLM requires the target namespace and an OperatorGroup to exist before the Subscription.
        # Create them idempotently via dry-run → apply.
        oc create namespace cert-manager --dry-run=client -o yaml | oc apply -f -

        # Resolve the actual catalog source name from the packagemanifest (avoids hardcoding)
        local cm_catalog
        cm_catalog="$(oc get packagemanifest cert-manager -n openshift-marketplace \
            -o jsonpath='{.status.catalogSource}' 2>/dev/null || echo "community-operators")"
        info "cert-manager catalog source: ${cm_catalog}"

        # OperatorGroup with empty targetNamespaces = AllNamespaces mode (cert-manager is cluster-wide)
        oc apply -n cert-manager -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: cert-manager
  namespace: cert-manager
spec: {}
EOF

        oc apply -n cert-manager -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: cert-manager
  namespace: cert-manager
spec:
  channel: stable
  installPlanApproval: Automatic
  name: cert-manager
  source: ${cm_catalog}
  sourceNamespace: openshift-marketplace
EOF
        info "Waiting for cert-manager operator deployment..."
        local retries=0
        until oc get deployment cert-manager -n cert-manager &>/dev/null; do
            sleep 10
            retries=$((retries + 1))
            [[ ${retries} -gt 36 ]] && { err "cert-manager operator did not become ready in 6m"; exit 1; }
        done
    else
        info "OLM not available — installing cert-manager via Helm"
        helm upgrade cert-manager oci://quay.io/jetstack/charts/cert-manager \
            --install \
            --version "${CERT_MANAGER_VERSION}" \
            --namespace cert-manager \
            --create-namespace \
            --set crds.enabled=true \
            --wait
    fi

    wait_cert_manager_ready
    info "cert-manager installed"
}

readonly CERT_MANAGER_VERSION="v1.20.0"

# --- Cluster domain resolution -----------------------------------------------

get_cluster_domain() {
    oc get ingresses.config.openshift.io cluster \
        -o jsonpath='{.spec.domain}' 2>/dev/null
}

# --- Wait helpers ------------------------------------------------------------

wait_deployment() {
    local name="$1"
    local timeout="${2:-3m}"
    info "Waiting for deployment/${name} to be available (timeout: ${timeout})..."
    oc rollout status "deployment/${name}" -n "${NS}" --timeout="${timeout}"
}

wait_certificate() {
    local name="$1"
    info "Waiting for Certificate/${name} to be Ready..."
    local retries=0
    until oc get certificate "${name}" -n "${NS}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | grep -q "True"; do
        sleep 5
        retries=$((retries + 1))
        [[ ${retries} -gt 36 ]] && { err "Certificate/${name} did not become Ready in 3m"; exit 1; }
    done
    info "Certificate/${name} is Ready"
}

# --- CA cert extraction ------------------------------------------------------

extract_ca_cert() {
    log "Extracting CA cert from cert-manager secret"
    mkdir -p "${DEPLOY_DIR}"

    local retries=0
    until oc get secret osac-ca -n cert-manager &>/dev/null; do
        sleep 5
        retries=$((retries + 1))
        [[ ${retries} -gt 24 ]] && { err "osac-ca secret did not appear in cert-manager namespace"; exit 1; }
    done

    oc get secret osac-ca -n cert-manager \
        -o go-template='{{ index .data "tls.crt" | base64decode }}' \
        > "${DEPLOY_DIR}/osac-ca.pem"
    info "CA cert written to deploy/osac-ca.pem"
}

# --- Main deploy -------------------------------------------------------------

deploy() {
    local cluster_domain
    cluster_domain="$(get_cluster_domain)"
    if [[ -z "${cluster_domain}" ]]; then
        err "Could not determine cluster domain from OCP ingress config"
        exit 1
    fi
    info "Cluster domain: ${cluster_domain}"

    local keycloak_host="ffs-keycloak.${NS}.${cluster_domain}"
    local grpc_host="ffs-grpc.${NS}.${cluster_domain}"

    log "Deploying OSAC backend to namespace: ${NS}"
    log "Keycloak Route:  https://${keycloak_host}"
    log "gRPC Route:      ${grpc_host}:443"

    # Enable HTTP/2 on the OCP ingress (required for gRPC — idempotent)
    log "Enabling HTTP/2 on the OCP ingress controller"
    oc annotate ingresses.config.openshift.io cluster \
        ingress.operator.openshift.io/default-enable-http2=true \
        --overwrite
    info "HTTP/2 enabled (ingress controller restart may take ~30s)"

    # Create namespace
    if ! oc get namespace "${NS}" &>/dev/null; then
        oc create namespace "${NS}"
        info "Namespace ${NS} created"
    else
        info "Namespace ${NS} already exists"
    fi

    # Install cert-manager if needed
    if [[ "${SKIP_CERT_MANAGER}" == false ]]; then
        ensure_cert_manager
    fi

    # Apply CA cert chain (cluster-scoped ClusterIssuer + cert-manager-namespaced Issuer/Certificate)
    log "Applying cert-manager CA chain"
    oc apply -f "${MANIFESTS_DIR}/cert-manager-ca.yaml"
    # Wait for the CA secret to be produced in cert-manager namespace
    extract_ca_cert

    # Copy CA cert into a ca-bundle ConfigMap for the fulfillment-service chart
    oc create configmap ca-bundle \
        -n "${NS}" \
        --from-file=bundle.pem="${DEPLOY_DIR}/osac-ca.pem" \
        --dry-run=client -o yaml | oc apply -f -
    info "ca-bundle ConfigMap applied"

    # Generate and apply the Keycloak TLS Certificate (needs Route hostname in dnsNames)
    log "Applying Keycloak TLS Certificate"
    oc apply -n "${NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ffs-keycloak
  labels:
    app.kubernetes.io/part-of: osac-test-backend
spec:
  issuerRef:
    kind: ClusterIssuer
    name: osac-ca
  dnsNames:
    - ffs-keycloak
    - ffs-keycloak.${NS}.svc.cluster.local
    - ${keycloak_host}
  secretName: ffs-keycloak-tls
EOF

    # Deploy Postgres
    log "Deploying ffs-postgres"
    oc apply -n "${NS}" -f "${MANIFESTS_DIR}/postgres.yaml"
    wait_deployment ffs-postgres 3m

    # Create Keycloak realm ConfigMap from vendored realm.json
    oc create configmap ffs-keycloak-realm \
        -n "${NS}" \
        --from-file=realm.json="${MANIFESTS_DIR}/realm.json" \
        --dry-run=client -o yaml | oc apply -f -
    info "ffs-keycloak-realm ConfigMap applied"

    # Wait for Keycloak cert before deploying (it needs the TLS secret)
    wait_certificate ffs-keycloak

    # Deploy Keycloak
    log "Deploying ffs-keycloak"
    oc apply -n "${NS}" -f "${MANIFESTS_DIR}/keycloak.yaml"
    wait_deployment ffs-keycloak 4m

    # Create Keycloak passthrough Route
    log "Creating Keycloak OCP Route (passthrough TLS)"
    oc apply -n "${NS}" -f - <<EOF
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: ffs-keycloak
  labels:
    app.kubernetes.io/name: ffs-keycloak
    app.kubernetes.io/part-of: osac-test-backend
spec:
  host: ${keycloak_host}
  to:
    kind: Service
    name: ffs-keycloak
  port:
    targetPort: https
  tls:
    termination: passthrough
EOF

    # Apply fulfillment-service secrets
    log "Applying fulfillment-service secrets"
    oc apply -n "${NS}" -f "${MANIFESTS_DIR}/fulfillment-secrets.yaml"

    # Generate fulfillment-service Helm values for OCP
    local values_file
    values_file="$(mktemp /tmp/ffs-values-ocp.XXXXXX.yaml)"
    cat > "${values_file}" <<EOF
# Generated by deploy-osac-backend.sh for namespace ${NS} on ${cluster_domain}
variant: kind

certs:
  issuerRef:
    kind: ClusterIssuer
    name: osac-ca
  caBundle:
    configMap: ca-bundle

# externalHostname: used in the chart's TLS cert SANs for external access.
# Set to the gRPC Route hostname so TLS verification succeeds from outside the cluster.
externalHostname: ${grpc_host}
internalHostname: fulfillment-grpc-server.${NS}.svc.cluster.local

auth:
  # In-cluster Keycloak URL for service-to-service (fulfillment-service -> Keycloak)
  issuerUrl: "https://ffs-keycloak.${NS}.svc.cluster.local:8443/realms/osac"
  controllerCredentials:
    - secret:
        name: ffs-controller-credentials
        items:
          - key: client-id
            param: client-id
          - key: client-secret
            param: client-secret

idp:
  provider: keycloak
  url: "https://ffs-keycloak.${NS}.svc.cluster.local:8443"
  credentials:
    - secret:
        name: ffs-controller-credentials
        items:
          - key: client-id
            param: client-id
          - key: client-secret
            param: client-secret

database:
  connection:
    - secret:
        name: ffs-database-credentials
        items:
          - key: url
            param: url
          - key: user
            param: user
          - key: password
            param: password

log:
  level: debug
EOF

    # Install fulfillment-service Helm chart.
    # The chart renders TLSRoute (Gateway API) objects which don't exist on stock OCP —
    # filter them out with yq before applying, same as the OSAC SP's own CI workflow.
    log "Installing fulfillment-service chart (version: ${FULFILLMENT_SERVICE_CHART_VERSION})"
    if ! command -v yq &>/dev/null; then
        err "'yq' is required to filter the fulfillment-service chart manifests"
        err "Install: brew install yq  OR  go install github.com/mikefarah/yq/v4@latest"
        rm -f "${values_file}"
        exit 1
    fi

    local rendered_file="/tmp/ffs-rendered.yaml"
    local filtered_file="/tmp/ffs-rendered-filtered.yaml"

    helm template ffs-fulfillment-service \
        oci://ghcr.io/osac-project/charts/fulfillment-service \
        --version "${FULFILLMENT_SERVICE_CHART_VERSION}" \
        --namespace "${NS}" \
        --values "${values_file}" \
        2>&1 | grep -Ev '^(Pulled|Digest): ' > "${rendered_file}"

    yq eval-all 'select(. != null) | select(.kind != "TLSRoute")' "${rendered_file}" \
        > "${filtered_file}"

    oc apply -n "${NS}" -f "${filtered_file}"
    rm -f "${values_file}" "${rendered_file}" "${filtered_file}"

    log "Waiting for fulfillment-grpc-server to be available (up to 5m)"
    oc rollout status deployment/fulfillment-grpc-server -n "${NS}" --timeout=5m

    # Discover the gRPC server Service port (for the Route targetPort)
    local grpc_port
    grpc_port="$(oc get service fulfillment-grpc-server -n "${NS}" \
        -o jsonpath='{.spec.ports[0].name}' 2>/dev/null)"
    if [[ -z "${grpc_port}" ]]; then
        # Fallback: use port number
        grpc_port="$(oc get service fulfillment-grpc-server -n "${NS}" \
            -o jsonpath='{.spec.ports[0].port}' 2>/dev/null)"
    fi

    # Create gRPC passthrough Route (HTTP/2 + TLS)
    log "Creating gRPC OCP Route (passthrough TLS)"
    oc apply -n "${NS}" -f - <<EOF
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: ffs-grpc
  labels:
    app.kubernetes.io/part-of: osac-test-backend
  annotations:
    haproxy.router.openshift.io/timeout: 5m
spec:
  host: ${grpc_host}
  to:
    kind: Service
    name: fulfillment-grpc-server
  port:
    targetPort: "${grpc_port}"
  tls:
    termination: passthrough
EOF

    info "gRPC Route created at ${grpc_host}:443"

    # Write output env file (Phase 1 complete)
    write_env_file "${keycloak_host}" "${grpc_host}"

    # --- Phase 2: osac-operator + BMFO + aap-mock + fixtures ----------------
    if [[ "${SKIP_PHASE2}" == true ]]; then
        info "Skipping Phase 2 (--skip-phase2 set) — clusters will not reach ACTIVE"
    else
        deploy_phase2
    fi

    log "OSAC backend deployed successfully"
    log ""
    log "Next step (deploy-dcm.sh auto-detects deploy/osac-backend.env):"
    log "  ./scripts/deploy-dcm.sh --environment-agent --osac-service-provider"
    log ""
    log "Then run tests:"
    log "  make test-osac-sp"
    log ""
    log "For full CRUD + NATS coverage, also export template IDs discovered"
    log "from this backend before running tests:"
    log "  OSAC_E2E_CLUSTER_TEMPLATE_ID=<id> OSAC_E2E_VM_TEMPLATE_ID=<id> make test-osac-sp"
    log ""
    log "See .cursor/prompts/deploy-osac-backend.md for the full runbook."
}

# --- Upstream manifest fetching ----------------------------------------------
# fetch_upstream FILE DEST_DIR [SUBPATH]
# Downloads FILE from the pinned upstream ref into DEST_DIR.
# SUBPATH defaults to test/e2e/manifests-tierb; pass crds/ or tierb-config/ as needed.
fetch_upstream() {
    local file="$1"
    local dest_dir="$2"
    local subpath="${3:-test/e2e/manifests-tierb}"
    local url="${OSAC_SP_UPSTREAM_RAW}/${OSAC_SP_UPSTREAM_REF}/${subpath}/${file}"
    curl --fail --silent --show-error --location \
        -o "${dest_dir}/${file}" "${url}" \
        || { err "Failed to download ${url}"; return 1; }
}

# --- Phase 2: osac-operator + BMFO + aap-mock + BareMetalHost fixtures ------
# Enables clusters to reach ACTIVE status (kubeconfig delivery).
# Mirrors dcm-project/osac-service-provider's own Tier B CI Phase 2 setup.
# Unmodified upstream files are downloaded at deploy time from OSAC_SP_UPSTREAM_REF
# rather than committed to this repo. Only aap-mock.yaml is committed because we
# changed the image reference to a Quay-published build.
deploy_phase2() {
    local phase2_dir="${MANIFESTS_DIR}/phase2"
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    trap 'rm -rf "${tmp_dir}"' RETURN

    log "Deploying Phase 2 components (osac-operator, BMFO, aap-mock, fixtures)"
    info "Fetching upstream manifests at ref ${OSAC_SP_UPSTREAM_REF}"

    # --- CRDs (not bundled in any Helm chart — must be pre-applied) ----------
    log "Installing Phase 2 CRDs"
    local crds_tmp="${tmp_dir}/crds"
    mkdir -p "${crds_tmp}"
    local crd
    for crd in \
        baremetalhosts.metal3.io.yaml \
        clusterorders.osac.openshift.io.yaml \
        hostedclusters.hypershift.openshift.io.yaml \
        nodepools.hypershift.openshift.io.yaml \
        osac.openshift.io_baremetalinstances.yaml \
        osac.openshift.io_baremetalpools.yaml \
        osac.openshift.io_computeinstances.yaml \
        tenants.osac.openshift.io.yaml; do
        fetch_upstream "${crd}" "${crds_tmp}" "test/e2e/manifests-tierb/crds"
        # baremetalhosts.metal3.io is owned by CNV/metal3 on OCP; applying it
        # conflicts with the cluster-version-operator. Skip if already present.
        if [[ "${crd}" == "baremetalhosts.metal3.io.yaml" ]] \
                && oc get crd baremetalhosts.metal3.io &>/dev/null; then
            info "baremetalhosts.metal3.io already present (CNV/metal3) — skipping"
            continue
        fi
        # --server-side avoids annotation size limit on large CRDs.
        oc_apply_filtered -f "${crds_tmp}/${crd}" --server-side
    done
    info "CRDs applied"

    # Track Phase 2 ownership via labels on the resources themselves (not a
    # local file). Labels survive re-deploys and a missing deploy/ directory;
    # tear_down only deletes resources bearing PHASE2_PART_OF_LABEL.

    # --- Agent CRD stub (required by osac-operator v0.0.18) ------------------
    # Gap 4 fix (FLPATH-4788): osac-operator 0.0.18+ expects the Agent CRD from
    # Assisted Installer (agent-install.openshift.io/v1beta1) to be present.
    # Without it the operator pod crashes at startup with "no matches for kind Agent".
    # In Tier B we provide a minimal stub CRD; no real Agent objects are created.
    # Clusters remain PROGRESSING (not ACTIVE) — full Agent provisioning is Tier C.
    if oc get crd agents.agent-install.openshift.io &>/dev/null; then
        if phase2_owned crd agents.agent-install.openshift.io; then
            info "Agent CRD stub already present (owned) — leaving in place"
        else
            info "Agent CRD already present without our label — leaving in place (will not delete on teardown)"
        fi
    else
        log "Applying Agent CRD stub (osac-operator v0.0.18 requirement)"
        oc_apply_filtered -f "${phase2_dir}/agent-crd-stub.yaml" --server-side
        info "Agent CRD stub applied (labeled ${PHASE2_PART_OF_KEY}=${PHASE2_PART_OF_LABEL})"
    fi

    # --- hardware-inventory namespace (required by osac-operator v0.0.18) ----
    # The operator's Agent watch is scoped to the hardware-inventory namespace;
    # it must exist before the operator starts or the watch will error-loop.
    if oc get namespace hardware-inventory &>/dev/null; then
        if phase2_owned namespace hardware-inventory; then
            info "hardware-inventory namespace already present (owned) — leaving in place"
        else
            info "hardware-inventory namespace already exists without our label — leaving in place (will not delete on teardown)"
        fi
    else
        log "Creating hardware-inventory namespace"
        oc create namespace hardware-inventory
        oc label namespace hardware-inventory \
            "${PHASE2_PART_OF_KEY}=${PHASE2_PART_OF_LABEL}"
        info "hardware-inventory created and labeled for teardown ownership"
    fi

    # --- BMFO stub secrets ---------------------------------------------------
    log "Applying BMFO stub secrets"
    fetch_upstream "bmfo-secrets.yaml" "${tmp_dir}"
    oc apply -n "${NS}" -f "${tmp_dir}/bmfo-secrets.yaml"

    # --- osac-operator Helm chart --------------------------------------------
    # Gap 3/4 fix (FLPATH-4788): chart 0.0.18 is proto-compatible with
    # fulfillment-service 0.0.107 (state_transition_time is Timestamp — OSAC-2928).
    # Chart 0.0.12 / image v0.0.12 is INCOMPATIBLE: gRPC unmarshal fails with
    # "string field contains invalid UTF-8" on Timestamp fields.
    #
    # We use a local values file (tests/osac-backend/phase2/osac-operator-values.yaml)
    # instead of fetching the upstream test/e2e/tierb-config/osac-operator-values.yaml,
    # because the upstream file pins to v0.0.12.
    log "Installing osac-operator (chart version: ${OSAC_OPERATOR_CHART_VERSION})"
    local op_values="${phase2_dir}/osac-operator-values.yaml"
    if oc get deployment osac-operator -n "${NS}" &>/dev/null; then
        info "osac-operator already installed — upgrading"
        helm upgrade osac-operator oci://ghcr.io/osac-project/charts/osac-operator \
            --version "${OSAC_OPERATOR_CHART_VERSION}" \
            --namespace "${NS}" \
            --values "${op_values}" \
            --reuse-values 2>&1 | tail -3
    else
        helm install osac-operator oci://ghcr.io/osac-project/charts/osac-operator \
            --version "${OSAC_OPERATOR_CHART_VERSION}" \
            --namespace "${NS}" \
            --values "${op_values}" \
            2>&1 | tail -3
    fi
    oc rollout status deployment/osac-operator -n "${NS}" --timeout=3m

    # --- Agent RBAC for osac-operator (v0.0.18 gap-fill) ---------------------
    # osac-operator 0.0.18's manager ClusterRole doesn't include get/list/watch
    # for agents.agent-install.openshift.io, causing controller startup errors.
    # Patch idempotently: skip if the rule is already present.
    log "Patching osac-operator ClusterRole for Agent access"
    # Note: chart 0.0.18 names the manager role "osac-operator-manager" (no "-role" suffix).
    if oc get clusterrole osac-operator-manager \
            -o jsonpath='{.rules[*].apiGroups}' 2>/dev/null \
            | grep -qF 'agent-install.openshift.io'; then
        info "Agent RBAC already present on osac-operator-manager — skipping"
    else
        oc patch clusterrole "osac-operator-manager" \
            --type=json \
            -p='[{"op":"add","path":"/rules/-","value":{"apiGroups":["agent-install.openshift.io"],"resources":["agents"],"verbs":["get","list","watch"]}}]'
        info "Agent RBAC patched"
    fi

    # --- Hub-access RBAC gap-fill --------------------------------------------
    log "Applying hub-access RBAC for osac-operator"
    fetch_upstream "hub-access-hosted-clusters-rbac.yaml" "${tmp_dir}"
    oc apply -n "${NS}" -f "${tmp_dir}/hub-access-hosted-clusters-rbac.yaml"

    # --- BMFO Helm chart -----------------------------------------------------
    log "Installing bare-metal-fulfillment-operator (chart version: ${BMFO_CHART_VERSION})"
    if oc get deployment bmf-operator-controller-manager -n "${NS}" &>/dev/null; then
        info "BMFO already installed — upgrading"
        helm upgrade bmf-operator oci://ghcr.io/osac-project/charts/bare-metal-fulfillment-operator \
            --version "${BMFO_CHART_VERSION}" \
            --namespace "${NS}" \
            --reuse-values 2>&1 | tail -3
    else
        helm install bmf-operator oci://ghcr.io/osac-project/charts/bare-metal-fulfillment-operator \
            --version "${BMFO_CHART_VERSION}" \
            --namespace "${NS}" \
            2>&1 | tail -3
    fi
    oc rollout status deployment/bmf-operator-controller-manager -n "${NS}" --timeout=3m

    # --- osac-aap-mock -------------------------------------------------------
    # aap-mock.yaml is committed locally: we changed the image to quay.io/dcm-project/osac-aap-mock:tierb
    # (upstream uses a CI-local build; we publish to a public Quay repo so OCP can pull it).
    log "Deploying osac-aap-mock"
    oc apply -n "${NS}" -f "${phase2_dir}/aap-mock.yaml"
    oc rollout status deployment/osac-aap-mock -n "${NS}" --timeout=2m

    # --- BareMetalHost fixtures ----------------------------------------------
    # Upstream manifests already carry app.kubernetes.io/part-of=osac-sp-e2e-tierb.
    # Before applying, check each fixture: skip if a same-named BMH exists
    # without our label (another workload owns it). If it has our label or
    # does not exist, apply (idempotent). After apply, assert the label is
    # present — guards against upstream manifest drift that drops the label.
    log "Applying BareMetalHost fixtures"
    local fixture bmh_name
    for fixture in \
        baremetalhost-fixture-always.yaml \
        baremetalhost-fixture-cleanup.yaml \
        baremetalhost-fixture-contended.yaml \
        baremetalhost-fixture-ineligible.yaml \
        baremetalhost-fixture-unset.yaml; do
        fetch_upstream "${fixture}" "${tmp_dir}"
        # Extract the metadata.name from the YAML (first occurrence)
        bmh_name="$(grep -m1 '^\s*name:' "${tmp_dir}/${fixture}" | awk '{print $2}')"
        if oc get baremetalhost "${bmh_name}" -n default &>/dev/null \
                && ! phase2_owned baremetalhost "${bmh_name}" default; then
            info "BareMetalHost '${bmh_name}' exists in default without our label — skipping (not owned)"
            continue
        fi
        oc apply -n default -f "${tmp_dir}/${fixture}"
        # Post-apply verification: ensure the label is present.
        if ! phase2_owned baremetalhost "${bmh_name}" default; then
            err "BareMetalHost '${bmh_name}' applied but missing label ${PHASE2_PART_OF_KEY}=${PHASE2_PART_OF_LABEL}"
            err "Upstream fixture YAML may have drifted — fix the manifest and re-run"
            return 1
        fi
    done
    info "BareMetalHost fixtures applied in namespace 'default' (matches BMFO inventory config)"

    # --- Hub, HostType, ClusterTemplate + ClusterVersion registration ---------
    # Uses grpcurl against the fulfillment-service internal API (port-forwarded).
    # The osac CLI requires interactive device auth (oauth2 device flow) so we
    # call the gRPC endpoints directly instead.
    log "Registering hub, HostType, ClusterTemplate, and ClusterVersion with fulfillment-service"
    register_fixtures

    info "Phase 2 deployment complete — clusters will advance to PROGRESSING via osac-operator + BMFO + aap-mock"
    info "Note: full ACTIVE status (kubeconfig delivery) requires real Agents — this is Tier C territory"
}

# register_fixtures port-forwards the fulfillment-service internal API *and*
# Keycloak, then registers the Hub, HostType, ClusterTemplate, and ClusterVersion
# required for ClusterOrder dispatch to proceed in Tier B.
#
# Uses grpcurl with server reflection (no proto files needed).
# Create is treated as success when the object is returned OR when the API
# reports AlreadyExists (idempotent re-run). Any other error fails the deploy.
#
# Cleanup: PF PIDs are killed explicitly at the end (and on early failure via
# `|| rc=$?` + unconditional kill). Does NOT install a RETURN trap — that would
# overwrite deploy_phase2's tmp-dir RETURN trap.
register_fixtures() {
    local internal_api_port=18083
    # Keycloak MUST be reached on :8443 — the token's issuer URL embeds this
    # port, and fulfillment-service rejects tokens whose issuer is not in its
    # trust list (a forward on any other port produces Unauthenticated).
    local keycloak_port=8443
    local pf_api_pid="" pf_kc_pid=""
    local rc=0
    local own_kc_pf=false

    oc port-forward svc/fulfillment-internal-api "${internal_api_port}:8001" \
        -n "${NS}" >/tmp/pf-fulfillment-internal-api.log 2>&1 &
    pf_api_pid=$!

    # Reuse an existing :8443 listener (e.g. make port-forward-osac) when present;
    # otherwise start our own forward and clean it up when we finish.
    if wait_local_port "${keycloak_port}" 1; then
        info "Keycloak already reachable on :${keycloak_port} — reusing existing forward"
    else
        oc port-forward svc/ffs-keycloak "${keycloak_port}:8443" \
            -n "${NS}" >/tmp/pf-ffs-keycloak.log 2>&1 &
        pf_kc_pid=$!
        own_kc_pf=true
    fi

    if ! wait_local_port "${internal_api_port}" 30; then
        err "fulfillment-internal-api port-forward on :${internal_api_port} did not become ready"
        rc=1
    elif ! wait_local_port "${keycloak_port}" 30; then
        err "ffs-keycloak not reachable on :${keycloak_port}"
        rc=1
    else
        _register_fixtures_body "${internal_api_port}" "${keycloak_port}" || rc=$?
    fi

    kill "${pf_api_pid}" 2>/dev/null || true
    wait "${pf_api_pid}" 2>/dev/null || true
    if [[ "${own_kc_pf}" == true && -n "${pf_kc_pid}" ]]; then
        kill "${pf_kc_pid}" 2>/dev/null || true
        wait "${pf_kc_pid}" 2>/dev/null || true
    fi
    return "${rc}"
}

# wait_local_port PORT [TIMEOUT_SECS]
# Returns 0 once 127.0.0.1:PORT accepts a TCP connection.
wait_local_port() {
    local port="$1"
    local timeout="${2:-30}"
    local i
    for ((i = 1; i <= timeout; i++)); do
        if (echo >/dev/tcp/127.0.0.1/"${port}") 2>/dev/null; then
            return 0
        fi
        sleep 1
    done
    return 1
}

_register_fixtures_body() {
    local internal_api_port="$1"
    local keycloak_port="$2"

    # Get OIDC token (client credentials — no browser interaction)
    local token
    token="$(curl --fail --silent --show-error \
        --resolve "ffs-keycloak.${NS}.svc.cluster.local:${keycloak_port}:127.0.0.1" \
        --cacert "${DEPLOY_DIR}/osac-ca.pem" \
        --request POST \
        "https://ffs-keycloak.${NS}.svc.cluster.local:${keycloak_port}/realms/osac/protocol/openid-connect/token" \
        -d "client_id=osac-admin&client_secret=tierb-osac-admin-secret&grant_type=client_credentials" \
        | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')"
    if [[ -z "${token}" ]]; then
        err "Failed to obtain OIDC token — fixture registration aborted"
        return 1
    fi

    # Kubeconfig for the Hub: in-cluster endpoint so the fulfillment-service
    # (running inside OCP) can reach the API server via kubernetes.default.svc.
    # Use `oc` (already required) rather than `kubectl`.
    local kubeconfig_b64
    kubeconfig_b64="$(oc config view --raw --minify \
        | sed -E 's#server: https://[^[:space:]]+#server: https://kubernetes.default.svc#' \
        | base64 | tr -d '\n')"

    grpc_create() {
        # grpc_create LABEL SERVICE METHOD JSON_BODY
        # Fails the function (and thus Phase 2) on non-idempotent errors.
        #
        # Two success paths:
        #   1. grpcurl exits 0 AND output contains "id"  → created/returned.
        #   2. grpcurl exits non-zero AND output is an AlreadyExists error → idempotent re-run.
        # Anything else is a hard failure.
        local label="$1" service="$2" method="$3" body="$4"
        local result rc=0
        result="$(grpcurl -insecure \
            -H "Authorization: Bearer ${token}" \
            -d "${body}" \
            "127.0.0.1:${internal_api_port}" \
            "${service}/${method}" 2>&1)" || rc=$?
        if [[ "${rc}" -eq 0 ]] && echo "${result}" | grep -q '"id"'; then
            info "  ${label} created ✓"
            return 0
        fi
        if [[ "${rc}" -ne 0 ]] \
                && echo "${result}" | grep -qiE 'AlreadyExists|already exists|ALREADY_EXISTS'; then
            info "  ${label} already exists — ok"
            return 0
        fi
        err "  ${label} registration failed (grpcurl exit ${rc}):"
        echo "${result}" >&2
        return 1
    }

    info "  Registering Hub"
    grpc_create "Hub" osac.private.v1.Hubs Create \
        "{\"object\":{\"metadata\":{\"name\":\"default-hub\"},\"spec\":{\"kubeconfig\":\"${kubeconfig_b64}\",\"namespace\":\"default\"}}}" \
        || return 1

    info "  Registering HostType"
    grpc_create "HostType" osac.public.v1.HostTypes Create \
        '{"object":{"id":"standard","metadata":{"name":"standard"},"title":"Tier B standard host","description":"Tier B dispatch fixture host type"}}' \
        || return 1

    info "  Registering ClusterTemplate"
    grpc_create "ClusterTemplate(id=default-hcp)" osac.public.v1.ClusterTemplates Create \
        '{"object":{"id":"default-hcp","metadata":{"name":"default-hcp"},"title":"Tier B test cluster","description":"Tier B dispatch fixture","node_sets":{"compute":{"host_type":{"id":"standard"},"size":3}}}}' \
        || return 1

    info "  Registering ClusterVersion"
    grpc_create "ClusterVersion" osac.private.v1.ClusterVersions Create \
        '{"object":{"metadata":{"name":"tierb-1-30-0"},"spec":{"image":"quay.io/e2e-test/cluster:1.30.0","version":"1.30.0","enabled":true,"is_default":true}}}' \
        || return 1

    info "Fixtures registered — use OSAC_E2E_CLUSTER_TEMPLATE_ID=default-hcp for tests"
}

write_env_file() {
    local keycloak_host="$1"
    local grpc_host="$2"

    mkdir -p "${DEPLOY_DIR}"
    cat > "${DEPLOY_DIR}/osac-backend.env" <<EOF
# OSAC backend env — generated by deploy-osac-backend.sh
# Source this file before running deploy-dcm.sh with --osac-service-provider:
#   source deploy/osac-backend.env
#
# Credentials are test-only values committed to git (NFR-TB-020).
# Do not use these for any real deployment.

export SP_OSAC_FULFILLMENT_ADDRESS="${grpc_host}:443"
export SP_OSAC_OIDC_ISSUER_URL="https://${keycloak_host}/realms/osac"
export SP_OSAC_OIDC_CLIENT_ID="osac-admin"
export SP_OSAC_OIDC_CLIENT_SECRET="tierb-osac-admin-secret"
export OSAC_CA_CERT_FILE="${DEPLOY_DIR}/osac-ca.pem"
EOF

    info "Env vars written to deploy/osac-backend.env"
    info "CA cert written to deploy/osac-ca.pem"
}

# --- Entry point -------------------------------------------------------------

check_tools
check_oc_login
check_cluster_health

if [[ "${TEAR_DOWN}" == true ]]; then
    tear_down
else
    deploy
fi
