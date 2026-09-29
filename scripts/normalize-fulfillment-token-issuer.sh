#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
    printf 'Usage: %s <rendered-manifest.yaml> <external-issuer-url>\n' "$0" >&2
    exit 2
fi

manifest="$1"
issuer_url="$2"

if [[ ! -f "${manifest}" ]]; then
    printf 'Rendered manifest not found: %s\n' "${manifest}" >&2
    exit 1
fi
if [[ "${issuer_url}" != https://* ]]; then
    printf 'Token issuer must use https: %s\n' "${issuer_url}" >&2
    exit 1
fi
if ! command -v yq >/dev/null 2>&1; then
    printf 'yq v4 is required to normalize the fulfillment token issuer\n' >&2
    exit 1
fi

local_token_issuer="--token-issuer=${issuer_url}"
TOKEN_ISSUER_ARG="${local_token_issuer}" yq eval-all -i '
    select(.kind == "Deployment" and
       (.metadata.name == "fulfillment-grpc-server" or
        .metadata.name == "fulfillment-console-proxy")) |=
        ((.spec.template.spec.containers[].command[] | select(test("^--token-issuer="))) = strenv(TOKEN_ISSUER_ARG))
' "${manifest}"

issuer_count="$(TOKEN_ISSUER_ARG="${local_token_issuer}" yq eval-all '
    [select(.kind == "Deployment" and
       (.metadata.name == "fulfillment-grpc-server" or
        .metadata.name == "fulfillment-console-proxy"))
     | .spec.template.spec.containers[].command[]
     | select(. == strenv(TOKEN_ISSUER_ARG))] | length
' "${manifest}")"
bad_issuer_count="$(yq eval-all '
    [select(.kind == "Deployment" and
       (.metadata.name == "fulfillment-grpc-server" or
        .metadata.name == "fulfillment-console-proxy"))
     | .spec.template.spec.containers[].command[]
     | select(test("^--token-issuer=.*:8000$"))] | length
' "${manifest}")"

if [[ "${issuer_count}" != 2 || "${bad_issuer_count}" != 0 ]]; then
    printf 'Expected both fulfillment deployments to use %s; found %s corrected and %s stale issuer arguments\n' \
        "${local_token_issuer}" "${issuer_count}" "${bad_issuer_count}" >&2
    exit 1
fi

printf 'Normalized gRPC server and console-proxy token issuer to %s (HTTPS Route port 443)\n' "${issuer_url}"
