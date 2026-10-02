# Deploy the DCM Stack

Deploy the full DCM stack for E2E testing using `scripts/deploy-dcm.sh`.

## Prerequisites

1. **Required tools**: `git`, `podman`, `podman-compose`, `curl`, `jq`
2. **For KubeVirt provider**: `oc` (OCP cluster with CNV installed)
3. **For k8s container provider**: `oc` or `kubectl` (any Kubernetes cluster)
4. **For k8s storage provider**: `oc` or `kubectl` (any Kubernetes cluster)
5. **For k8s network provider**: `oc` or `kubectl` (any Kubernetes cluster)

## Commands

### Default Deploy
```bash
./scripts/deploy-dcm.sh
```

### Deploy a Specific Version
```bash
# Pin all images to an explicit tag (auto-derives release branch)
./scripts/deploy-dcm.sh --version v0.1.0-rc.1

# Auto-resolve the latest semver tag from Quay.io
./scripts/deploy-dcm.sh --version release

# Explicit version with a custom control-plane branch
./scripts/deploy-dcm.sh --version v0.1.0-rc.1 --control-plane-branch my-branch
```

### Deploy from a Different Branch
```bash
./scripts/deploy-dcm.sh --control-plane-branch feature-x
```

### Deploy from a Fork
```bash
./scripts/deploy-dcm.sh --control-plane-repo https://github.com/myfork/control-plane.git
```

### Deploy to a Custom Directory
```bash
./scripts/deploy-dcm.sh --control-plane-dir /tmp/my-dcm-deploy
```

### Deploy with Auto-Cleanup on Failure
```bash
./scripts/deploy-dcm.sh --cleanup-on-failure
```

### Deploy with Authentication Enabled
```bash
# Loads deploy/compose.auth.yaml with the auth profile, starts Keycloak, and enables JWT validation
./scripts/deploy-dcm.sh --auth-enabled

# Equivalent via environment (Jenkins uses this today)
AUTH_DISABLED=false AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm AUTH_JWT_AUDIENCE=dcm-api \
    ./scripts/deploy-dcm.sh

# Tear down auth-enabled stack with the same flag
./scripts/deploy-dcm.sh --auth-enabled --tear-down
```

### Deploy with Environment Agent (preferred for embedded SPs)

Use this when the user wants environment-agent, embedded providers, network SP, or a
single-line OCP bring-up of CP + agent (not Kind). Do **not** also pass the overlapping
standalone SP flags for the same capabilities.

```bash
# Control-plane + agent with embedded container + VM SPs
./scripts/deploy-dcm.sh --with-environment-agent \
    --agent-embedded-sps container,vm \
    --kubeconfig ~/.kube/config

# Network SP (preferred path; not the legacy standalone flag)
./scripts/deploy-dcm.sh --with-environment-agent \
    --agent-embedded-sps network \
    --kubeconfig ~/.kube/config

# ACM cluster SP embedded in the agent (optional --deploy-acm first)
./scripts/deploy-dcm.sh --with-environment-agent \
    --agent-embedded-sps cluster \
    --deploy-acm \
    --kubeconfig ~/.kube/config

# Optional: override host agent port (default 8081; clashes with standalone KubeVirt)
./scripts/deploy-dcm.sh --with-environment-agent \
    --agent-embedded-sps container \
    --agent-port 9090 \
    --kubeconfig ~/.kube/config

# Optional: set embedded list via env instead of --agent-embedded-sps
AGENT_EMBEDDED_SPS=container,vm ./scripts/deploy-dcm.sh --with-environment-agent \
    --kubeconfig ~/.kube/config
```

Valid embedded tokens: `container`, `vm`, `cluster`, `storage`, `network`.
Kubeconfig is required (mounted via `AGENT_KUBECONFIG_HOST`). Agent health:
`http://localhost:${AGENT_PORT}/api/v1alpha1/health`. Embedding `cluster` needs
`SP_PULL_SECRET` / `ACM_CLUSTER_SP_PULL_SECRET` (auto-resolved from
`openshift-config/pull-secret` when unset) and `SP_CLUSTER_NAMESPACE` (default
`clusters`).

### Deploy with k8s Container Service Provider
```bash
# Auto-detects cluster from existing oc/kubectl session
./scripts/deploy-dcm.sh --k8s-container-service-provider

# With explicit kubeconfig
./scripts/deploy-dcm.sh --k8s-container-service-provider --kubeconfig ~/.kube/config
```

### Deploy with k8s Storage Service Provider
```bash
# Requires control-plane compose with the storage profile (host port 8089)
./scripts/deploy-dcm.sh --k8s-storage-service-provider --kubeconfig ~/.kube/config
```

### Deploy with k8s Network Service Provider (legacy)

> **Prefer environment agent:** use `--with-environment-agent --agent-embedded-sps network`
> (see section above, agent `deploy/DEPLOY.md`, and
> `test-plans/FLPATH-3227-k8s-network-sp.md`). Auth stays off with control-plane
> default `AUTH_DISABLED=true`.

```bash
# LEGACY — standalone Quay image path (often unavailable; FLPATH-4881 obsolete)
./scripts/deploy-dcm.sh --k8s-network-service-provider --kubeconfig ~/.kube/config
```

### Deploy with KubeVirt Service Provider
```bash
./scripts/deploy-dcm.sh --kubevirt-service-provider --kubeconfig ~/.kube/config
```

### Deploy with All Service Providers
```bash
./scripts/deploy-dcm.sh --all-service-providers --kubeconfig ~/.kube/config
```

### Deploy with oc login Credentials
```bash
./scripts/deploy-dcm.sh --all-service-providers \
    --cluster-api https://api.cluster.example.com \
    --cluster-password mypassword

# Or via environment variables
OPENSHIFT_API=https://api.cluster.example.com \
OPENSHIFT_PASSWORD=mypassword \
./scripts/deploy-dcm.sh --kubevirt-service-provider
```

## Cluster Authentication

When any service provider **or** the environment agent is enabled, the script resolves cluster access in this order:
1. Explicit `--kubeconfig PATH` (or `KUBECONFIG` env var)
2. Existing `oc`/`kubectl` session (auto-detected)
3. `oc login` with `--cluster-api` + `--cluster-password`

## Environment Variable Overrides

| Variable | Flag equivalent |
|----------|----------------|
| `DCM_VERSION` | `--version` |
| `CONTROL_PLANE_REPO` | `--control-plane-repo` |
| `CONTROL_PLANE_BRANCH` | `--control-plane-branch` |
| `CONTROL_PLANE_TMP_DIR` | `--control-plane-dir` |
| `KUBECONFIG` | `--kubeconfig` |
| `KUBEVIRT_VM_NAMESPACE` | `--kubevirt-vm-namespace` |
| `K8S_CONTAINER_SP_NAMESPACE` | `--k8s-container-namespace` |
| `K8S_STORAGE_SP_NAMESPACE` | `--k8s-storage-namespace` |
| `K8S_NETWORK_SP_NAMESPACE` | `--k8s-network-namespace` (legacy standalone network SP) |
| `K8S_NETWORK_SERVICE_PROVIDER_VERSION` | (legacy image tag for network SP compose override) |
| `AGENT_EMBEDDED_SPS` | `--agent-embedded-sps` (required with `--with-environment-agent`) |
| `AGENT_PORT` | `--agent-port` (default `8081`) |
| `ENVIRONMENT_AGENT_VERSION` | Image tag for environment-agent |
| `SP_PULL_SECRET` / `ACM_CLUSTER_SP_PULL_SECRET` | Pull secret for embedded `cluster` SP |
| `SP_CLUSTER_NAMESPACE` | Namespace for embedded cluster SP (default `clusters`) |
| `OPENSHIFT_API` | `--cluster-api` |
| `OPENSHIFT_USERNAME` | `--cluster-username` |
| `OPENSHIFT_PASSWORD` | `--cluster-password` |
| `AUTH_DISABLED` | Set to `false` to enable auth (same as `--auth-enabled`) |

Flags take precedence over environment variables.

## Compose Credentials

After cloning control-plane, the script creates `deploy/.env` from `deploy/.env.example` if missing. Database and optional auth credentials are written there for compose `env_file: .env` services. Lab defaults match control-plane's `.env.example`; override via shell env vars before running the script.

## What Happens

1. Clones control-plane and uses `deploy/compose.yaml`; auth mode also loads `deploy/compose.auth.yaml` with the `auth` profile; agent mode adds `--profile environment-agent`
2. Bootstraps `deploy/.env` with DB credentials (and auth / agent credentials when enabled)
3. Runs `podman-compose up -d` (single bring-up for CP + optional agent)
4. Verifies all containers are running
5. Polls control-plane `/api/v1alpha1/health` (90s timeout); with agent, also polls agent health on `:8081` (or `--agent-port`)
6. Resolves container images to git commit SHAs via Quay.io API
7. Writes `dcm-versions.json`

## Output

- Stack available at `http://localhost:8080`
- With `--with-environment-agent`: agent API at `http://localhost:8081` (or `--agent-port`)
- Version info written to `dcm-versions.json`
