#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/osac-ca.sh
source "${SCRIPT_DIR}/../scripts/osac-ca.sh"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${tmp_dir}/key.pem" \
    -out "${tmp_dir}/valid-ca.pem" \
    -days 1 \
    -subj "/CN=OSAC test CA" \
    >/dev/null 2>&1
cat > "${tmp_dir}/leaf.cnf" <<'EOF'
[req]
prompt = no
distinguished_name = dn
x509_extensions = leaf

[dn]
CN = OSAC test leaf

[leaf]
basicConstraints = critical,CA:FALSE
EOF
openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${tmp_dir}/leaf-key.pem" \
    -out "${tmp_dir}/leaf.pem" \
    -days 1 \
    -config "${tmp_dir}/leaf.cnf" \
    >/dev/null 2>&1

if ! validate_osac_ca_cert "${tmp_dir}/valid-ca.pem"; then
    printf 'FAIL: valid certificate was rejected\n' >&2
    exit 1
fi

printf 'not a certificate\n' > "${tmp_dir}/invalid.pem"
: > "${tmp_dir}/empty.pem"
mkdir "${tmp_dir}/directory.pem"

for invalid_path in \
    "${tmp_dir}/missing.pem" \
    "${tmp_dir}/directory.pem" \
    "${tmp_dir}/empty.pem" \
    "${tmp_dir}/invalid.pem" \
    "${tmp_dir}/leaf.pem"; do
    if validate_osac_ca_cert "${invalid_path}" >/dev/null 2>&1; then
        printf 'FAIL: invalid CA path was accepted: %s\n' "${invalid_path}" >&2
        exit 1
    fi
done

printf 'OSAC CA certificate validation tests passed\n'
