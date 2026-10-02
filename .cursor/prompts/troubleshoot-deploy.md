# Troubleshoot DCM Deployment

Diagnose and fix issues with the DCM stack deployment.

## Required Information

Please provide:
1. **Error output**: Paste the deploy script output or error message
2. **Deploy directory**: Which directory was used? (default: `/tmp/dcm-e2e`)
3. **Flags used**: Any non-default flags (e.g., `--kubevirt-service-provider`)

## Common Failure Patterns

### "Missing required tools"
Install the missing tools listed in the error:
```bash
# macOS
brew install podman podman-compose curl jq

# Fedora/RHEL
sudo dnf install podman podman-compose curl jq
```

### "No cluster credentials found"
The script could not resolve cluster auth. Try one of:
```bash
# Option 1: Pass kubeconfig explicitly
./scripts/deploy-dcm.sh --kubevirt-service-provider --kubeconfig ~/.kube/config

# Option 2: Log in first, then deploy (session auto-detected)
oc login https://api.cluster.example.com --username=kubeadmin --password=...
./scripts/deploy-dcm.sh --kubevirt-service-provider

# Option 3: Pass credentials inline
./scripts/deploy-dcm.sh --kubevirt-service-provider \
    --cluster-api https://api.cluster.example.com --cluster-password secret
```

### "Cannot connect to cluster" (provider validation)
```bash
# Verify kubeconfig
oc --kubeconfig ~/.kube/config cluster-info

# Or with kubectl
kubectl --kubeconfig ~/.kube/config cluster-info

# Check CNV is installed (kubevirt only)
oc get crd virtualmachines.kubevirt.io
```

### Containers fail to start
```bash
# Check container status
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml ps

# View logs for a failing service in the default model
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml logs --tail=50 <service-name>

# For an auth-enabled deployment, include the auth override and profile
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml -f /tmp/dcm-e2e/deploy/compose.auth.yaml --profile auth ps
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml -f /tmp/dcm-e2e/deploy/compose.auth.yaml --profile auth logs --tail=50 keycloak

# Check for port conflicts
podman ps --format '{{.Ports}}' | grep 8080
```

### Health check timeouts
```bash
# Manual health check
curl -v http://localhost:8080/api/v1alpha1/health

# Check control-plane logs
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml logs --tail=50 control-plane
```

### Compose file not found
```bash
# Verify clone worked
ls -la /tmp/dcm-e2e/deploy/compose.yaml

# Re-deploy (cleans and re-clones)
./scripts/deploy-dcm.sh
```

### Port already in use
```bash
# Find what's using port 8080
lsof -i :8080

# Tear down and redeploy
./scripts/deploy-dcm.sh --tear-down
./scripts/deploy-dcm.sh
```

## Diagnostic Commands

```bash
# All container status
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml ps

# Recent container logs (all services)
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml logs --tail=20

# Specific service logs in the default model
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml logs --tail=50 <service>

# Auth-enabled model, including Keycloak
podman-compose -f /tmp/dcm-e2e/deploy/compose.yaml -f /tmp/dcm-e2e/deploy/compose.auth.yaml --profile auth logs --tail=50 keycloak

# Container resource usage
podman stats --no-stream

# Network inspection
podman network inspect dcm-e2e_default
```

## Nuclear Option: Full Reset

```bash
# Tear down everything
./scripts/deploy-dcm.sh --tear-down

# Also clean podman state if needed
podman system prune -f

# Re-deploy
./scripts/deploy-dcm.sh
```
