#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

assert_equal() {
    local actual="$1" expected="$2" message="$3"
    if [[ "${actual}" != "${expected}" ]]; then
        printf 'FAIL: %s\n  expected: %s\n  actual:   %s\n' \
            "${message}" "${expected}" "${actual}" >&2
        exit 1
    fi
}

cat > "${tmp_dir}/rendered.yaml" <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fulfillment-grpc-server
spec:
  template:
    spec:
      containers:
        - name: grpc
          command:
            - fulfillment-service
            - --token-issuer=https://ffs-grpc.example.test:8000
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fulfillment-console-proxy
spec:
  template:
    spec:
      containers:
        - name: console-proxy
          command:
            - fulfillment-service
            - --token-issuer=https://ffs-grpc.example.test:8000
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: unrelated-service
spec:
  template:
    spec:
      containers:
        - name: unrelated
          command:
            - service
            - --token-issuer=https://unchanged.example.test:8000
YAML

bash "${REPO_ROOT}/scripts/normalize-fulfillment-token-issuer.sh" \
    "${tmp_dir}/rendered.yaml" "https://ffs-grpc.example.test"

corrected_count="$(yq eval-all '[select(.kind == "Deployment" and
    (.metadata.name == "fulfillment-grpc-server" or .metadata.name == "fulfillment-console-proxy"))
    | .spec.template.spec.containers[].command[]
    | select(. == "--token-issuer=https://ffs-grpc.example.test")] | length' "${tmp_dir}/rendered.yaml")"
assert_equal "${corrected_count}" "2" "both fulfillment components use the Route issuer"

unrelated_issuer="$(yq eval-all '[select(.metadata.name == "unrelated-service")
    | .spec.template.spec.containers[].command[]
    | select(test("^--token-issuer="))] | .[0]' "${tmp_dir}/rendered.yaml")"
assert_equal "${unrelated_issuer}" "--token-issuer=https://unchanged.example.test:8000" \
    "unrelated deployment issuer is preserved"

fake_bin="${tmp_dir}/bin"
mkdir -p "${fake_bin}"
cat > "${fake_bin}/lsof" <<'LSOF'
#!/usr/bin/env bash
case " $* " in
    *" -iTCP:8443 "*|*" -iTCP:19443 "*) printf 'LISTEN\n' ;;
    *) exit 1 ;;
esac
LSOF
chmod +x "${fake_bin}/lsof"

forward_output="$(PATH="${fake_bin}:${PATH}" bash "${REPO_ROOT}/scripts/osac-port-forward.sh" --ensure)"
if [[ "${forward_output}" != *"already listening"* ]]; then
    printf 'FAIL: --ensure did not preserve already-listening port-forwards\n%s\n' \
        "${forward_output}" >&2
    exit 1
fi

backend_line="$(grep -nF "KUBECONFIG=\"\${DCM_KUBECONFIG}\" bash \"\${REPO_ROOT}/scripts/deploy-osac-backend.sh\"" \
    "${REPO_ROOT}/scripts/deploy-dcm.sh" | grep -v -- '--tear-down' | cut -d: -f1)"
ensure_line="$(grep -nF 'ensure_osac_port_forwards || exit 1' \
    "${REPO_ROOT}/scripts/deploy-dcm.sh" | cut -d: -f1)"
validation_line="$(grep -nF '# Validate and export env vars for each enabled provider' \
    "${REPO_ROOT}/scripts/deploy-dcm.sh" | cut -d: -f1)"
if [[ -z "${backend_line}" || -z "${ensure_line}" || -z "${validation_line}" ]]; then
    printf 'FAIL: could not locate backend, port-forward, and provider-validation stages\n' >&2
    exit 1
fi
if (( backend_line >= ensure_line || ensure_line >= validation_line )); then
    printf 'FAIL: turnkey deploy order must be backend, port-forward ensure, provider validation\n' >&2
    exit 1
fi

if ! grep -Fq 'name: fulfillment-api' "${REPO_ROOT}/scripts/deploy-osac-backend.sh" || \
   ! grep -Fq 'normalize-fulfillment-token-issuer.sh' "${REPO_ROOT}/scripts/deploy-osac-backend.sh"; then
    printf 'FAIL: external Route must target fulfillment-api and normalize the Route issuer\n' >&2
    exit 1
fi

if ! grep -Fq -- '--osac-aap-mode MODE' "${REPO_ROOT}/scripts/deploy-dcm.sh" || \
   ! grep -Fq -- '--aap-mode MODE' "${REPO_ROOT}/scripts/deploy-osac-backend.sh" || \
   ! grep -Fq "OSAC_AAP_MODE=\"\${OSAC_AAP_MODE:-mock}\"" "${REPO_ROOT}/scripts/deploy-osac-backend.sh" || \
   ! grep -Fq "osac-aap-platform.\${AAP_NAMESPACE}.svc.cluster.local/api/controller" \
       "${REPO_ROOT}/scripts/deploy-osac-aap.sh"; then
    printf 'FAIL: mock/real AAP mode toggle wiring is incomplete\n' >&2
    exit 1
fi

printf 'OSAC deploy wiring tests passed\n'
