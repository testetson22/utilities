#!/usr/bin/env bash
# osac-port-forward.sh — Persistent oc port-forward for OSAC backend services.
#
# Registers auto-restart loops as macOS launchd agents so they survive terminal
# exits and cross-shell-session boundaries (plain nohup/disown processes are
# subject to Cursor terminal process-group cleanup). Each loop restarts
# oc port-forward automatically when the OCP API server WebSocket drops.
#
# Ports forwarded:
#   8443  → svc/ffs-keycloak:8443             (Keycloak OIDC)
#   19443 → svc/fulfillment-grpc-server:8000  (gRPC backend)
#
# Listening address: 127.0.0.1 — sufficient for Podman containers on macOS.
# On macOS, Podman runs inside a VM managed by gvproxy.  When a container
# connects to host.containers.internal (192.168.127.254:PORT), gvproxy
# transparently forwards that connection to 127.0.0.1:PORT on the Mac host.
# Confirmed by runtime probe: `nc -z host.containers.internal PORT` from
# an Alpine container succeeds when only 127.0.0.1 is bound on the Mac.
#
# Usage:
#   scripts/osac-port-forward.sh [--namespace <ns>]
#   scripts/osac-port-forward.sh --stop [--namespace <ns>]
#
# Makefile targets (canonical entry points):
#   make port-forward-osac       — start (launchd-managed)
#   make stop-port-forward-osac  — stop
#
# Requirements: oc logged in (oc_login_auto), macOS launchctl.
set -euo pipefail

NAMESPACE="${OSAC_BACKEND_NAMESPACE:-osac-test-backend}"
STOP=false
ENSURE=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --namespace) NAMESPACE="$2"; shift 2 ;;
        --stop)      STOP=true; shift ;;
        --ensure)    ENSURE=true; shift ;;
        --help)
            echo "Usage: $0 [--namespace <ns>] [--ensure] [--stop]"
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: $0 [--namespace <ns>] [--ensure] [--stop]" >&2
            exit 1
            ;;
    esac
done

if [[ "${STOP}" == true && "${ENSURE}" == true ]]; then
    echo "--ensure and --stop cannot be used together" >&2
    exit 2
fi

port_listening() {
    local port="$1"
    command -v lsof >/dev/null 2>&1 || return 1
    lsof -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | grep -q .
}

wait_for_port() {
    local port="$1"
    for ((attempt = 0; attempt < 10; attempt++)); do
        port_listening "${port}" && return 0
        sleep 1
    done
    return 1
}

# ── Stop path ───────────────────────────────────────────────────────────────

if [[ "${STOP}" == true ]]; then
    # launchctl removal is expected to be a no-op when the agent isn't loaded;
    # suppress error output rather than aborting with set -e.
    launchctl remove com.osac.pf.keycloak 2>/dev/null || true
    launchctl remove com.osac.pf.grpc     2>/dev/null || true
    # Also stop any oc port-forward processes owned by this script's launchd agents.
    # Use exact PIDs from the PID files rather than broad pkill -f so we don't
    # inadvertently stop port-forwards started by unrelated invocations.
    for pidfile in /tmp/pf-ffs-keycloak.pid /tmp/pf-fulfillment-grpc-server.pid; do
        if [[ -f "${pidfile}" ]]; then
            pid=$(cat "${pidfile}")
            if kill -0 "${pid}" 2>/dev/null; then
                kill "${pid}" 2>/dev/null || true
            fi
            rm -f "${pidfile}"
        fi
    done
    echo "[$(date -u '+%H:%M:%S')] OSAC port-forwards stopped (launchd agents removed)"
    exit 0
fi

if [[ "${ENSURE}" == true ]] && port_listening 8443 && port_listening 19443; then
    echo "[$(date -u '+%H:%M:%S')] OSAC port-forwards already listening (8443, 19443)"
    exit 0
fi

# ── Start path ──────────────────────────────────────────────────────────────

OC_BIN="$(command -v oc 2>/dev/null || echo "/usr/local/bin/oc")"
KC_PATH="${KUBECONFIG:-${HOME}/.kube/config}"

# Write the restart-loop scripts with absolute paths (launchd has a minimal PATH).
cat > /tmp/osac-pf-keycloak.sh << EOF
#!/bin/bash
export KUBECONFIG="${KC_PATH}"
while true; do
    "${OC_BIN}" port-forward svc/ffs-keycloak 8443:8443 \\
        -n "${NAMESPACE}" --address 127.0.0.1 >>/tmp/pf-ffs-keycloak.log 2>&1
    sleep 1
done
EOF

cat > /tmp/osac-pf-grpc.sh << EOF
#!/bin/bash
export KUBECONFIG="${KC_PATH}"
while true; do
    "${OC_BIN}" port-forward svc/fulfillment-grpc-server 19443:8000 \\
        -n "${NAMESPACE}" --address 127.0.0.1 >>/tmp/pf-fulfillment-grpc-server.log 2>&1
    sleep 1
done
EOF
chmod +x /tmp/osac-pf-keycloak.sh /tmp/osac-pf-grpc.sh

# Remove any existing launchd agents before registering new ones.
launchctl remove com.osac.pf.keycloak 2>/dev/null || true
launchctl remove com.osac.pf.grpc     2>/dev/null || true

# Stop any oc port-forward processes left behind by a previous invocation.
for pidfile in /tmp/pf-ffs-keycloak.pid /tmp/pf-fulfillment-grpc-server.pid; do
    if [[ -f "${pidfile}" ]]; then
        pid=$(cat "${pidfile}")
        if kill -0 "${pid}" 2>/dev/null; then
            kill "${pid}" 2>/dev/null || true
        fi
        rm -f "${pidfile}"
    fi
done
sleep 1

# Register with launchd — survives terminal exit and shell session boundaries.
launchctl submit -l com.osac.pf.keycloak -- /bin/bash /tmp/osac-pf-keycloak.sh
launchctl submit -l com.osac.pf.grpc     -- /bin/bash /tmp/osac-pf-grpc.sh

# Record the PIDs launchd assigned so stop can clean them specifically.
sleep 2
launchctl list com.osac.pf.keycloak 2>/dev/null \
    | awk -F'"' '/"PID"/{print $4}' > /tmp/pf-ffs-keycloak.pid || true
launchctl list com.osac.pf.grpc 2>/dev/null \
    | awk -F'"' '/"PID"/{print $4}' > /tmp/pf-fulfillment-grpc-server.pid || true

if ! wait_for_port 8443; then
    echo "ERROR: Keycloak port-forward failed to listen on 8443; inspect /tmp/pf-ffs-keycloak.log" >&2
    exit 1
fi
if ! wait_for_port 19443; then
    echo "ERROR: fulfillment gRPC port-forward failed to listen on 19443; inspect /tmp/pf-fulfillment-grpc-server.log" >&2
    exit 1
fi

echo "[$(date -u '+%H:%M:%S')] OSAC port-forward launchd agents registered:"
echo "  com.osac.pf.keycloak  → 127.0.0.1:8443  → svc/ffs-keycloak:8443"
echo "  com.osac.pf.grpc      → 127.0.0.1:19443 → svc/fulfillment-grpc-server:8000"
echo "  Logs: /tmp/pf-ffs-keycloak.log  /tmp/pf-fulfillment-grpc-server.log"
echo "  Stop: make stop-port-forward-osac"
