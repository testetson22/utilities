#!/usr/bin/env bash

validate_osac_ca_cert() {
    local cert_file="${1:-}"
    local basic_constraints

    if [[ -z "${cert_file}" ]]; then
        printf 'ERROR: OSAC_CA_CERT_FILE is not set\n' >&2
        return 1
    fi
    if [[ ! -f "${cert_file}" ]]; then
        printf 'ERROR: OSAC CA certificate is not a regular file: %s\n' "${cert_file}" >&2
        return 1
    fi
    if [[ ! -r "${cert_file}" ]]; then
        printf 'ERROR: OSAC CA certificate is not readable: %s\n' "${cert_file}" >&2
        return 1
    fi
    if [[ ! -s "${cert_file}" ]]; then
        printf 'ERROR: OSAC CA certificate is empty: %s\n' "${cert_file}" >&2
        return 1
    fi
    if ! command -v openssl >/dev/null 2>&1; then
        printf 'ERROR: openssl is required to validate OSAC_CA_CERT_FILE\n' >&2
        return 1
    fi
    if ! openssl x509 -in "${cert_file}" -noout >/dev/null 2>&1; then
        printf 'ERROR: OSAC CA certificate is not a valid PEM X.509 certificate: %s\n' "${cert_file}" >&2
        return 1
    fi
    if ! basic_constraints="$(openssl x509 -in "${cert_file}" -noout -text 2>/dev/null)" || \
        [[ "${basic_constraints}" != *"CA:TRUE"* ]]; then
        printf 'ERROR: OSAC certificate does not declare CA:TRUE: %s\n' "${cert_file}" >&2
        return 1
    fi
}
