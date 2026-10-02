# Test Plan: DCM Authentication and Authenticated User-Token Propagation

| Field | Value |
|---|---|
| **Epic** | [FLPATH-3254](https://redhat.atlassian.net/browse/FLPATH-3254) |
| **Author** | Chad Crum |
| **Contributors** | Vlad Kolodny (v1.8 E2E / full-stack gap TCs; v1.9 /providers→/agents migration) |
| **Version** | 2.1 |
| **Last Updated** | 2026-09-28 |
| **Target Release** | DCM 1.0 |
| **Status** | In progress — utilities PR #57 auth matrix validated locally; RHDH UI flow and FLPATH-4622 candidate cases remain pending |

## Description

DCM control-plane authentication adds Keycloak-backed identity to the DCM API. The control-plane validates JWT bearer tokens directly against Keycloak's JWKS endpoint via OIDC discovery (`go-oidc/v3`), with a proxy-header fallback (`X-Auth-Proxy-Secret` + `X-Forwarded-User`) for callers behind an auth proxy. New actors are JIT-provisioned on first login. An admin actor is seeded on startup when `DCM_ADMIN_SUBJECT` is configured. For RHDH, the backend validates the RHDH session and forwards the authenticated user's DCM OIDC token from `X-DCM-OIDC-Token` unchanged as `Authorization: Bearer <token>` to DCM.

This plan validates authentication and authenticated user-token propagation across three deployment configurations: auth disabled (default), JWT-enabled, and proxy-only mode. It also includes Helm chart smoke tests confirming baseline deployment with auth disabled (auth-enabled Helm testing deferred to [FLPATH-4476](https://redhat.atlassian.net/browse/FLPATH-4476)). Authorization, RBAC, tenant isolation, policy enforcement, and access-control testing are explicitly out of scope because authorization is not implemented.

**Scope split:**

| Scope | TCs | Where exercised |
|-------|-----|-----------------|
| Control-plane authentication middleware | TC-01 – TC-31 | Compose + subsystem suite (`control-plane#32`) |
| Helm authentication smoke (auth disabled) | TC-32 – TC-35 | OpenShift/Kubernetes |
| Full-stack authentication and user-token propagation | TC-36 – TC-46 | Utilities E2E + Jenkins (`ENABLE_DCM_AUTH`) |

TC-01–TC-35 remain the original control-plane / Helm plan. TC-36+ cover full-stack gaps (UI/RHDH backend, service providers, CI auth toggle) that the subsystem suite cannot cover. JWT negative cases beyond TC-14/TC-15 (wrong audience, `alg:none`) ❗ should cover in subsystem — see checklist.

### References

- [FLPATH-3254](https://redhat.atlassian.net/browse/FLPATH-3254) - DCM authentication epic
- [FLPATH-4432](https://redhat.atlassian.net/browse/FLPATH-4432) - Implement IDM/IAM user authentication layer (story)
- [control-plane#24](https://github.com/dcm-project/control-plane/pull/24) - feat(auth): add IDM/IAM authentication with in-app JWT validation
- [control-plane#32](https://github.com/dcm-project/control-plane/pull/32) - auth subsystem black-box test suite
- [FLPATH-4478](https://redhat.atlassian.net/browse/FLPATH-4478) - Add DCM authentication E2E tests in utilities repo (Closed/Done; utilities [PR #57](https://github.com/dcm-project/utilities/pull/57) merged)
- [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) — Implement Auth mechanism to the agent (SP→CP auth; QA gate for candidate execution)
- [utilities#57](https://github.com/dcm-project/utilities/pull/57) - Merged authentication utilities coverage
- [flight-path-auto-tests!955](https://gitlab.cee.redhat.com/ocp-edge-qe/flight-path-auto-tests/-/merge_requests/955) - `ENABLE_DCM_AUTH` pipeline parameter
- [enhancements#52](https://github.com/dcm-project/enhancements/pull/52) - Authentication enhancement design

### Acceptance Criteria

- All in-scope P1 (critical) authentication and authenticated user-token-propagation test cases must PASS, **or** be QA-gated with a documented candidate dependency explaining why they have not run yet
- All P2 (important) test cases must PASS or have documented workarounds
- P3 (nice-to-have) failures may be deferred with a tracking Jira issue
- Authorization, RBAC, tenant isolation, policy enforcement, and access-control outcomes are not acceptance criteria: authorization is not implemented and these areas are out of scope.

Known P1 QA gate today: [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) — Implement Auth mechanism to the agent. TC-36 Step 2, TC-37, and TC-38 are not yet executed against its candidate; TC-36 Steps 1 and 3 remain independently runnable.
---

## Environment and Global Setup

### Environment Requirements

- RHEL 9.x host with Podman 5.x and podman-compose
- Root or sudo access on the host
- `git`, `curl`, `jq` available
- `psql` (postgresql-client) on the host, OR use `podman exec <postgres-container> psql` for DB queries
- Ports 8080, 8180, 5432, 4222, 7007 free
- Network access to `quay.io` for pulling container images

> **CI note:** On Ecosystem Jenkins `flightpath-dcm-deploy`, the control-plane host port is remapped **8080 → 9080** to avoid conflicts (FLPATH-4421). Use `http://localhost:9080` for API calls in CI; local `make compose-up` still uses `8080`. Keycloak is published on host port `8180` when auth mode is enabled.

> **⚠️ API change ([control-plane#51](https://github.com/dcm-project/control-plane/pull/51)):** The `/providers` endpoint has been **removed** and replaced by `/agents`. All curl examples in this plan have been updated to use `/api/v1alpha1/catalog-items` (for simple auth verification) or `/api/v1alpha1/agents` (for CRUD operations). TC-08 and TC-34 use the agent API directly.

### Deployment Configurations

Three configurations are used across test cases:

| Config | AUTH_DISABLED | AUTH_ISSUER_URL | AUTH_JWT_AUDIENCE | AUTH_PROXY_SECRET | Use Case |
|--------|-------------|-----------------|-------------------|-------------------|----------|
| **A** | `true` | _(empty)_ | _(empty)_ | `dcm-dev-proxy-secret` | Default, no auth |
| **B** | `false` | `http://keycloak:8080/realms/dcm` | `dcm-api` | `dcm-dev-proxy-secret` | Full auth (JWT + proxy) |
| **C** | `false` | _(empty)_ | _(empty)_ | `dcm-dev-proxy-secret` | Proxy-only, no JWT |

### Keycloak Preconfigured Data

From `deploy/keycloak/realm-export.json`:

| Item | Value |
|------|-------|
| Realm | `dcm` |
| Client (confidential) | `dcm-proxy` (secret: `<DCM_PROXY_SECRET>`, direct access grants + service accounts, audience mapper: `dcm-api`) |
| Client (public) | `dcm-cli` (device auth grant, audience mapper: `dcm-api`) |
| User | `dcm-admin` (UUID: `56deb662-4820-5d83-b828-f4beb11a5fa7`, password: `<DCM_DEV_USER_PASSWORD>`) |
| Access token lifespan | 300s (5 minutes) |

### Global Setup

These steps apply to all test cases. Complete them once before starting.

**Step 1: Clone and checkout the control-plane repository**

```bash
cd /root
git clone https://github.com/dcm-project/control-plane.git dcm-control-plane
cd dcm-control-plane
git checkout flpath-4432-implement-authentication
```

**Expected:** Repository cloned and on the `flpath-4432-implement-authentication` branch.

**Step 2: Start the stack in auth-disabled mode (Config A)**

```bash
make compose-up
```

**Expected:** The default Compose model starts and becomes healthy. Keycloak is not part of this model.

```bash
podman compose -f deploy/compose.yaml ps
```

**Step 3: Verify the health endpoint**

```bash
curl -s http://localhost:8080/api/v1alpha1/health | jq .
```

**Expected:** `{"status":"ok","path":"/api/v1alpha1/health"}`

### Helper: Switch to Config B (Auth Enabled)

```bash
AUTH=true make compose-down
AUTH=true AUTH_DISABLED=false AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm AUTH_JWT_AUDIENCE=dcm-api make compose-up
```

**Expected:** The auth Compose model starts, including Keycloak. Keycloak takes 30-60s to import the realm.

```bash
podman compose -f deploy/compose.yaml -f deploy/compose.auth.yaml --profile auth ps
```

**Step 4: Verify Keycloak OIDC discovery (Config B)**

```bash
curl -sf http://localhost:8180/realms/dcm/.well-known/openid-configuration | jq .issuer
```

**Expected:** `"http://keycloak:8080/realms/dcm"`

> **Issuer alignment:** Keycloak uses `KC_HOSTNAME=http://keycloak:8080` with `KC_HOSTNAME_STRICT=false` (set in `deploy/compose.auth.yaml`) to stamp `iss=http://keycloak:8080/realms/dcm` in all tokens - even when the token endpoint is accessed externally via `localhost:8180`. The control-plane's `AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm` matches this. If you see `iss=http://localhost:8180/realms/dcm` in tokens, verify `KC_HOSTNAME` (not `KC_HOSTNAME_URL`) is set in `deploy/compose.auth.yaml`.

### Helper: Switch to Config C (Proxy-Only)

```bash
AUTH=true make compose-down
AUTH_DISABLED=false make compose-up
```

### Helper: Obtain JWT Tokens

```bash
# Password grant (dcm-admin user)
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)

# Client credentials (service account)
SA_TOKEN=$(curl -s -d 'grant_type=client_credentials&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)

# Inspect token claims
echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{sub, preferred_username, aud, iss}'
```

### Helper: Database Access

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane
```

---

## Test Cases

> **Happy Path Flow:** TC-01 through TC-08 form the smoke test sequence. Run these first to validate the core authentication path end-to-end. If any of these fail, subsequent test cases are likely to fail as well.

### TC-01: Health endpoint bypasses auth when auth is enabled

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `health_test.go`
**Requires:** Global setup, Config B

#### Description

The health endpoint at `/api/v1alpha1/health` must be accessible without authentication headers even when auth is enabled. The middleware checks the URL path before any auth logic.

#### Prerequisites

- Stack running in Config B (auth enabled)

#### Steps

**Step 1: Hit health endpoint with no auth headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' http://localhost:8080/api/v1alpha1/health
```

**Expected:** HTTP 200 with `{"status":"ok","path":"/api/v1alpha1/health"}`.

**Step 2: Confirm a non-health endpoint is rejected without auth**

```bash
curl -s -w '\nHTTP %{http_code}\n' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `{"type":"UNAUTHENTICATED","status":401,"title":"Unauthorized","detail":"missing authentication"}`.

#### Cleanup

None - Config B needed for subsequent test cases.

---

### TC-02: Auth disabled mode - API requests succeed without auth

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (implicit) | catalog / policy / sp subsystem suites (`AUTH_DISABLED=true`)
**Requires:** Global setup, Config A

#### Description

With `AUTH_DISABLED=true` (default compose), all API requests succeed without authentication. `DisabledMiddleware` injects a synthetic `auth-disabled` system actor into every request context.

#### Prerequisites

- Stack running in Config A (`AUTH_DISABLED=true`)

#### Steps

**Step 1: List catalog items without auth headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200 with `{"next_page_token":"","results":[...]}`. No 401 or 403.

**Step 2: Register an agent without auth headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' -X POST http://localhost:8080/api/v1alpha1/agents -H 'Content-Type: application/json' -d '{"name":"auth-disabled-test","environment":"test","service_types":["vm"],"cost":"low","topic_name":"dcm.agent.auth-disabled-test"}'
```

**Expected:** HTTP 201 with agent JSON containing a generated `agent_id`.

#### Cleanup

Switch to Config B for remaining test cases (see Helper: Switch to Config B).

---

### TC-03: Admin actor and identity seeded on startup

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `admin_seed_test.go`
**Requires:** Config B with fresh database

#### Description

When `DCM_ADMIN_SUBJECT` is set, the control-plane creates an admin actor and Keycloak identity binding during startup. The `Seed()` method runs before the HTTP server starts.

#### Prerequisites

- Config B running with fresh database (volumes removed by `AUTH=true make compose-down`)

#### Steps

**Step 1: Verify admin actor in database**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT id, username, type, status FROM actors WHERE username = 'admin';"
```

**Expected:** One row with `username=admin`, `type=human`, `status=active`.

**Step 2: Verify admin identity binding**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT actor_id, auth_provider, external_id FROM actor_identities WHERE external_id = '56deb662-4820-5d83-b828-f4beb11a5fa7';"
```

**Expected:** One row with `auth_provider=keycloak`. The `actor_id` matches the admin actor's `id` from step 1.

#### Cleanup

None - admin actor needed for subsequent test cases.

---

### TC-04: JWT authentication via password grant

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `oidc_test.go`
**Requires:** TC-03

#### Description

Obtain a JWT access token from Keycloak using the Resource Owner Password Credentials grant, then send it as a Bearer token to the control-plane. The control-plane validates the JWT signature via OIDC discovery/JWKS, extracts `sub` and `preferred_username`, and resolves the admin actor.

#### Prerequisites

- Config B running
- Admin actor seeded (TC-03)

#### Steps

**Step 1: Obtain an access token**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
echo "Token length: ${#TOKEN}"
```

**Expected:** Token length > 100 (a valid JWT string).

**Step 2: Inspect token claims**

```bash
echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{sub, preferred_username, aud, iss}'
```

**Expected:** `sub` = `56deb662-4820-5d83-b828-f4beb11a5fa7`, `preferred_username` = `dcm-admin`, `aud` contains `dcm-api`, `iss` = `http://keycloak:8080/realms/dcm`.

**Step 3: Call API with Bearer token**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200 with catalog items JSON.

#### Cleanup

None - token expires naturally after 300s.

---

### TC-05: JWT authentication via client_credentials grant

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `oidc_test.go`
**Requires:** TC-03

#### Description

Validates service-to-service authentication using client_credentials grant. The control-plane JIT-provisions a service account actor on first use.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Obtain a service account token**

```bash
SA_TOKEN=$(curl -s -d 'grant_type=client_credentials&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
echo "Token length: ${#SA_TOKEN}"
```

**Expected:** Token length > 100.

**Step 2: Inspect service account claims**

```bash
echo "$SA_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{sub, preferred_username, aud}'
```

**Expected:** `sub` is a UUID, `preferred_username` starts with `service-account-dcm-proxy`, `aud` contains `dcm-api`.

**Step 3: Call API with service account token**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $SA_TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

**Step 4: Verify JIT-provisioned service account in DB**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT username, type, status FROM actors WHERE username LIKE 'service-account%';"
```

**Expected:** One row with `username=service-account-dcm-proxy`, `type=human`, `status=active`.

> **Known limitation:** Service accounts are typed as `human` - JIT provisioning always sets `type=human`. Tracked as [FLPATH-4483](https://redhat.atlassian.net/browse/FLPATH-4483).

#### Cleanup

None.

---

### TC-06: JIT provisioning of new Keycloak user

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `jit_test.go`, `oidc_test.go`
**Requires:** TC-04

#### Description

A brand-new Keycloak user is automatically provisioned as a DCM actor on their first API call. JIT provisioning creates an actor row and identity binding in a single transaction.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Get Keycloak admin token**

```bash
KC_ADMIN_TOKEN=$(curl -s -d 'grant_type=password&client_id=admin-cli&username=admin&password=<KEYCLOAK_ADMIN_PASSWORD>' http://localhost:8180/realms/master/protocol/openid-connect/token | jq -r .access_token)
```

**Expected:** Token obtained.

**Step 2: Create a new user in the dcm realm**

```bash
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -X POST http://localhost:8180/admin/realms/dcm/users -H "Authorization: Bearer $KC_ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"username":"jit-test-user","enabled":true,"firstName":"JIT","lastName":"Test","email":"jit-test@example.com","emailVerified":true,"credentials":[{"type":"password","value":"<TEST_USER_PASSWORD>","temporary":false}]}'
```

**Expected:** HTTP 201.

> **Note:** Keycloak 26 Declarative User Profile requires `firstName`, `lastName`, and `email` with `emailVerified: true`. Without `email` and `emailVerified`, the password grant returns `"Account is not fully set up"` (invalid_grant).

**Step 3: Obtain a token for the new user**

```bash
JIT_TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=jit-test-user&password=<TEST_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
```

**Expected:** Token obtained.

**Step 4: First API call triggers JIT provisioning**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $JIT_TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

**Step 5: Verify actor created in database**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT username, type, status FROM actors WHERE username = 'jit-test-user';"
```

**Expected:** One row: `username=jit-test-user`, `type=human`, `status=active`.

**Step 6: Verify identity binding**

```bash
JIT_SUB=$(echo "$JIT_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq -r .sub)
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT auth_provider, external_id FROM actor_identities WHERE external_id = '$JIT_SUB';"
```

**Expected:** One row with `auth_provider=keycloak`, `external_id` matching the user's `sub` UUID.

#### Cleanup

None - jit-test-user needed for TC-19 and TC-20.

---

### TC-07: Proxy header authentication

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `proxy_secret_test.go`
**Requires:** TC-03

#### Description

Validates the proxy-header fallback auth path. A valid `X-Auth-Proxy-Secret` causes the middleware to accept `X-Forwarded-User` as the subject identifier.

#### Prerequisites

- Config B running (both JWT and proxy paths active)

#### Steps

**Step 1: Send request with valid proxy headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: 56deb662-4820-5d83-b828-f4beb11a5fa7' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Admin actor resolved from `X-Forwarded-User`.

**Step 2: Verify preferred username header is propagated**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: 56deb662-4820-5d83-b828-f4beb11a5fa7' -H 'X-Forwarded-Preferred-Username: dcm-admin' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

#### Cleanup

None.

---

### TC-08: Agent CRUD via control-plane API with JWT auth

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `provider_crud_test.go` → `agent_crud_test.go`
**Requires:** TC-04

> **History:** Originally "Provider CRUD" — [control-plane#51](https://github.com/dcm-project/control-plane/pull/51) replaced the `/providers` API with `/agents` (agent-based architecture over NATS). Updated to target the agent registration/list/get lifecycle.

#### Description

Validates authentication through the **control-plane API** only (JWT on agent register / list / get via `curl`). Proves the auth middleware correctly gates the agent management endpoints under Config B. No full-stack instance path (that is TC-36).

#### Prerequisites

- Config B running

#### Steps

**Step 1: Obtain a token**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
```

**Expected:** Token obtained.

**Step 2: Register an agent**

```bash
curl -s -w '\nHTTP %{http_code}\n' -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"tc08-crud-agent","environment":"test","service_types":["vm","container"],"cost":"low","topic_name":"dcm.agent.tc08-crud-agent"}' \
  http://localhost:8080/api/v1alpha1/agents
```

**Expected:** HTTP 201 (first run) or HTTP 200 (re-run — registration is idempotent by name). Response includes `agent_id`.

**Step 3: List agents**

```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/agents | jq '.agents[].name'
```

**Expected:** Output includes `"tc08-crud-agent"`.

**Step 4: Get agent by ID**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/agents/<agent_id-from-step-2>
```

**Expected:** HTTP 200 with matching agent.

**Step 5: Re-register agent (idempotent update)**

```bash
curl -s -w '\nHTTP %{http_code}\n' -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"tc08-crud-agent","environment":"test-updated","service_types":["vm","container"],"cost":"medium","topic_name":"dcm.agent.tc08-crud-agent"}' \
  http://localhost:8080/api/v1alpha1/agents
```

**Expected:** HTTP 200 (not 201) — same `agent_id`, updated fields.

**Step 6: Unauthenticated register must fail**

```bash
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -X POST -H 'Content-Type: application/json' \
  -d '{"name":"unauth-agent","environment":"test","service_types":["vm"],"cost":"low","topic_name":"dcm.agent.unauth-agent"}' \
  http://localhost:8080/api/v1alpha1/agents
```

**Expected:** HTTP 401.

#### Cleanup

No DELETE endpoint for agents (FK constraints preserve resource→agent history). For manual testers:

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "DELETE FROM agents WHERE name LIKE 'tc08-crud-%';"
```

Alternatively, `AUTH=true make compose-down` tears down the auth-enabled Compose model and destroys the Postgres volume and all data.

---

### TC-09: JIT provisioning uses preferred_username for actor username

**Priority:** P2 (important)
**Type:** Functional
**Method:** Automated (subsystem) | `jit_test.go`
**Requires:** TC-06

#### Description

When `preferred_username` is present in JWT claims or the `X-Forwarded-Preferred-Username` header, the JIT-provisioned actor's username is set from it. Without it, the username falls back to the external ID (UUID).

#### Prerequisites

- Config B running

#### Steps

**Step 1: Verify jit-test-user from TC-06 used preferred_username**

```bash
JIT_SUB=$(echo "$JIT_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq -r .sub)
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT username FROM actors a JOIN actor_identities ai ON a.id = ai.actor_id WHERE ai.external_id = '$JIT_SUB';"
```

**Expected:** `username = jit-test-user` (from `preferred_username` claim), NOT the raw UUID.

#### Cleanup

None.

---

### TC-10: JIT provisioning falls back to externalID when preferred_username is absent

**Priority:** P2 (important)
**Type:** Functional
**Method:** Automated (subsystem) | `jit_test.go`
**Requires:** TC-03, Config B

#### Description

When no `preferred_username` is available (proxy path without the preferred username header), the actor's username is set to the external ID.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Send request via proxy headers without preferred_username**

```bash
NEW_UUID=$(uuidgen)
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H "X-Forwarded-User: $NEW_UUID" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Actor JIT-provisioned.

**Step 2: Verify username equals the UUID**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT username FROM actors a JOIN actor_identities ai ON a.id = ai.actor_id WHERE ai.external_id = '$NEW_UUID';"
```

**Expected:** `username` equals `$NEW_UUID`.

#### Cleanup

None.

---

### TC-11: Second request for same user skips re-provisioning

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `jit_test.go`
**Requires:** TC-06

#### Description

After JIT provisioning on first login, subsequent requests for the same user resolve from cache or DB lookup without creating duplicate records.

#### Prerequisites

- Config B running, jit-test-user already provisioned (TC-06)

#### Steps

**Step 1: Send a second request for jit-test-user**

```bash
JIT_TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=jit-test-user&password=<TEST_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $JIT_TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

**Step 2: Verify still exactly one actor**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT count(*) FROM actors WHERE username = 'jit-test-user';"
```

**Expected:** Count = 1.

#### Cleanup

None.

---

### TC-11b: Username collision during JIT provisioning returns 409

**Priority:** P2 (important)
**Type:** Negative
**Method:** Automated (subsystem) | `race_test.go`
**Requires:** TC-07

#### Description

When two different external identities attempt to JIT-provision with the same `preferred_username`, the first succeeds and the second receives HTTP 409 Conflict. This validates the unique username constraint and the `ErrUsernameConflict` error path in `service.go`.

The proxy-header path is used here because it allows independent control of subject UUID and preferred_username without Keycloak user manipulation.

#### Prerequisites

- Config B running

#### Steps

**Step 1: JIT-provision user A with a shared username via proxy headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: aaaaaaaa-1111-1111-1111-aaaaaaaaaaaa' -H 'X-Forwarded-Preferred-Username: collision-test-user' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Actor created with `username=collision-test-user`, `external_id=aaaaaaaa-1111-...`.

**Step 2: Verify user A was provisioned**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT id, username, type FROM actors WHERE username = 'collision-test-user';"
```

**Expected:** Exactly one row.

**Step 3: JIT-provision user B with the same username but different external ID**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: bbbbbbbb-2222-2222-2222-bbbbbbbbbbbb' -H 'X-Forwarded-Preferred-Username: collision-test-user' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 409 with `"detail":"username already in use by another account"`.

**Step 4: Verify no duplicate actors**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT count(*) FROM actors WHERE username = 'collision-test-user';"
```

**Expected:** Count = 1 (only user A).

#### Cleanup

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "DELETE FROM actor_identities WHERE external_id = 'aaaaaaaa-1111-1111-1111-aaaaaaaaaaaa'; DELETE FROM actors WHERE username = 'collision-test-user';"
```

---

### TC-12: Bearer token takes precedence over proxy headers

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `proxy_secret_test.go`
**Requires:** TC-04

#### Description

When both `Authorization: Bearer` and `X-Auth-Proxy-Secret` + `X-Forwarded-User` headers are present, the JWT path takes precedence. The proxy headers are not evaluated.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Send request with both Bearer and proxy headers pointing to different identities**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: different-subject' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Identity comes from JWT claims (`sub = 56deb662-...`), not proxy headers.

#### Cleanup

None.

---

### TC-12b: Invalid JWT with valid proxy headers returns 401 (no fallback)

**Priority:** P1 (critical)
**Type:** Security
**Method:** Automated (subsystem) | `proxy_secret_test.go`
**Requires:** TC-04

#### Description

When a JWTValidator is configured (Config B) and a request sends an invalid Bearer token alongside valid proxy headers, the middleware must return 401 - not fall through to the proxy path. This prevents a bad JWT from being silently ignored when proxy headers are present.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Send invalid JWT with valid proxy headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'Authorization: Bearer <INVALID_JWT>' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: 56deb662-4820-5d83-b828-f4beb11a5fa7' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `"detail":"invalid bearer token"`. The proxy headers are NOT evaluated.

#### Cleanup

None.

---

### TC-13: No JWTValidator falls through to proxy path

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** Config C

#### Description

When `AUTH_ISSUER_URL` is not set (no JWTValidator configured), Bearer tokens are ignored and the proxy-header path is used instead.

#### Prerequisites

- Stack running in Config C (proxy-only, no JWT)

#### Steps

**Step 1: Send request with Bearer token and valid proxy headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'Authorization: Bearer <INVALID_JWT>' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: 56deb662-4820-5d83-b828-f4beb11a5fa7' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Bearer token ignored (no validator). Identity from proxy headers.

#### Cleanup

Switch back to Config B if continuing with other test cases.

---

### TC-14: Expired JWT rejected

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Manual (candidate for utilities E2E)
**Requires:** TC-04, Config B

#### Description

An expired JWT token is rejected with 401. Keycloak access tokens have a 300s (5 minute) lifespan by default.

> **Faster alternative:** Temporarily set the realm access-token lifespan to 30s in Keycloak Admin (`Realm settings → Tokens → Access Token Lifespan`), obtain a token, wait 35s, then restore 300s. Prefer this over a 310s sleep in automation.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Obtain a token and wait for expiration**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
echo "Waiting 310s for token to expire..."
sleep 310
```

**Step 2: Send request with expired token**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `"detail":"invalid bearer token"`.

#### Cleanup

None.

---

### TC-15: Forged JWT rejected

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Automated (subsystem) | `oidc_test.go`
**Requires:** Config B

#### Description

A JWT with an invalid signature is rejected. The control-plane verifies signatures against Keycloak's JWKS endpoint.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Send request with forged Bearer token**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'Authorization: Bearer <FORGED_JWT>' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `"detail":"invalid bearer token"`.

#### Cleanup

None.

---

### TC-16: Missing auth headers returns 401

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Automated (subsystem) | `proxy_secret_test.go`
**Requires:** Config B

#### Description

A request with no auth headers (no Bearer, no proxy secret) is rejected with 401.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Send request with no auth headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `{"type":"UNAUTHENTICATED","status":401,"title":"Unauthorized","detail":"missing authentication"}`.

**Step 2: Verify WWW-Authenticate and Content-Type headers**

```bash
curl -s -D- -o /dev/null http://localhost:8080/api/v1alpha1/catalog-items 2>/dev/null | grep -iE 'content-type|www-authenticate'
```

**Expected:** `Content-Type: application/problem+json` and `WWW-Authenticate: Bearer`.

#### Cleanup

None.

---

### TC-17: Wrong proxy secret returns 401

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Automated (subsystem) | `proxy_secret_test.go`
**Requires:** Config B or Config C

#### Description

A request with an incorrect `X-Auth-Proxy-Secret` is rejected. The comparison uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.

#### Prerequisites

- Config B or Config C running

#### Steps

**Step 1: Send request with wrong proxy secret**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: wrong-secret' -H 'X-Forwarded-User: 56deb662-4820-5d83-b828-f4beb11a5fa7' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `"detail":"invalid proxy secret"`.

#### Cleanup

None.

---

### TC-18: Missing subject identifier returns 401

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Automated (subsystem) | `proxy_secret_test.go`
**Requires:** Config C

#### Description

Valid proxy secret with missing or empty `X-Forwarded-User` is rejected.

#### Prerequisites

- Config C running (or Config B)

#### Steps

**Step 1: Valid proxy secret, no user header**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `"detail":"missing subject identifier"`.

**Step 2: Valid proxy secret, empty user header**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H 'X-Forwarded-User: ' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 401 with `"detail":"missing subject identifier"`.

#### Cleanup

None.

---

### TC-19: Suspended actor receives 403

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Automated (subsystem) | `status_test.go`
**Requires:** TC-06

#### Description

An actor with status `suspended` receives 403 Forbidden with an RFC 7807 error body.

#### Prerequisites

- Config B running
- `jit-test-user` actor exists (TC-06)

#### Steps

**Step 1: Suspend the actor**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "UPDATE actors SET status = 'suspended' WHERE username = 'jit-test-user';"
```

**Expected:** `UPDATE 1`.

**Step 2: Clear cache by restarting control-plane**

```bash
podman restart $(podman ps --filter 'label=com.docker.compose.service=control-plane' --format '{{.Names}}')
sleep 5
```

**Step 3: Obtain token and attempt API call**

```bash
JIT_TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=jit-test-user&password=<TEST_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $JIT_TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 403 with `{"type":"PERMISSION_DENIED","status":403,"title":"Forbidden","detail":"account suspended"}`.

**Step 4: Reactivate the actor**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "UPDATE actors SET status = 'active' WHERE username = 'jit-test-user';"
```

**Expected:** `UPDATE 1`.

#### Cleanup

Restart control-plane to clear stale cache:

```bash
podman restart $(podman ps --filter 'label=com.docker.compose.service=control-plane' --format '{{.Names}}')
sleep 5
```

---

### TC-20: Deactivated actor receives 403

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Automated (subsystem) | `status_test.go`
**Requires:** TC-06

#### Description

An actor with status `deactivated` receives 403 Forbidden.

#### Prerequisites

- Config B running
- `jit-test-user` actor exists and is active (TC-19 cleanup)

#### Steps

**Step 1: Deactivate the actor**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "UPDATE actors SET status = 'deactivated' WHERE username = 'jit-test-user';"
```

**Expected:** `UPDATE 1`.

**Step 2: Clear cache and attempt API call**

```bash
podman restart $(podman ps --filter 'label=com.docker.compose.service=control-plane' --format '{{.Names}}')
sleep 5
JIT_TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=jit-test-user&password=<TEST_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $JIT_TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 403 with `"detail":"account deactivated"`.

**Step 3: Reactivate the actor**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "UPDATE actors SET status = 'active' WHERE username = 'jit-test-user';"
podman restart $(podman ps --filter 'label=com.docker.compose.service=control-plane' --format '{{.Names}}')
sleep 5
```

#### Cleanup

Actor reactivated, cache cleared.

---

### TC-21: Reactivated actor succeeds after cache expires

**Priority:** P2 (important)
**Type:** Functional
**Method:** Automated (subsystem) | `status_test.go`
**Requires:** TC-19

#### Description

An actor that was suspended and then reactivated can access the API again after the cache is cleared.

#### Prerequisites

- Config B running
- Actor was suspended in TC-19, reactivated in TC-19 cleanup

#### Steps

**Step 1: Verify reactivated actor can access API**

```bash
JIT_TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=jit-test-user&password=<TEST_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $JIT_TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

#### Cleanup

None.

---

### TC-22: Cached actor bypasses DB lookup

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** TC-04

#### Description

After the first request resolves an actor from the database, subsequent requests serve from the in-memory cache without a DB query.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Send first request (populates cache)**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

**Step 2: Send second request immediately (should hit cache)**

```bash
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Control-plane logs should not show a DB query for actor resolution on this request.

#### Cleanup

None.

---

### TC-23: Cache entry expires after TTL

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** TC-04

#### Description

After the cache TTL expires, the next request re-queries the database. If the actor's status changed, the new status is enforced.

#### Prerequisites

- Config B running with `AUTH_CACHE_TTL=5s` for faster testing (requires stack restart)

#### Steps

**Step 1: Restart with short cache TTL**

```bash
AUTH=true make compose-down
AUTH=true AUTH_DISABLED=false AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm AUTH_JWT_AUDIENCE=dcm-api AUTH_CACHE_TTL=5s make compose-up
```

**Step 2: Send first request to cache the actor**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

**Step 3: Wait for cache expiry and re-request**

```bash
sleep 6
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Cache miss triggers fresh DB lookup.

#### Cleanup

Restart with default TTL:

```bash
AUTH=true make compose-down
AUTH=true AUTH_DISABLED=false AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm AUTH_JWT_AUDIENCE=dcm-api make compose-up
```

---

### TC-24: Concurrent first login - same user

**Priority:** P1 (critical)
**Type:** Edge Case
**Method:** Automated (subsystem) | `race_test.go`
**Requires:** TC-03, Config B

#### Description

When multiple concurrent requests arrive for a new user, only one actor and one identity row are created. The race-condition retry path in `provisionActor` handles unique constraint violations.

#### Prerequisites

- Config B running
- A new UUID not yet in the database

#### Steps

**Step 1: Fire 10 concurrent requests for a new user**

```bash
NEW_UUID=$(uuidgen)
seq 10 | xargs -P10 -I{} curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H "X-Forwarded-User: $NEW_UUID" -H 'X-Forwarded-Preferred-Username: race-test-user' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** All 10 requests return HTTP 200.

**Step 2: Verify exactly one actor and identity**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT count(*) FROM actors WHERE username = 'race-test-user';"
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT count(*) FROM actor_identities WHERE external_id = '$NEW_UUID';"
```

**Expected:** Both counts = 1.

#### Cleanup

None.

---

### TC-25: Concurrent first login - different users

**Priority:** P2 (important)
**Type:** Edge Case
**Method:** Manual
**Requires:** TC-03, Config B

#### Description

Multiple new users arriving concurrently each get their own actor with no cross-contamination.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Fire 5 concurrent requests for 5 different users**

```bash
for i in $(seq 5); do
  UUID=$(uuidgen)
  curl -s -o /dev/null -w "User $i: %{http_code}\n" -H 'X-Auth-Proxy-Secret: <AUTH_PROXY_SECRET>' -H "X-Forwarded-User: $UUID" -H "X-Forwarded-Preferred-Username: concurrent-user-$i" http://localhost:8080/api/v1alpha1/catalog-items &
done
wait
```

**Expected:** All 5 return HTTP 200.

**Step 2: Verify 5 distinct actors**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT username FROM actors WHERE username LIKE 'concurrent-user-%' ORDER BY username;"
```

**Expected:** 5 rows: `concurrent-user-1` through `concurrent-user-5`.

#### Cleanup

None.

---

### TC-26: 401 response format is RFC 7807 compliant

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Automated (subsystem) | `error_format_test.go`
**Requires:** Config B

#### Description

All 401 responses use `application/problem+json` content type and contain the required RFC 7807 fields: `type`, `status`, `title`, `detail`. 401 responses also include `WWW-Authenticate: Bearer` per RFC 7235.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Trigger a 401 and validate response structure**

```bash
curl -s -D- http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:**
- Status line: `HTTP/1.1 401 Unauthorized`
- `Content-Type: application/problem+json`
- `WWW-Authenticate: Bearer`
- Body contains: `type` (string), `status` (integer = 401), `title` (= "Unauthorized"), `detail` (string)

#### Cleanup

None.

---

### TC-27: 403 response format for suspended and deactivated actors

**Priority:** P2 (important)
**Type:** Functional
**Method:** Automated (subsystem) | `error_format_test.go`
**Requires:** TC-19, TC-20

#### Description

403 responses for suspended and deactivated actors use `application/problem+json` with `type=PERMISSION_DENIED` and appropriate `detail` messages.

#### Prerequisites

- Verified by TC-19 (suspended) and TC-20 (deactivated) - this TC documents the format expectation.

#### Steps

**Step 1: Verify suspended actor 403 format (from TC-19 step 3 output)**

**Expected:** `{"type":"PERMISSION_DENIED","status":403,"title":"Forbidden","detail":"account suspended"}`

**Step 2: Verify deactivated actor 403 format (from TC-20 step 2 output)**

**Expected:** `{"type":"PERMISSION_DENIED","status":403,"title":"Forbidden","detail":"account deactivated"}`

#### Cleanup

None.

---

### TC-28: Admin seeding is idempotent on restart

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** TC-03

#### Description

Restarting the control-plane does not create duplicate admin actors. The `Seed()` method checks for existing admin actor by username before creating.

#### Prerequisites

- Config B running, admin actor exists (TC-03)

#### Steps

**Step 1: Restart control-plane**

```bash
podman restart $(podman ps --filter 'label=com.docker.compose.service=control-plane' --format '{{.Names}}')
sleep 5
```

**Step 2: Verify no duplicate admin actors**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT count(*) FROM actors WHERE username = 'admin';"
```

**Expected:** Count = 1.

#### Cleanup

None.

---

### TC-29: Admin seeding skipped when DCM_ADMIN_SUBJECT is empty

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** Global setup

#### Description

When `DCM_ADMIN_SUBJECT` is empty, no admin actor is created. The control-plane logs an info message and continues.

> **Note:** The config validator requires `DCM_ADMIN_SUBJECT` when `AUTH_DISABLED=false`. This test case must use `AUTH_DISABLED=true` (Config A) to allow an empty admin subject.

#### Prerequisites

- Stack started with `DCM_ADMIN_SUBJECT=""` and `AUTH_DISABLED=true`

#### Steps

**Step 1: Create compose override to clear admin subject**

The main compose.yaml uses `${DCM_ADMIN_SUBJECT:-56deb662-...}` which substitutes the default when the env var is empty. A compose override file bypasses this by setting the value directly.

```bash
cat > /tmp/no-admin-override.yaml <<'EOF'
services:
  control-plane:
    environment:
      DCM_ADMIN_SUBJECT: ""
EOF
```

**Step 2: Start stack without admin subject**

```bash
AUTH=true make compose-down
COMPOSE_PROJECT_NAME=control-plane podman compose -f deploy/compose.yaml -f /tmp/no-admin-override.yaml up -d
```

> **Note:** Use `COMPOSE_PROJECT_NAME=control-plane` to match the Makefile's naming. The default `AUTH_DISABLED=true` applies since no override is set.

**Step 3: Verify no admin actor**

```bash
PGPASSWORD=<POSTGRES_PASSWORD> psql -h localhost -U <POSTGRES_USER> -d control-plane -c "SELECT count(*) FROM actors WHERE username = 'admin';"
```

**Expected:** Count = 0.

**Step 4: Check control-plane logs for skip message**

```bash
podman logs $(podman ps -a --filter 'label=com.docker.compose.service=control-plane' --format '{{.Names}}') 2>&1 | grep -i "admin"
```

**Expected:** Log contains `DCM_ADMIN_SUBJECT not set, skipping admin actor creation`.

#### Cleanup

Remove override and restart with default settings:

```bash
rm /tmp/no-admin-override.yaml
AUTH=true make compose-down
AUTH=true AUTH_DISABLED=false AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm AUTH_JWT_AUDIENCE=dcm-api make compose-up
```

---

### TC-30: Auth disabled mode ignores garbage auth headers

**Priority:** P2 (important)
**Type:** Negative
**Method:** Manual
**Requires:** Config A

#### Description

When `AUTH_DISABLED=true`, the middleware does not inspect auth headers at all. Even garbage values succeed.

#### Prerequisites

- Config A running

#### Steps

**Step 1: Send request with garbage headers**

```bash
curl -s -w '\nHTTP %{http_code}\n' -H 'X-Auth-Proxy-Secret: garbage' -H 'X-Forwarded-User: garbage' -H 'Authorization: Bearer garbage' http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200. Headers irrelevant when `AUTH_DISABLED=true`.

#### Cleanup

None.

---

### TC-31: Compose stack teardown

**Priority:** P1 (critical)
**Type:** Cleanup
**Method:** Manual
**Requires:** All compose-based TCs (TC-01 through TC-30) complete

#### Description

Remove all compose stack resources and verify clean shutdown.

#### Steps

**Step 1: Stop the stack and remove volumes**

```bash
cd /root/dcm-control-plane
AUTH=true make compose-down
```

**Expected:** All containers stopped, volumes removed.

**Step 2: Verify no containers remain**

```bash
podman ps --filter 'name=control-plane' --format '{{.Names}}'
```

**Expected:** No output.

#### Cleanup

Optional - remove the clone:

```bash
rm -rf /root/dcm-control-plane
```

---

### Helm Chart Smoke Tests (Auth Disabled)

> **Note:** The Helm chart does not set any `AUTH_` environment variables, so the control-plane
> defaults to `AUTH_DISABLED=true`. Auth-enabled Helm deployment is tracked in
> [FLPATH-4476](https://redhat.atlassian.net/browse/FLPATH-4476). These TCs verify baseline
> Helm deployment and API functionality with authentication disabled.

### TC-32: Helm install and pod readiness

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** OpenShift/Kubernetes cluster access

#### Description

Install the DCM Helm chart into a test namespace and verify all pods reach a healthy state.

#### Prerequisites

- `oc` or `kubectl` CLI configured with cluster access
- Helm 3.x installed
- DCM control-plane repo cloned (same branch as compose tests)

#### Steps

**Step 1: Create namespace and install chart**

```bash
oc new-project dcm-helm-test || kubectl create namespace dcm-helm-test
helm install dcm-test deploy/helm/dcm/ --namespace dcm-helm-test
```

**Expected:** Helm release `dcm-test` created with no errors.

**Step 2: Wait for control-plane pod to be ready**

```bash
oc wait --for=condition=ready pod -l app.kubernetes.io/name=control-plane,app.kubernetes.io/instance=dcm-test -n dcm-helm-test --timeout=120s
```

**Expected:** Pod reaches `Ready` within 120 seconds.

**Step 3: Verify all pods are running**

```bash
kubectl get pods -n dcm-helm-test
```

**Expected:** At minimum `control-plane`, `postgres`, and `nats` pods in `Running` state with no restarts.

#### Cleanup

None - chart remains for TC-33.

---

### TC-33: Health endpoint accessible via Helm deployment

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** TC-32

#### Description

The health endpoint is reachable through the Kubernetes service. Confirms the control-plane started successfully with default (auth-disabled) config.

#### Prerequisites

- TC-32 passed, all pods running

#### Steps

**Step 1: Determine the control-plane URL**

For OpenShift with route enabled (default):

```bash
CP_URL=$(oc get route dcm-test-control-plane -n dcm-helm-test -o jsonpath='{.spec.host}')
echo "Control-plane URL: https://$CP_URL"
```

For clusters without routes, use port-forward:

```bash
kubectl port-forward svc/dcm-test-control-plane 8080:8080 -n dcm-helm-test &
PF_PID=$!
CP_URL="localhost:8080"
```

**Step 2: Hit the health endpoint**

For route (TLS):

```bash
curl -sk "https://$CP_URL/api/v1alpha1/health"
```

For port-forward:

```bash
curl -s "http://$CP_URL/api/v1alpha1/health"
```

**Expected:** HTTP 200 with `{"status":"ok","path":"/api/v1alpha1/health"}`.

**Step 3: Verify API responds without auth headers**

```bash
curl -sk "https://$CP_URL/api/v1alpha1/catalog-items"
```

**Expected:** HTTP 200 with `{"next_page_token":"","results":[...]}`. No authentication required because `AUTH_DISABLED` defaults to `true`.

#### Cleanup

If using port-forward, stop it:

```bash
kill $PF_PID 2>/dev/null
```

---

### TC-34: Multi-subsystem CRUD with auth disabled (Helm)

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** TC-33

#### Description

Exercise multiple control-plane subsystems (agents, catalog service-types, catalog items) through the Helm-deployed API with auth disabled. Verifies more than one component is functional end-to-end, not just health.

#### Prerequisites

- TC-33 passed, `CP_URL` determined

#### Steps

**Step 1: Resolve the control-plane URL**

```bash
CP_URL=$(oc get route dcm-test-control-plane -n dcm-helm-test -o jsonpath='{.spec.host}')
SCHEME="https"
```

Or for port-forward:

```bash
kubectl port-forward svc/dcm-test-control-plane 8080:8080 -n dcm-helm-test &
PF_PID=$!
CP_URL="localhost:8080"
SCHEME="http"
```

**Step 2: Register an agent**

```bash
curl -sk -X POST "$SCHEME://$CP_URL/api/v1alpha1/agents" -H 'Content-Type: application/json' -d '{"name":"helm-smoke-agent","environment":"test","service_types":["vm"],"cost":"low","topic_name":"dcm.agent.helm-smoke-agent"}' -w '\nHTTP %{http_code}\n'
```

**Expected:** HTTP 201 (first run) or HTTP 200 (re-run — registration is idempotent by name). Response includes `agent_id`.

**Step 3: List agents**

```bash
curl -sk "$SCHEME://$CP_URL/api/v1alpha1/agents" -w '\nHTTP %{http_code}\n'
```

**Expected:** HTTP 200 with `{"agents":[...]}` containing `helm-smoke-agent`.

**Step 4: List catalog service-types**

```bash
curl -sk "$SCHEME://$CP_URL/api/v1alpha1/service-types" -w '\nHTTP %{http_code}\n'
```

**Expected:** HTTP 200 with an empty list or existing service types. Confirms the catalog subsystem is reachable.

**Step 5: List catalog items**

```bash
curl -sk "$SCHEME://$CP_URL/api/v1alpha1/catalog-items" -w '\nHTTP %{http_code}\n'
```

**Expected:** HTTP 200. Confirms the catalog item subsystem responds independently of agents.

**Step 6: List policies**

```bash
curl -sk "$SCHEME://$CP_URL/api/v1alpha1/policies" -w '\nHTTP %{http_code}\n'
```

**Expected:** HTTP 200. Confirms the policy subsystem is serving requests.

#### Cleanup

Agent remains in DB (no DELETE endpoint). Helm uninstall in TC-35 removes everything.

```bash
kill $PF_PID 2>/dev/null
```

---

### TC-35: Helm uninstall cleans up resources

**Priority:** P1 (critical)
**Type:** Cleanup
**Method:** Manual
**Requires:** TC-34

#### Description

Uninstall the Helm release and verify all Kubernetes resources are removed.

#### Steps

**Step 1: Uninstall the Helm release**

```bash
helm uninstall dcm-test --namespace dcm-helm-test
```

**Expected:** Release `dcm-test` uninstalled.

**Step 2: Wait for pods to terminate**

```bash
kubectl wait --for=delete pod --all -n dcm-helm-test --timeout=60s 2>/dev/null || true
kubectl get pods -n dcm-helm-test
```

**Expected:** No pods remain in the namespace.

**Step 3: Verify PVCs are cleaned up**

```bash
kubectl get pvc -n dcm-helm-test
```

**Expected:** No PVCs remain (or PVCs remain if the chart uses `Retain` - document which).

**Step 4: Delete the test namespace**

```bash
oc delete project dcm-helm-test || kubectl delete namespace dcm-helm-test
```

**Expected:** Namespace deleted.

#### Cleanup

None - this is the Helm cleanup test case.

---

## Full-Stack Authentication and User-Token Propagation Tests (TC-36 – TC-46)

> **Why this section exists:** TC-01–TC-35 validate the control-plane auth middleware (and Helm with auth disabled). They do **not** cover RHDH/DCM-UI backend machine-to-machine auth, service providers registering under auth, instance provisioning under auth, or the Jenkins auth toggle.
>
> **Automation target:** utilities E2E suite + `flightpath-dcm-deploy` with `ENABLE_DCM_AUTH=true`. Subsystem suite cannot cover UI/SPs/CI.
>
> **Scope:** These cases validate authentication and authenticated user-token propagation only. Authorization, RBAC, tenant isolation, policy enforcement, and access-control testing are out of scope because authorization is not implemented.

### Helper: Full stack with auth enabled (Config B + providers)

Use the utilities deploy path or Ecosystem Jenkins `flightpath-dcm-deploy` with:

```bash
# Example env for deploy-dcm.sh / compose
export AUTH_DISABLED=false
export AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm
export AUTH_JWT_AUDIENCE=dcm-api
# Local default (same as TC-01–TC-35). Jenkins remaps control-plane to :9080 — see Environment CI note.
export DCM_API_URL=http://localhost:8080
```

Confirm unauthenticated API fails before running client TCs:

```bash
curl -s -o /dev/null -w 'HTTP %{http_code}\n' "${DCM_API_URL}/api/v1alpha1/catalog-items"
# Expected: 401
# CI: DCM_API_URL=http://localhost:9080
```

> **Port convention:** All curl examples in this section use `http://localhost:8080` (local compose). On Ecosystem Jenkins, substitute `http://localhost:9080` (or set `DCM_API_URL` accordingly).

### Latest validation evidence

The following evidence was collected from the current auth-enabled DCM test environment. It is execution evidence, not a claim that the complete plan has run:

| Test | Result | Evidence |
|---|---|---|
| TC-01 | ✅ Passed | Health endpoint returned HTTP 200 with auth enabled. |
| TC-04 | ✅ Passed | RHBK password-grant login for the configured test user returned a valid access token. |
| TC-16 | ✅ Passed | Protected catalog request without a Bearer token returned HTTP 401. |
| TC-26 | ✅ Passed | The unauthenticated response used the expected `application/problem+json` format. |
| TC-41 | ✅ Passed | Utilities PR #57 DCM API auth matrix ran 2/2 auth specs successfully; 0 failures. This does not cover RHDH UI token forwarding. |

TC-39 and TC-40 have not been marked passed by route availability alone. They require an authenticated RHDH browser session and evidence that the session-associated DCM token is forwarded to DCM.

---

### TC-36: Instance creation under JWT auth (full stack)

**Priority:** P1 (critical) — QA-gated; not yet executed against the [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) — Implement Auth mechanism to the agent candidate
**Type:** Functional
**Method:** Manual | QA-gated (Step 2) | Steps 1 and 3 independently runnable
**Requires:** Config B; Step 2 needs a registered healthy SP from the FLPATH-4622 candidate; Steps 1 and 3 do not

> **❗ QA gate (Step 2):** This step has not yet been executed against an FLPATH-4622 — Implement Auth mechanism to the agent candidate. **Step 1** (token) and **Step 3** (unauthenticated → 401) are independently runnable without SPs.

#### Description

End-to-end path with auth enabled: obtain JWT → create catalog instance → placement → SP provision. Proves auth does not break catalog/placement/SPRM. Negative unauth check is independent of SPs.

#### Prerequisites

- Config B running
- Step 2 only: at least one healthy registered SP, catalog item / service type, placement with `selected_provider` (needs SP auth / FLPATH-4622)
- Steps 1 and 3: Config B only

#### Steps

**Step 1: Obtain token**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' \
  http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
```

**Step 2: Create instance via API** (execute against the FLPATH-4622 candidate)

```bash
curl -s -w '\nHTTP %{http_code}\n' -X POST \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "api_version": "v1alpha1",
    "display_name": "<INSTANCE_NAME>",
    "spec": {
      "catalog_item_id": "<CATALOG_ITEM_UID>",
      "user_values": []
    }
  }' \
  http://localhost:8080/api/v1alpha1/catalog-item-instances
```

**Expected:** HTTP 201. Instance `uid` returned; visible on subsequent authenticated `GET /api/v1alpha1/catalog-item-instances/<uid>`.

**Step 3: Unauthenticated create must fail** (runnable without SPs)

```bash
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -X POST \
  -H 'Content-Type: application/json' \
  -d '{
    "api_version": "v1alpha1",
    "display_name": "<INSTANCE_NAME>",
    "spec": {
      "catalog_item_id": "<CATALOG_ITEM_UID>",
      "user_values": []
    }
  }' \
  http://localhost:8080/api/v1alpha1/catalog-item-instances
```

**Expected:** HTTP 401.

#### Cleanup

Delete the test instance with a valid Bearer token (`DELETE /api/v1alpha1/catalog-item-instances/<uid>`), when Step 2 was run.
---

### TC-37: Service provider registration with auth enabled

**Priority:** P1 (critical) — QA-gated; not yet executed against the [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) — Implement Auth mechanism to the agent candidate
**Type:** Functional
**Method:** Manual | QA-gated
**Requires:** Config B, SP container(s) started, and the FLPATH-4622 candidate SP→CP authentication path

> **❗ QA gate:** Execute this case when the FLPATH-4622 — Implement Auth mechanism to the agent candidate is available. It is not yet executed against that candidate and awaits candidate-based QA.

#### Description

Service providers must successfully register with the control-plane when auth is enabled. Subsystem suite has no SPs.

#### Prerequisites

- Config B running (`AUTH_DISABLED=false`)
- FLPATH-4622 candidate available so SP registration/health calls include valid credentials
- At least one SP image started with registration URL pointing at control-plane

#### Steps

**Step 1: Confirm SP container is running**

```bash
podman ps --format '{{.Names}} {{.Status}}' | grep -E 'k8s-container|kubevirt|acm-cluster|three-tier'
```

**Expected:** SP container(s) Up.

**Step 2: List registered service providers (agents) with admin JWT**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' \
  http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/agents | jq '.agents[].name'
```

**Expected:** Expected SP name(s) present (e.g. `k8s-container-provider`).

#### Cleanup

None.

---

### TC-38: Service provider health remains ready with auth enabled

**Priority:** P1 (critical) — QA-gated; not yet executed against the [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) — Implement Auth mechanism to the agent candidate
**Type:** Functional
**Method:** Manual | QA-gated
**Requires:** TC-37

> **❗ QA gate:** Execute after TC-37 against the FLPATH-4622 — Implement Auth mechanism to the agent candidate. This case is not yet executed against that candidate; it is not permanently blocked.

#### Description

After registration, SP health reported by the control-plane stays ready/healthy under auth. Catches broken SP→CP credential paths.

#### Prerequisites

- TC-37 passed against the FLPATH-4622 candidate

#### Steps

**Step 1: Poll SP health via authenticated agent API**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' \
  http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1alpha1/agents | jq '.agents[] | {name, health_status}'
```

**Expected:** Registered agents report ready/healthy via `health_status` (or documented known-unhealthy exceptions such as ACM SP GVK scheme issues tracked separately).

**Step 2: SP container logs show no repeated 401 to control-plane**

```bash
podman logs <sp-container> 2>&1 | grep -iE '401|unauthorized|UNAUTHENTICATED' | tail -5
```

**Expected:** No sustained auth failures against the control-plane registration/health APIs.

#### Cleanup

None.

---

### TC-39: RHDH backend forwards the authenticated user's DCM OIDC token

**Priority:** P1 (critical)
**Type:** Functional / Integration
**Method:** Manual | Planned utilities/RHDH UI E2E
**Requires:** Config B, RHDH with the DCM plugin, and a valid RHDH user session with a DCM OIDC token

#### Description

Verify the UI request path: **RHDH user login → `X-DCM-OIDC-Token` → backend validates the RHDH session → backend forwards the exact user token as `Authorization: Bearer <token>` to DCM**. This is user-token propagation; client credentials and `dcm-proxy` are not a normal UI-flow substitute.

#### Prerequisites

- Config B running
- RHDH DCM backend configured with the DCM API URL
- A configured RHDH test user can log in through the RHDH route and receives a DCM OIDC token in `X-DCM-OIDC-Token`
- A known DCM provider or catalog fixture is available for the request

> **Current status:** The RHDH route and RHBK test user are available. Execution remains pending the authenticated browser-session step; a direct DCM bearer token is not a substitute for the RHDH session.

#### Steps

**Step 1: Log in to RHDH as the test user and open a DCM-backed view.**

**Expected:** The request reaching the DCM backend contains `X-DCM-OIDC-Token`; the backend accepts the RHDH session before contacting DCM.

**Step 2: Capture redacted request evidence at the backend-to-DCM boundary.**

**Expected:** The outbound request contains `Authorization: Bearer <same-user-token>`. Compare only a safe token fingerprint or decoded non-secret claims; do not record the raw token.

**Step 3: Verify the DCM response reaches the RHDH view.**

**Expected:** The DCM backend response is successful and represents the authenticated RHDH user's request. No replacement service token or client-credentials token is used.

#### Cleanup

Log out the test user and remove any temporary redacted diagnostics.

---

### TC-40: Authenticated user opens `/dcm` and sees known provider/catalog data

**Priority:** P1 (critical)
**Type:** Functional / UI E2E
**Method:** Manual | Planned utilities/RHDH UI E2E
**Requires:** TC-39 and a known provider/catalog fixture

#### Description

One user logs in to RHDH, opens `/dcm`, and sees the known provider or catalog fixture. Anonymous API behavior and authenticated UI success are separate assertions: an anonymous API request must return 401 on an auth-enabled deployment, while the authenticated user flow must succeed.

#### Prerequisites

- TC-39 passed
- Config B running
- One known provider or catalog fixture available

> **Current status:** Pending TC-39 authenticated-session evidence. This case is not blocked by a missing route or missing RHBK user.

#### Steps

**Step 1: Verify anonymous API behavior.**

Call the DCM-backed API without an RHDH session or `Authorization` header.

**Expected:** HTTP 401. This assertion is separate from the authenticated UI flow.

**Step 2: Log in to RHDH as one test user and open `/dcm`.**

**Expected:** RHDH loads the DCM page for the authenticated user.

**Step 3: Verify the known provider or catalog fixture is displayed.**

**Expected:** The fixture is visible and the associated DCM-backed request succeeds through the TC-39 user-token forwarding path.

#### Cleanup

Log out the test user.

---

### TC-41: Utilities authentication matrix via `ENABLE_DCM_AUTH`

**Priority:** P2 (important)
**Type:** Functional / CI
**Method:** Automated coverage merged in [utilities#57](https://github.com/dcm-project/utilities/pull/57); local auth-enabled execution verified; Jenkins execution not yet verified
**Requires:** Utilities PR #57 merged, Keycloak credentials for the DCM API client, and a Jenkins environment that can run `ENABLE_DCM_AUTH`

#### Description

Validates the merged utilities DCM API authentication matrix. It distinguishes the auth-disabled no-token path, the auth-enabled anonymous rejection, and the auth-enabled API request authenticated with a Keycloak-issued Bearer token. The local auth-enabled validation of PR #57 passed 2/2 auth specs with no failures; Jenkins execution remains unverified. This case does not validate RHDH login, RHDH session cookies, `X-DCM-OIDC-Token`, or `/dcm` UI behavior; those remain TC-39 and TC-40.

#### Prerequisites

- Access to Ecosystem Jenkins `flightpath-dcm-deploy` (or private folder copy)
- Host with free ports / existing OCP as required by job

#### Steps

**Step 1: Run with `ENABLE_DCM_AUTH=false` (auth disabled).**

**Expected:** A no-token request succeeds.

**Step 2: Run with `ENABLE_DCM_AUTH=true` as an anonymous caller.**

**Expected:** The anonymous request returns HTTP 401.

**Step 3: Run with `ENABLE_DCM_AUTH=true` using the configured authenticated DCM API client.**

**Expected:** The authenticated DCM API request succeeds with a Bearer token. Preserve redacted token evidence; do not treat this result as evidence of RHDH user-token forwarding, and do not mark Jenkins passed until a Jenkins run is verified.

#### Cleanup

Per job teardown parameters (`SKIP_TEARDOWN`).

---

### TC-42: Keycloak restart baseline resilience

**Priority:** P2 (important)
**Type:** Edge Case
**Method:** Manual | Planned E2E
**Requires:** Config B

#### Description

After a Keycloak restart without an intentional signing-key change, a newly issued token must work. This is a baseline restart smoke; the JWKS and signing-key-rotation matrix is TC-45.

#### Prerequisites

- Config B running

#### Steps

**Step 1: Obtain token and verify API works**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' \
  http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200.

**Step 2: Restart Keycloak**

```bash
podman restart $(podman ps --filter 'label=com.docker.compose.service=keycloak' --format '{{.Names}}')
# wait until realm OIDC discovery is healthy
until curl -sf http://localhost:8180/realms/dcm/.well-known/openid-configuration >/dev/null; do sleep 2; done
```

**Step 3: Obtain a new token and call API**

```bash
TOKEN2=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' \
  http://localhost:8180/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -H "Authorization: Bearer $TOKEN2" \
  http://localhost:8080/api/v1alpha1/catalog-items
```

**Expected:** HTTP 200 with the new token. Document whether the pre-restart `TOKEN` still works (depends on key stability across restart).

#### Cleanup

None.

---

### TC-43: User-token forwarding trust boundary

**Priority:** P1 (critical)
**Type:** Security / Integration
**Method:** Manual | Planned utilities E2E
**Requires:** Config B and RHDH DCM backend access

#### Description

Verify that only a validated RHDH session can cause the backend to forward `X-DCM-OIDC-Token` to DCM. The trust boundary is the RHDH backend: browser-supplied token headers without a valid RHDH session must not be forwarded.

#### Steps

**Step 1: Send a DCM backend request with no RHDH session.**

**Expected:** The request is rejected before DCM token forwarding; no outbound DCM request is made with a user token.

**Step 2: Send a request with a forged or arbitrary `X-DCM-OIDC-Token` header and no valid RHDH session.**

**Expected:** The header is not trusted or forwarded. The response is an authentication failure appropriate to the RHDH backend.

**Step 3: Repeat with a valid RHDH session.**

**Expected:** Only the session-associated DCM OIDC token is forwarded as the Bearer token. Record only redacted/fingerprint evidence.

#### Cleanup

Remove temporary diagnostics and log out the test user.

---

### TC-44: JWT rejection matrix

**Priority:** P1 (critical)
**Type:** Negative / Security
**Method:** Automated where token fixtures are available; otherwise manual
**Requires:** Config B and a known protected DCM endpoint

#### Description

Verify authentication rejection for malformed, expired, invalid-signature, wrong-issuer, and wrong-audience JWTs. This case validates token acceptance only; it does not test authorization or resource access policy.

#### Steps

**Step 1: Send each invalid token category to the protected endpoint:** missing Bearer token, malformed JWT, expired JWT, invalid signature, wrong issuer, and wrong audience.

**Expected:** Each request sent directly to the protected DCM endpoint returns HTTP 401 with the documented problem response. RHDH token-forwarding behavior is covered separately by TC-43.

**Step 2: Send a valid DCM user token.**

**Expected:** Authentication succeeds. This case does not assert authorization, RBAC, tenant isolation, policy enforcement, or access-control behavior.

#### Cleanup

Discard generated invalid-token fixtures and redact any captured claims.

---

### TC-45: JWKS restart and signing-key rotation

**Priority:** P1 (critical)
**Type:** Resilience / Security
**Method:** Manual | Planned utilities E2E
**Requires:** Config B, issuer administration for a non-production test realm, and a protected DCM endpoint

#### Description

Verify that authentication recovers after a Keycloak/JWKS restart and after a signing-key rotation. This extends TC-42 by requiring a real signing-key change and confirmation that newly signed user tokens are accepted.

#### Steps

**Step 1: Record a redacted fingerprint of a valid user token and verify it succeeds.**

**Expected:** The protected endpoint succeeds for the initial valid user token.

**Step 2: Restart the issuer and wait for OIDC discovery and JWKS to be available. Obtain a newly issued user token.**

**Expected:** The newly issued token succeeds.

**Step 3: Rotate the signing key in the non-production realm, publish the updated JWKS, and obtain a newly signed user token.**

**Expected:** The newly signed user token succeeds after the control plane refreshes JWKS. If the old token is no longer accepted, it returns 401; document the observed configured rotation behavior without treating it as an authorization result.

#### Cleanup

Restore the test realm's signing-key configuration according to its approved test-environment procedure.

---

### TC-46: Token redaction in logs and test evidence

**Priority:** P1 (critical)
**Type:** Security / Observability
**Method:** Manual | Planned utilities E2E
**Requires:** Config B, a completed authenticated RHDH-to-DCM request, and access to relevant test/runtime logs

#### Description

Verify that raw `X-DCM-OIDC-Token` and `Authorization: Bearer` values are not emitted in browser diagnostics, RHDH backend logs, DCM logs, Jenkins console output, or saved test artifacts. Token fingerprints and explicitly redacted claims are permitted.

#### Steps

**Step 1: Complete an authenticated RHDH-to-DCM request.**

**Expected:** The request succeeds without exposing the token in the UI.

**Step 2: Inspect relevant RHDH backend logs, DCM logs, Jenkins console output when available, and generated test artifacts.**

**Expected:** No raw JWT or complete Bearer value appears. Any diagnostic token representation is redacted or a non-reversible fingerprint.

**Step 3: Exercise an authentication failure with a malformed token.**

**Expected:** Failure diagnostics identify the token category or error safely and still do not expose the supplied token value.

#### Cleanup

Remove temporary diagnostic artifacts containing more than approved redacted evidence.

---

## Global Teardown

If any test case left resources behind, run the relevant cleanup:

**Compose stack:**

```bash
cd /root/dcm-control-plane
AUTH=true make compose-down
```

This runs `podman compose down -v` which removes all containers and volumes.

**Helm deployment:**

```bash
helm uninstall dcm-test --namespace dcm-helm-test 2>/dev/null
oc delete project dcm-helm-test 2>/dev/null || kubectl delete namespace dcm-helm-test 2>/dev/null
```

---

## Test Case Dependency Graph

```
Global Setup (clone, Config A start)
    |
    +-- TC-02 (Auth disabled CRUD) --- TC-30 (Disabled ignores garbage)
    |
    +-- [Switch to Config B] --+
    |                          |
    +-- TC-01 (Health bypass)  +-- TC-03 (Admin seeding)
    |                          |     |
    +-- TC-16 (No auth 401)    |     +-- TC-04 (JWT password) ----+-- TC-08 (CRUD w/ JWT)
    |                          |     |                             |
    +-- TC-17 (Wrong secret)   |     +-- TC-05 (JWT client_creds) +-- TC-12 (JWT precedence)
    |                          |     |                             |
    +-- TC-15 (Forged JWT)     |     +-- TC-07 (Proxy headers)    +-- TC-12b (Invalid JWT no fallback)
                               |     |                             |
                               |     |                             +-- TC-22 (Cache hit)
                               |     |                             |
                               |     +-- TC-28 (Idempotent seed)   +-- TC-23 (Cache TTL)
                               |     |
                               |     +-- TC-06 (JIT user) ---+-- TC-09 (preferred_username)
                               |                              |
                               +-- TC-10 (UUID fallback)      +-- TC-11 (No re-provision)
                               |                              |
                               +-- TC-24 (Concurrent same)    +-- TC-19 (Suspended 403) --- TC-21 (Reactivated)
                               |                              |
                               +-- TC-25 (Concurrent diff)    +-- TC-20 (Deactivated 403)
                               |
                               +-- [Switch to Config C] --+
                               |                          |
                               +-- TC-13 (No validator)   +-- TC-18 (Empty subject)
                               |
                               +-- TC-14 (Expired JWT)
                               |
                               +-- TC-26 (401 RFC 7807)
                               +-- TC-27 (403 format)
                               +-- TC-29 (No admin subject)
                               |
                               +-- TC-31 (Compose teardown)


Helm Chart (independent track - requires K8s/OCP cluster):

    TC-32 (Helm install) --- TC-33 (Health check) --- TC-34 (CRUD round-trip) --- TC-35 (Helm uninstall)

Full-stack authentication / user-token propagation (Config B + RHDH / CI):

    Config B --+-- TC-37 (SP register) --- TC-38 (SP health under auth)   [QA-gated: FLPATH-4622 candidate]
               |                              |
               +-- TC-36 Step 2 (instance create)  [QA-gated: FLPATH-4622 candidate]
               +-- TC-36 Steps 1 + 3 (token / unauthenticated → 401)  [independently runnable]
               +-- TC-39 (RHDH user-token forwarding) --- TC-40 (authenticated `/dcm` fixture)
               |                                      |
               |                                      +-- TC-43 (forwarding trust boundary)
               +-- TC-41 (utilities auth matrix; PR #57 merged, Jenkins execution unverified)
               +-- TC-42 (Keycloak restart baseline) --- TC-45 (JWKS restart + signing-key rotation)
               +-- TC-44 (JWT rejection matrix)
               +-- TC-46 (token redaction)
```

---

## Risk Observations

1. **Proxy-header fallback is a separate trust path**: Anyone with `AUTH_PROXY_SECRET` can set arbitrary `X-Forwarded-User`. JWT user-token propagation is the RHDH UI path; do not use proxy headers as evidence that it works.
2. **`AUTH_DISABLED=true` is the compose default**: It must be explicitly overridden for authentication testing. TC-41 keeps the no-token result separate from auth-enabled assertions.
3. **JWKS availability and key refresh are authentication dependencies**: If the issuer/JWKS is unavailable, validator startup or refresh can fail. TC-42 and TC-45 cover restart and rotation behavior.
4. **Helm chart auth is not templatized**: `AUTH_DISABLED` is hardcoded to `true` in the chart. Auth-enabled Helm testing remains deferred to [FLPATH-4476](https://redhat.atlassian.net/browse/FLPATH-4476).
5. **Token-forwarding boundary**: A browser-supplied `X-DCM-OIDC-Token` must not be trusted without a validated RHDH session. TC-43 covers this boundary.
6. **Token disclosure**: Raw JWTs in runtime logs, Jenkins output, or saved artifacts are a security exposure. TC-46 requires redaction/fingerprint-only evidence.
7. **CI control-plane host port is 9080**: Ecosystem Jenkins remaps 8080→9080. E2E and curl examples must use the correct host port or authentication tests can fail falsely.
8. **Utilities coverage versus execution**: [FLPATH-4478](https://redhat.atlassian.net/browse/FLPATH-4478) is Closed/Done and utilities PR #57 is merged, but no Jenkins execution result is recorded here.
9. **Agent authentication candidate**: TC-36 Step 2, TC-37, and TC-38 are QA-gated and not yet executed against the [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) — Implement Auth mechanism to the agent candidate. TC-36 Steps 1 and 3 remain independently runnable.

---

## Automation Coverage

The following test cases are automated in the auth subsystem test suite
([control-plane#32](https://github.com/dcm-project/control-plane/pull/32),
`test/subsystem/auth/`). Automated tests run in CI on every PR via
`.github/workflows/subsystem.yaml`.

| TC | Description | Subsystem Test File | Notes |
|---|---|---|---|
| TC-01 | Health bypass | `health_test.go` | 3 sub-cases: no auth, invalid bearer, wrong proxy |
| TC-02 | Auth disabled mode | catalog / policy / sp suites | Implicit: those stacks set `AUTH_DISABLED=true` and exercise APIs without Bearer tokens |
| TC-03 | Admin seeding | `admin_seed_test.go` | Actor + identity verified via DB |
| TC-04 | JWT password grant | `oidc_test.go` | ROPC flow + JIT provision + DB verify |
| TC-05 | Client credentials | `oidc_test.go` | Service account JIT provision + DB verify |
| TC-06 | JIT provisioning | `jit_test.go`, `oidc_test.go` | Proxy and JWT paths both covered |
| TC-07 | Proxy header auth | `proxy_secret_test.go` | |
| TC-09 | preferred_username | `jit_test.go` | |
| TC-10 | UUID fallback | `jit_test.go` | |
| TC-11 | No re-provisioning | `jit_test.go` | Cache hit path |
| TC-11b | Username collision | `race_test.go` | 409 Conflict verified |
| TC-12 | JWT precedence (valid) | `proxy_secret_test.go` | Valid JWT + valid proxy, JWT identity wins |
| TC-12b | JWT precedence (invalid) | `proxy_secret_test.go` | Invalid JWT + valid proxy = hard 401 |
| TC-15 | Forged JWT | `oidc_test.go` | Tampered signature; ❗ should cover `alg:none` |
| TC-16 | Missing auth 401 | `proxy_secret_test.go` | |
| TC-17 | Wrong proxy secret | `proxy_secret_test.go` | |
| TC-18 | Missing subject | `proxy_secret_test.go` | |
| TC-19 | Suspended 403 | `status_test.go` | DB update + cache TTL wait |
| TC-20 | Deactivated 403 | `status_test.go` | |
| TC-21 | Reactivated actor | `status_test.go` | Suspend -> 403 -> reactivate -> 200 |
| TC-24 | Concurrent same user | `race_test.go` | 10 goroutines, unique constraint handling |
| TC-26 | 401 RFC 7807 format | `error_format_test.go` | Content-Type + body fields + WWW-Authenticate |
| TC-27 | 403 RFC 7807 format | `error_format_test.go` | |

**22 of 35 control-plane/Helm test cases** have dedicated auth-suite automation (27 Ginkgo specs). **TC-02** is additionally covered implicitly by catalog/policy/sp suites (`AUTH_DISABLED=true`). Counting TC-02: **23 of 35** have automated coverage.

Remaining manual-only / gap TCs (TC-01–TC-35):

| TC | Reason not automated |
|---|---|
| TC-08 | Provider CRUD via CP API with JWT - control-plane API only; full-stack instance under auth is TC-36 |
| TC-13 | Config C (proxy-only) - requires separate compose configuration |
| TC-14 | Expired JWT - requires wait or short-lived token (see TC-14 note); ❗ should cover wrong-audience JWT (subsystem) |
| TC-22 | Cache bypass verification - requires log inspection |
| TC-23 | Cache TTL expiry - partially covered by status tests (`AUTH_CACHE_TTL=2s`); no dedicated TTL assertion |
| TC-25 | Concurrent different users - low risk vs TC-24 |
| TC-28 | Admin seeding idempotent - requires container restart |
| TC-29 | Empty DCM_ADMIN_SUBJECT - requires separate compose configuration |
| TC-30 | Auth disabled ignores garbage headers - not covered by other suites (they omit auth headers; they do not assert garbage Bearer/proxy headers are ignored) |
| TC-31 | Compose teardown - infrastructure, not functional |
| TC-32-35 | Helm chart tests - separate infrastructure (FLPATH-4476) |

### Full-stack / E2E authentication coverage (TC-36 – TC-46)

| TC | Description | Status |
|---|---|---|
| TC-36 | Instance create under auth | ❗ Step 2 QA-gated pending an FLPATH-4622 candidate; Steps 1+3 independently runnable |
| TC-37 | SP registration under auth | ❗ QA-gated; not yet executed against the FLPATH-4622 candidate |
| TC-38 | SP health under auth | ❗ QA-gated; not yet executed against the FLPATH-4622 candidate |
| TC-39 | RHDH authenticated user-token forwarding | Pending authenticated RHDH UI-session execution; route and RHBK test user verified |
| TC-40 | Authenticated `/dcm` known-fixture view | Pending TC-39 authenticated-session evidence |
| TC-41 | `ENABLE_DCM_AUTH` authentication matrix | [utilities#57](https://github.com/dcm-project/utilities/pull/57) merged; local DCM API auth matrix passed 2/2; Jenkins execution unverified; RHDH UI forwarding remains TC-39/TC-40 |
| TC-42 | Keycloak restart baseline resilience | Not automated — planned utilities E2E |
| TC-43 | User-token forwarding trust boundary | Not automated — planned utilities E2E |
| TC-44 | JWT rejection matrix | Partial subsystem coverage; full matrix not yet automated |
| TC-45 | JWKS restart and signing-key rotation | Not automated — planned utilities E2E |
| TC-46 | Token redaction | Not automated — planned utilities E2E |

**1 of 11 E2E authentication TCs has merged automation coverage (TC-41); no Jenkins pass is claimed.**

---

## Version History

| Version | Date | Changes |
|---|---|---|
| 1.0 | 2026-07-17 | Initial test plan: 31 test cases covering health bypass, auth disabled, admin seeding, JWT auth, JIT provisioning, proxy headers, status enforcement, caching, concurrency, error format, and configuration |
| 1.1 | 2026-07-17 | Added Helm chart smoke tests (TC-32 through TC-35): install, health, CRUD round-trip, uninstall. Total: 35 test cases |
| 1.2 | 2026-07-17 | Commands validated against live lab deployment |
| 1.3 | 2026-07-17 | Senior QE review fixes: corrected KC_HOSTNAME_STRICT doc (false not true), fixed TC-29 compose override for empty DCM_ADMIN_SUBJECT, added TC-12b (invalid JWT + valid proxy = 401), added username conflict risk observation |
| 1.4 | 2026-07-20 | Full validation on lab host: all 32 compose TCs passed, 4 Helm TCs skipped (no cluster). Fixes: TC-06 KC user needs email+emailVerified, TC-11b cleanup table name actor_identities not identities, TC-29 must use AUTH_DISABLED=true, TC-19/20 restart commands use podman restart directly, psql via podman exec |
| 1.5 | 2026-07-20 | Phase 6 review fixes: promoted TC-14/TC-15 to P1 (security-critical), fixed TC-31 container filter from deploy_ to control-plane, replaced container name placeholders with discoverable podman commands |
| 1.6 | 2026-07-20 | TC-32 Step 2: corrected label selector from `app.kubernetes.io/component=control-plane` to `app.kubernetes.io/name=control-plane` based on actual chart template execution on a lab SNO cluster |
| 1.7 | 2026-07-22 | Added automation coverage section mapping 22 of 35 TCs to subsystem test suite ([control-plane#32](https://github.com/dcm-project/control-plane/pull/32)). 27 Ginkgo specs cover TC-01, TC-03-07, TC-09-12b, TC-15-21, TC-24, TC-26-27. Remaining 13 TCs documented as manual-only with rationale |
| 1.8 | 2026-08-05 | Added full-stack/E2E gap TCs TC-36–TC-46 (CLI, UI M2M, SP under auth, instance under auth, wrong audience, alg:none, Jenkins ENABLE_DCM_AUTH, JWKS restart). Clarified TC-08/TC-14 notes, CI port 9080, and the then-known E2E gaps. Total: 46 test cases |
| 1.8.1 | 2026-08-07 | Review trim: drop CLI + JWT-negative E2E TCs; renumber gaps to TC-36–TC-42; document the then-current FLPATH-4622/UI dependencies; fix catalog-item-instances API + `health_status`; port convention; Ecosystem Jenkins; TC-08 CP-only; mark TC-02 automated via catalog/policy/sp (`AUTH_DISABLED=true`); clarify TC-30 not covered by those suites. Total numbered TCs: 42 (+ TC-11b/12b) |
| 2.0 | 2026-09-28 | Authentication-only update: scope and acceptance criteria exclude authorization, RBAC, tenant isolation, policy enforcement, and access control; rewrote RHDH user-token propagation (TC-39) and authenticated `/dcm` flow (TC-40); updated merged utilities PR #57 matrix (TC-41) without claiming Jenkins execution; changed TC-36–TC-38 to QA-gated FLPATH-4622 candidate execution; removed the obsolete UI-ticket dependency; added TC-43–TC-46 for trust boundary, JWT rejection, JWKS/signing-key rotation, and token redaction. Total numbered TCs: 46 (+ TC-11b/12b) |
| 2.1 | 2026-09-28 | Recorded fresh auth-enabled validation: TC-01, TC-04, TC-16, TC-26, and TC-41 passed; utilities PR #57 auth matrix passed 2/2 locally; clarified that TC-39/TC-40 require an authenticated RHDH browser session and are not blocked by route or RHBK-user availability. Documented the Jenkins gap: upstream `dcm_deploy.groovy` still passes unsupported auth flags, so TC-41 remains unverified in Jenkins pending a pipeline fix. |

---

## Test case checklist

Checkbox = automated (dedicated auth-suite link **or** documented coverage via other subsystem suites). Empty automation link means not automated yet.

**❗** = coverage gap or QA-gated dependency (a gap may remain even if the base TC is already automated).

Base path for subsystem links: [`dcm-project/control-plane` `test/subsystem/`](https://github.com/dcm-project/control-plane/tree/main/test/subsystem) (`auth/`, `catalog/`, `policy/`, `sp/`).

### Subsystem tests (TC-01 – TC-35)

| Automated | TC | Name | Automation |
|---|---|---|---|
| [x] | TC-01 | Health endpoint bypasses auth when auth is enabled | [health_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/health_test.go) |
| [x] | TC-02 | Auth disabled mode - API requests succeed without auth | [catalog](https://github.com/dcm-project/control-plane/tree/main/test/subsystem/catalog) / [policy](https://github.com/dcm-project/control-plane/tree/main/test/subsystem/policy) / [sp](https://github.com/dcm-project/control-plane/tree/main/test/subsystem/sp) (`AUTH_DISABLED=true`) |
| [x] | TC-03 | Admin actor and identity seeded on startup | [admin_seed_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/admin_seed_test.go) |
| [x] | TC-04 | JWT authentication via password grant | [oidc_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/oidc_test.go) |
| [x] | TC-05 | JWT authentication via client_credentials grant | [oidc_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/oidc_test.go) |
| [x] | TC-06 | JIT provisioning of new Keycloak user | [jit_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/jit_test.go), [oidc_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/oidc_test.go) |
| [x] | TC-07 | Proxy header authentication | [proxy_secret_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/proxy_secret_test.go) |
| [ ] | TC-08 | Provider CRUD via control-plane API with JWT auth | — |
| [x] | TC-09 | JIT provisioning uses preferred_username for actor username | [jit_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/jit_test.go) |
| [x] | TC-10 | JIT provisioning falls back to externalID when preferred_username is absent | [jit_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/jit_test.go) |
| [x] | TC-11 | Second request for same user skips re-provisioning | [jit_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/jit_test.go) |
| [x] | TC-11b | Username collision during JIT provisioning returns 409 | [race_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/race_test.go) |
| [x] | TC-12 | Bearer token takes precedence over proxy headers | [proxy_secret_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/proxy_secret_test.go) |
| [x] | TC-12b | Invalid JWT with valid proxy headers returns 401 (no fallback) | [proxy_secret_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/proxy_secret_test.go) |
| [ ] | TC-13 | No JWTValidator falls through to proxy path | — |
| [ ] ❗ | TC-14 | Expired JWT rejected | — ❗ should cover wrong-audience JWT (Config B + curl; needs a Keycloak client **without** the `dcm-api` audience mapper — stock `dcm-proxy`/`dcm-cli` always map `aud=dcm-api`) |
| [x] ❗ | TC-15 | Forged JWT rejected | [oidc_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/oidc_test.go) — ❗ should cover `alg:none` |
| [x] | TC-16 | Missing auth headers returns 401 | [proxy_secret_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/proxy_secret_test.go) |
| [x] | TC-17 | Wrong proxy secret returns 401 | [proxy_secret_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/proxy_secret_test.go) |
| [x] | TC-18 | Missing subject identifier returns 401 | [proxy_secret_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/proxy_secret_test.go) |
| [x] | TC-19 | Suspended actor receives 403 | [status_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/status_test.go) |
| [x] | TC-20 | Deactivated actor receives 403 | [status_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/status_test.go) |
| [x] | TC-21 | Reactivated actor succeeds after cache expires | [status_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/status_test.go) |
| [ ] | TC-22 | Cached actor bypasses DB lookup | — |
| [ ] | TC-23 | Cache entry expires after TTL | — (partial: status tests use short `AUTH_CACHE_TTL`; no dedicated TTL expiry assertion) |
| [x] | TC-24 | Concurrent first login - same user | [race_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/race_test.go) |
| [ ] | TC-25 | Concurrent first login - different users | — |
| [x] | TC-26 | 401 response format is RFC 7807 compliant | [error_format_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/error_format_test.go) |
| [x] | TC-27 | 403 response format for suspended and deactivated actors | [error_format_test.go](https://github.com/dcm-project/control-plane/blob/main/test/subsystem/auth/error_format_test.go) |
| [ ] | TC-28 | Admin seeding is idempotent on restart | — |
| [ ] | TC-29 | Admin seeding skipped when DCM_ADMIN_SUBJECT is empty | — |
| [ ] | TC-30 | Auth disabled mode ignores garbage auth headers | — (other suites omit auth; they do not assert garbage headers are ignored) |
| [ ] | TC-31 | Compose stack teardown | — |
| [ ] | TC-32 | Helm install and pod readiness | — |
| [ ] | TC-33 | Health endpoint accessible via Helm deployment | — |
| [ ] | TC-34 | Multi-subsystem CRUD with auth disabled (Helm) | — |
| [ ] | TC-35 | Helm uninstall cleans up resources | — |

### E2E authentication tests (TC-36 – TC-46)

| Automated | TC | Name | Automation |
|---|---|---|---|
| [ ] ❗ | TC-36 | Instance creation under JWT auth (full stack) | — ❗ Step 2 QA-gated pending the [FLPATH-4622](https://redhat.atlassian.net/browse/FLPATH-4622) candidate; Steps 1+3 independently runnable |
| [ ] ❗ | TC-37 | Service provider registration with auth enabled | — ❗ QA-gated; not yet executed against the FLPATH-4622 candidate |
| [ ] ❗ | TC-38 | Service provider health remains ready with auth enabled | — ❗ QA-gated; not yet executed against the FLPATH-4622 candidate |
| [ ] | TC-39 | RHDH backend forwards the authenticated user's DCM OIDC token | Pending authenticated RHDH UI-session execution; route and RHBK test user verified |
| [ ] | TC-40 | Authenticated user opens `/dcm` and sees known provider/catalog data | Pending TC-39 authenticated-session evidence |
| [x] | TC-41 | Utilities authentication matrix via `ENABLE_DCM_AUTH` | [utilities#57](https://github.com/dcm-project/utilities/pull/57) merged; local DCM API auth matrix passed 2/2; Jenkins execution unverified; RHDH UI forwarding remains TC-39/TC-40 |
| [ ] | TC-42 | Keycloak restart baseline resilience | — |
| [ ] | TC-43 | User-token forwarding trust boundary | — |
| [ ] | TC-44 | JWT rejection matrix | — partial subsystem coverage only |
| [ ] | TC-45 | JWKS restart and signing-key rotation | — |
| [ ] | TC-46 | Token redaction in logs and test evidence | — |

---

## Sanitization Notice

This document is intended for sharing. The following rules apply:

- No credentials, tokens, API keys, or passwords - use placeholders (`<ADMIN_PASSWORD>`, `<API_TOKEN>`)
- No internal hostnames or IPs - use placeholders (`<CLUSTER_HOST>`, `<API_ENDPOINT>`)
- No internal URLs (Kerberos, RHSSO, internal wikis, proprietary portals)
- No PII (employee names, emails, account IDs) - use `<USER>`, `<ADMIN>`
- Secrets required by steps should reference where to obtain them (e.g., "retrieve from vault at `<VAULT_PATH>`"), never inline
- Open-source tool and project names are fine as-is
