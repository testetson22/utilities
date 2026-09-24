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
TEAR_DOWN=false
SKIP_CERT_MANAGER=false

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
  --tear-down           Remove the backend namespace and all its resources
  --namespace NS        OCP namespace to deploy into (default: ${OSAC_BACKEND_NAMESPACE})
  --chart-version VER   fulfillment-service Helm chart version (default: ${FULFILLMENT_SERVICE_CHART_VERSION})
  --skip-cert-manager   Skip cert-manager install; assume it is already present
  --help                Show this help message

Environment variables:
  OSAC_BACKEND_NAMESPACE        Override the default namespace
  FULFILLMENT_SERVICE_CHART_VERSION  Override the chart version pin

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
        --help)                usage; exit 0 ;;
        *)                     err "Unknown option: $1"; usage; exit 1 ;;
    esac
done

readonly NS="${OSAC_BACKEND_NAMESPACE}"

# --- Tool checks -------------------------------------------------------------

check_tools() {
    local missing=()
    for tool in oc helm curl jq; do
        command -v "${tool}" &>/dev/null || missing+=("${tool}")
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing required tools: ${missing[*]}"
        exit 1
    fi
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

tear_down() {
    log "Tearing down OSAC backend (namespace: ${NS})"

    if oc get namespace "${NS}" &>/dev/null; then
        oc delete namespace "${NS}" --wait=true
        info "Namespace ${NS} deleted"
    else
        info "Namespace ${NS} not found — nothing to delete"
    fi

    # Remove cluster-scoped resources owned by this backend.
    log "Removing cluster-scoped resources"
    oc delete clusterissuer osac-ca --ignore-not-found
    info "ClusterIssuer osac-ca removed"
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
    rm -f "${DEPLOY_DIR}/osac-backend.env" "${DEPLOY_DIR}/osac-ca.pem"
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

    # Write output env file
    write_env_file "${keycloak_host}" "${grpc_host}"

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
