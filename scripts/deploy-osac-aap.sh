#!/usr/bin/env bash
# Deploy the disposable AAP 2.7 controller/Gateway integration used by OSAC.
# This is opt-in; the OSAC AAP mock remains the default path.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
AAP_NAMESPACE="${OSAC_AAP_NAMESPACE:-osac-aap-test}"
OSAC_NAMESPACE="${OSAC_BACKEND_NAMESPACE:-osac-test-backend}"
MANIFEST_FILE="${OSAC_AAP_MANIFEST:-${REPO_ROOT}/tests/manifest.zip}"
AAP_API_PORT="${OSAC_AAP_API_PORT:-18081}"
TEAR_DOWN=false

log() { printf '==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
err() { printf 'ERROR: %s\n' "$*" >&2; }

usage() {
    cat <<EOF
Usage: $(basename "$0") [--tear-down]

Deploy or remove the disposable AAP 2.7 integration used by OSAC tests.

Environment:
  OSAC_AAP_NAMESPACE  AAP namespace (default: ${AAP_NAMESPACE})
  OSAC_BACKEND_NAMESPACE OSAC backend namespace (default: ${OSAC_NAMESPACE})
  OSAC_AAP_MANIFEST    subscription manifest zip (default: ${MANIFEST_FILE})
  OSAC_AAP_API_PORT    local Gateway port-forward (default: ${AAP_API_PORT})
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tear-down) TEAR_DOWN=true; shift ;;
        --help) usage; exit 0 ;;
        *) err "Unknown option: $1"; usage; exit 1 ;;
    esac
done

for tool in oc curl jq openssl base64; do
    command -v "${tool}" >/dev/null 2>&1 || { err "Missing required tool: ${tool}"; exit 1; }
done

if [[ "${TEAR_DOWN}" == true ]]; then
    log "Removing disposable AAP namespace ${AAP_NAMESPACE}"
    oc delete namespace "${AAP_NAMESPACE}" --ignore-not-found --wait=true
    oc delete secret osac-aap-gateway-token -n "${OSAC_NAMESPACE}" --ignore-not-found
    exit 0
fi

[[ -s "${MANIFEST_FILE}" ]] || { err "AAP subscription manifest not found: ${MANIFEST_FILE}"; exit 1; }

if ! oc get namespace "${AAP_NAMESPACE}" >/dev/null 2>&1; then
    oc create namespace "${AAP_NAMESPACE}"
fi

oc apply -f - <<'YAML'
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: osac-aap-test
  namespace: osac-aap-test
spec:
  targetNamespaces:
    - osac-aap-test
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: ansible-automation-platform-operator
  namespace: osac-aap-test
spec:
  channel: stable-2.7
  installPlanApproval: Automatic
  name: ansible-automation-platform-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
YAML

log "Waiting for AAP Operator CSV"
oc wait --for=jsonpath='{.status.phase}'=Succeeded \
    csv -l operators.coreos.com/ansible-automation-platform-operator.osac-aap-test \
    -n "${AAP_NAMESPACE}" --timeout=5m

db_password="$(oc get secret aap-postgres-runtime -n "${AAP_NAMESPACE}" -o jsonpath='{.data.POSTGRESQL_PASSWORD}' 2>/dev/null | base64 --decode || true)"
if [[ -z "${db_password}" ]]; then
    db_password="$(openssl rand -hex 24)"
    oc create secret generic aap-postgres-runtime -n "${AAP_NAMESPACE}" \
        --from-literal=POSTGRESQL_USER=controller \
        --from-literal=POSTGRESQL_PASSWORD="${db_password}" \
        --from-literal=POSTGRESQL_DATABASE=controller
fi
admin_password="$(oc get secret aap-controller-admin-password -n "${AAP_NAMESPACE}" -o jsonpath='{.data.password}' 2>/dev/null | base64 --decode || true)"
if [[ -z "${admin_password}" ]]; then
    admin_password="${OSAC_AAP_ADMIN_PASSWORD:-$(openssl rand -hex 24)}"
    oc create secret generic aap-controller-admin-password -n "${AAP_NAMESPACE}" \
        --from-literal=password="${admin_password}"
fi
oc apply -f - <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: aap-postgres-config
  namespace: ${AAP_NAMESPACE}
type: Opaque
stringData:
  host: aap-postgres.${AAP_NAMESPACE}.svc.cluster.local
  port: "5432"
  database: controller
  username: controller
  password: "${db_password}"
  sslmode: disable
  type: unmanaged
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: aap-postgres-data
  namespace: ${AAP_NAMESPACE}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: hostpath-csi
  resources:
    requests:
      storage: 20Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: aap-postgres
  namespace: ${AAP_NAMESPACE}
  labels:
    app.kubernetes.io/name: aap-postgres
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: aap-postgres
  template:
    metadata:
      labels:
        app.kubernetes.io/name: aap-postgres
    spec:
      containers:
        - name: postgres
          image: registry.redhat.io/rhel9/postgresql-15:1
          ports: [{name: postgres, containerPort: 5432}]
          env:
            - {name: POSTGRESQL_USER, valueFrom: {secretKeyRef: {name: aap-postgres-runtime, key: POSTGRESQL_USER}}}
            - {name: POSTGRESQL_PASSWORD, valueFrom: {secretKeyRef: {name: aap-postgres-runtime, key: POSTGRESQL_PASSWORD}}}
            - {name: POSTGRESQL_DATABASE, valueFrom: {secretKeyRef: {name: aap-postgres-runtime, key: POSTGRESQL_DATABASE}}}
          readinessProbe: {tcpSocket: {port: postgres}, initialDelaySeconds: 5, periodSeconds: 5}
          resources: {requests: {cpu: 100m, memory: 512Mi}, limits: {cpu: "1", memory: 2Gi}}
          volumeMounts: [{name: data, mountPath: /var/lib/pgsql/data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: aap-postgres-data}}]
---
apiVersion: v1
kind: Service
metadata:
  name: aap-postgres
  namespace: ${AAP_NAMESPACE}
spec:
  selector: {app.kubernetes.io/name: aap-postgres}
  ports: [{name: postgres, port: 5432, targetPort: postgres}]
YAML
oc rollout status deployment/aap-postgres -n "${AAP_NAMESPACE}" --timeout=3m

oc apply -f - <<YAML
apiVersion: automationcontroller.ansible.com/v1beta1
kind: AutomationController
metadata:
  name: osac-aap
  namespace: ${AAP_NAMESPACE}
spec:
  replicas: 2
  admin_user: admin
  admin_password_secret: aap-controller-admin-password
  postgres_configuration_secret: aap-postgres-config
  projects_persistence: false
  create_preload_data: true
  ingress_type: Route
  service_type: ClusterIP
  metrics_utility_enabled: false
YAML

oc apply -f - <<YAML
apiVersion: aap.ansible.com/v1alpha1
kind: AnsibleAutomationPlatform
metadata:
  name: osac-aap-platform
  namespace: ${AAP_NAMESPACE}
  admin_password_secret: aap-controller-admin-password
  ingress_type: Route
  route_tls_termination_mechanism: Edge
  controller: {name: osac-aap, disabled: false}
  api: {replicas: 1}
  metrics: {}
  database:
    postgres_storage_class: hostpath-csi
    storage_requirements: {requests: {storage: 20Gi}}
YAML

oc wait --for=condition=Available deployment/osac-aap-platform-gateway \
    -n "${AAP_NAMESPACE}" --timeout=5m

gateway_service="osac-aap-platform"
port_forward_pid=""
cleanup_port_forward() { [[ -z "${port_forward_pid}" ]] || kill "${port_forward_pid}" 2>/dev/null || true; }
trap cleanup_port_forward EXIT
if ! lsof -nP -iTCP:"${AAP_API_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
    oc port-forward --address 127.0.0.1 -n "${AAP_NAMESPACE}" service/${gateway_service} "${AAP_API_PORT}:80" >/tmp/osac-aap-port-forward.log 2>&1 &
    port_forward_pid=$!
    for _ in {1..20}; do lsof -nP -iTCP:"${AAP_API_PORT}" -sTCP:LISTEN >/dev/null 2>&1 && break; sleep 1; done
fi

gateway_port="${AAP_API_PORT}"
gateway_pat="$(oc get secret osac-aap-gateway-token -n "${OSAC_NAMESPACE}" \
    -o jsonpath='{.data.token}' 2>/dev/null | base64 --decode || true)"
if [[ -z "${gateway_pat}" ]]; then
    gateway_pat="$(curl --silent --show-error --fail --max-time 30 \
        --user "admin:${admin_password}" \
        --header 'Content-Type: application/json' \
        --data '{"description":"OSAC integration","scope":"write"}' \
        "http://127.0.0.1:${gateway_port}/api/gateway/v1/tokens/" | jq -er '.token')"
fi

manifest_b64="$(base64 < "${MANIFEST_FILE}" | tr -d '\n')"
curl --silent --show-error --fail --max-time 120 \
    --header "Authorization: Bearer ${gateway_pat}" \
    --header 'Content-Type: application/json' \
    --data "$(jq -n --arg manifest "${manifest_b64}" '{manifest:$manifest}')" \
    "http://127.0.0.1:${gateway_port}/api/controller/v2/config/" >/dev/null

oc create secret generic osac-aap-gateway-token -n "${OSAC_NAMESPACE}" \
    --from-literal=token="${gateway_pat}" --dry-run=client -o yaml | oc apply -f -

oc create configmap osac-noop-project -n "${AAP_NAMESPACE}" \
    --from-literal=osac-create-hosted-cluster.yml='---
- name: OSAC no-op dispatch
  hosts: localhost
  connection: local
  gather_facts: false
  vars:
    payload: "{{ ansible_eda.event.payload | default({}) }}"
  tasks:
    - name: Fail only when the caller requests the failure path
      ansible.builtin.fail:
        msg: Intentional OSAC AAP failure-path test; no infrastructure was changed
      when: payload.osac_test_result | default("success") == "failure"
    - ansible.builtin.debug:
        msg: "OSAC real AAP dispatch succeeded for {{ payload.metadata.name | default(\"unlinked\") }}"' \
    --dry-run=client -o yaml | oc apply -f -
extra_volumes=$' - name: osac-noop-project\n  configMap:\n    name: osac-noop-project'
extra_mount=$' - name: osac-noop-project\n  mountPath: /var/lib/awx/projects/osac-noop\n  readOnly: true'
oc patch automationcontroller osac-aap -n "${AAP_NAMESPACE}" --type=merge \
    -p "$(jq -n --arg v "${extra_volumes# }" --arg m "${extra_mount# }" '{spec:{projects_persistence:false,extra_volumes:$v,web_extra_volume_mounts:$m,task_extra_volume_mounts:$m}}')"
oc rollout status deployment/osac-aap-web -n "${AAP_NAMESPACE}" --timeout=5m
oc rollout status deployment/osac-aap-task -n "${AAP_NAMESPACE}" --timeout=5m

gateway_api="http://127.0.0.1:${gateway_port}/api/controller/v2"
project_response="$(curl --silent --show-error --fail --max-time 30 \
    --header "Authorization: Bearer ${gateway_pat}" \
    "${gateway_api}/projects/?name=OSAC%20No-op%20Project")"
project_id="$(jq -er '.results[0].id // empty' <<<"${project_response}" || true)"
if [[ -z "${project_id}" ]]; then
    project_id="$(curl --silent --show-error --fail --max-time 30 \
        --header "Authorization: Bearer ${gateway_pat}" \
        --header 'Content-Type: application/json' \
        --data '{"name":"OSAC No-op Project","description":"OSAC real AAP contract-test playbooks","organization":1,"scm_type":"","local_path":"osac-noop"}' \
        "${gateway_api}/projects/" | jq -er '.id')"
fi
inventory_id="$(curl --silent --show-error --fail --max-time 30 \
    --header "Authorization: Bearer ${gateway_pat}" \
    "${gateway_api}/inventories/?name=Demo%20Inventory" | jq -er '.results[0].id // 1')"
template_response="$(curl --silent --show-error --fail --max-time 30 \
    --header "Authorization: Bearer ${gateway_pat}" \
    "${gateway_api}/job_templates/?name=osac-create-hosted-cluster")"
template_id="$(jq -er '.results[0].id // empty' <<<"${template_response}" || true)"
if [[ -z "${template_id}" ]]; then
    template_id="$(curl --silent --show-error --fail --max-time 30 \
        --header "Authorization: Bearer ${gateway_pat}" \
        --header 'Content-Type: application/json' \
        --data "$(jq -n --argjson inventory "${inventory_id}" --argjson project "${project_id}" '{name:"osac-create-hosted-cluster",description:"OSAC real AAP contract-test template",job_type:"run",inventory:$inventory,project:$project,playbook:"osac-create-hosted-cluster.yml",ask_variables_on_launch:true,allow_simultaneous:true}')" \
        "${gateway_api}/job_templates/" | jq -er '.id')"
fi
info "OSAC real AAP template ready (project=${project_id}, job_template=${template_id})"

helm upgrade osac-operator oci://ghcr.io/osac-project/charts/osac-operator \
    --version "${OSAC_OPERATOR_CHART_VERSION:-0.0.18}" --namespace "${OSAC_NAMESPACE}" \
    --reuse-values --set-string aap.url="http://osac-aap-platform.${AAP_NAMESPACE}.svc.cluster.local/api/controller" \
    --set-string aap.token="${gateway_pat}" --set-string aap.insecureSkipVerify=true >/dev/null

log "Real AAP mode ready; OSAC operator uses Gateway-backed Controller API"
