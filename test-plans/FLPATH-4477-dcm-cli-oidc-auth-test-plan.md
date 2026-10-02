# Test Plan: DCM CLI OIDC Authentication


| Field              | Value                                                          |
| ------------------ | -------------------------------------------------------------- |
| **Story**          | [FLPATH-4477](https://redhat.atlassian.net/browse/FLPATH-4477) |
| **Author**         | Chad Crum                                                      |
| **Version**        | 1.4                                                            |
| **Last Updated**   | 2026-08-05                                                     |
| **Target Release** | DCM 1.0                                                        |
| **Status**         | Ready                                                          |




## Description

The DCM CLI adds OIDC authentication using the OAuth 2.0 Device Authorization Grant (RFC 8628). This test plan validates the `dcm login` and `dcm logout` commands, token storage (OS keyring with file fallback), authenticated HTTP transport with auto-refresh, static token bypass for CI/scripting, and configuration persistence.

### References

- [DCM CLI OIDC Authentication Spec](https://github.com/dcm-project/cli/blob/main/.ai/specs/dcm-cli-oidc-auth.spec.md) - requirements and acceptance criteria validated by this plan (lives in dcm-project/cli)
- [FLPATH-4477](https://redhat.atlassian.net/browse/FLPATH-4477) - Add OIDC authentication to DCM CLI
- [FLPATH-4432](https://redhat.atlassian.net/browse/FLPATH-4432) - Control-plane authentication (dependency)
- [FLPATH-4643](https://redhat.atlassian.net/browse/FLPATH-4643) - Auto-discover issuer URL (follow-up)



### Acceptance Criteria

- All P1 (critical) test cases must PASS
- All P2 (important) test cases must PASS or have documented workarounds
- P3 (nice-to-have) failures may be deferred with a tracking Jira issue

---



## Environment and Global Setup



### Environment Requirements

- RHEL 9.x host with Podman 5.x and podman-compose
- `dcm` CLI installed from a [GitHub release](https://github.com/dcm-project/cli/releases) **that includes OIDC authentication** (`dcm login` / `dcm logout` must exist; verify with `dcm login --help` before running cases)
- DCM control-plane stack available (auth-enabled for most cases; auth-disabled procedure documented for TC-09)
- Keycloak published on the host at `http://localhost:8180` by control-plane `deploy/compose.auth.yaml` (loaded with the `auth` profile), with the `dcm` realm imported
- Hostname `keycloak` resolves on the host to the Keycloak container IP (via `/etc/hosts`) so CLI discovery and token endpoints match the issuer advertised by the auth Compose model



### Keycloak Preconfigured Data

From control-plane `deploy/keycloak/realm-export.json` (and compose env for secrets):


| Item                  | Value                                                                            |
| --------------------- | -------------------------------------------------------------------------------- |
| Realm                 | `dcm`                                                                            |
| Client (public)       | `dcm-cli` (device auth grant, audience mapper: `dcm-api`)                        |
| Client (confidential) | `dcm-proxy` (password + client_credentials grants for static-token cases)        |
| User                  | `dcm-admin` (password from compose / realm secrets as `<DCM_DEV_USER_PASSWORD>`) |
| Client secret         | `dcm-proxy` secret from compose / realm secrets as `<DCM_PROXY_SECRET>`          |
| Access token lifespan | 300s (5 minutes)                                                                 |


**Credential sourcing:** Resolve `<DCM_DEV_USER_PASSWORD>` and `<DCM_PROXY_SECRET>` from the control-plane compose environment or Keycloak realm export used by the local stack. Do not commit real secrets into this plan.

**Issuer URL rule:** This plan uses `http://keycloak:8080/realms/dcm` for `--issuer-url` / `DCM_ISSUER_URL` / `AUTH_ISSUER_URL`. That value MUST equal the `issuer` string from OIDC discovery. Control-plane `deploy/compose.auth.yaml` sets `KC_HOSTNAME=http://keycloak:8080`, so discovery returns that issuer whether you hit Keycloak via `http://keycloak:8080` or the published port `http://localhost:8180`.

**Why not** `localhost:8180` **as issuer:** Overriding `KC_HOSTNAME` to `http://localhost:8180` makes discovery look host-friendly, but the control-plane container cannot reach Keycloak at `localhost:8180` (`localhost` inside the container is not the host). Host-gateway / `extra_hosts` workarounds are brittle (IPv6 `::1` vs host-gateway IPv4). Keep the stock `keycloak:8080` issuer so the control-plane and CLI share one reachable issuer; add a host `/etc/hosts` entry so the CLI on the host can resolve `keycloak`.

### Global Setup

**Step 1: Install an OIDC-capable CLI from GitHub**

```bash
gh release download <release> -R dcm-project/cli -p '*linux_amd64.tar.gz' --dir /tmp/dcm-cli --clobber
tar -xzf /tmp/dcm-cli/cli_*_linux_amd64.tar.gz -C /tmp/dcm-cli
sudo install -m 755 /tmp/dcm-cli/dcm /usr/local/bin/dcm
dcm version
dcm login --help
```

**Expected:** `dcm` is on `PATH`, prints version information, and `dcm login --help` documents the login command. If `login` is missing, the chosen release does not include OIDC authentication - pick a release (or build) that does before continuing.

**Step 2: Start the control-plane stack with the auth Compose override enabled (stock Keycloak issuer)**

Use stock Keycloak hostname (`KC_HOSTNAME=http://keycloak:8080`). Do not override it to localhost.

```bash
cd <control-plane-repo>
AUTH=true AUTH_DISABLED=false AUTH_ISSUER_URL=http://keycloak:8080/realms/dcm AUTH_JWT_AUDIENCE=dcm-api make compose-up
```

**Expected:** All services healthy (Keycloak takes 30-60s for realm import). Control plane validates JWTs against `http://keycloak:8080/realms/dcm`.

**Step 3: Point host** `keycloak` **at the Keycloak container**

```bash
KC_IP=$(podman inspect "$(podman ps -q --filter name=keycloak)" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
# Or use the compose service container name, e.g. deploy_keycloak_1 / control-plane_keycloak_1
grep -qE '[[:space:]]keycloak$' /etc/hosts && sudo sed -i -E "s/^[0-9.]+[[:space:]]+keycloak$/${KC_IP} keycloak/" /etc/hosts || echo "${KC_IP} keycloak" | sudo tee -a /etc/hosts
getent hosts keycloak
```

**Expected:** `keycloak` resolves to the container IP on the host.

**Step 4: Verify Keycloak OIDC discovery**

```bash
curl -sf http://keycloak:8080/realms/dcm/.well-known/openid-configuration | jq .issuer
# Published port also works for HTTP, but issuer claim stays keycloak:
curl -sf http://localhost:8180/realms/dcm/.well-known/openid-configuration | jq .issuer
```

**Expected:** Both return `"http://keycloak:8080/realms/dcm"`. If the issuer is anything else, fix `KC_HOSTNAME` / `AUTH_ISSUER_URL` before running login cases.

**Step 5: Clean any existing CLI credentials**

```bash
rm -f ~/.dcm/tokens.json
# Clear keyring entry if present (service dcm-cli, account = issuer URL)
```

**Auth-disabled stack (TC-09 only):**

```bash
cd <control-plane-repo>
AUTH=true make compose-down
AUTH_DISABLED=true make compose-up
```

After TC-09, restore the auth-enabled stack (Step 2) before continuing other cases.

---



## Test Cases



### TC-01: `dcm login` - interactive device flow

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** Global setup

#### Description

`dcm login` initiates the OIDC Device Authorization Grant flow, displays a verification URL and user code, and stores tokens after the user completes browser authentication.

#### Prerequisites

- Stack running with auth enabled
- No existing stored tokens



#### Steps

**Step 1: Run dcm login**

```bash
dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

**Expected:** Output on stderr includes:

- A verification URL (e.g., `http://keycloak:8080/realms/dcm/device`)
- A user code (e.g., `ABCD-EFGH`)
- Browser auto-opens (or prints URL if browser open fails)

**Step 2: Complete authentication in browser**

Open the displayed URL, enter the user code, log in as `dcm-admin`.

**Expected:** CLI prints `Logged in as dcm-admin (token expires in …; auto-refresh enabled)` on stderr.

**Step 3: Verify token is stored**

```bash
ls -la ~/.dcm/tokens.json
```

**Expected:** File exists with `0600` permissions (file fallback), OR credentials stored in OS keyring.

**Step 4: Verify config was persisted**

```bash
cat ~/.dcm/config.yaml
```

**Expected:** Contains `issuer-url: http://keycloak:8080/realms/dcm` and `control-plane-url: http://localhost:8080`.

#### Cleanup

None - tokens needed for subsequent test cases.

---



### TC-02: Authenticated API call after login

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** TC-01

#### Description

After `dcm login`, subsequent CLI commands inject the Bearer token automatically and succeed against the auth-enabled control-plane.

#### Prerequisites

- TC-01 passed, tokens stored



#### Steps

**Step 1: List policies with auth**

```bash
dcm policy list
```

**Expected:** HTTP 200 response with policies list (empty or populated). No 401 error.

**Step 2: List providers with auth**

```bash
dcm sp provider list
```

**Expected:** HTTP 200 response. Bearer token automatically injected.

**Step 3: Verify no auth flags needed**

The command uses `issuer-url` from `~/.dcm/config.yaml` (persisted by `dcm login`).

#### Cleanup

None.

---



### TC-03: Token auto-refresh on expiry

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** TC-01

#### Description

When the access token is expired, the CLI automatically refreshes using the stored refresh token without user intervention. Do not wait for Keycloak's default 300s access-token lifespan - force access-token expiry in the file store instead (same `IsExpired` / JWT `exp` behavior as TC-19).

#### Prerequisites

- Stack running with auth enabled
- File store active so the access token can be invalidated (force with `DBUS_SESSION_BUS_ADDRESS=` if needed; same technique as TC-13/TC-19)



#### Steps

**Step 1: Ensure tokens are in the file store**

If `~/.dcm/tokens.json` is missing (keyring backend was used in TC-01), re-login with file store:

```bash
rm -f ~/.dcm/tokens.json
DBUS_SESSION_BUS_ADDRESS= dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth. Confirm `~/.dcm/tokens.json` exists.

**Step 2: Force access-token expiry (keep refresh token)**

`IsExpired` prefers the JWT `exp` claim on `access_token`. Backdating `expiry` alone does not trigger refresh while the JWT is still valid. Replace `access_token` with a non-JWT so the CLI treats the session as expired and attempts refresh. Leave `refresh_token` unchanged.

```bash
python3 << 'PY'
import json, time
from pathlib import Path
p = Path.home() / ".dcm" / "tokens.json"
d = json.loads(p.read_text())
iu = "http://keycloak:8080/realms/dcm"
d[iu]["access_token"] = "not-a-jwt"  # forces IsExpired; JWT exp would ignore backdated expiry
d[iu]["expiry"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 60))
p.write_text(json.dumps(d))
print("Invalidated access token; refresh token left intact")
PY
```

**Step 3: Run a CLI command**

```bash
DBUS_SESSION_BUS_ADDRESS= dcm policy list --control-plane-url http://localhost:8080
```

**Expected:** Command succeeds (HTTP 200). The CLI automatically refreshed the access token using the refresh token. No login prompt.

**Step 4: Verify updated token in store**

```bash
python3 << 'PY'
import json
from pathlib import Path
d = json.loads((Path.home() / ".dcm" / "tokens.json").read_text())
iu = "http://keycloak:8080/realms/dcm"
tok = d[iu]["access_token"]
assert tok != "not-a-jwt", "access_token was not refreshed"
print("Refreshed access_token present:", bool(tok))
print("expiry:", d[iu].get("expiry"))
PY
```

**Expected:** `access_token` is a real JWT again (not `not-a-jwt`) with an updated expiry.

#### Cleanup

None.

---



### TC-04: `dcm logout` - token revocation

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** TC-01

#### Description

`dcm logout` revokes the refresh token at Keycloak's revocation endpoint, deletes stored credentials, and prints a confirmation.

#### Prerequisites

- Tokens stored from TC-01 (or TC-03 refresh)



#### Steps

**Step 1: Run dcm logout**

```bash
dcm logout
```

**Expected:** Output: `Logged out successfully` on stderr.

**Step 2: Verify tokens removed**

```bash
# File store
test -f ~/.dcm/tokens.json && python3 -c "import json; d=json.load(open('$HOME/.dcm/tokens.json')); iu='http://keycloak:8080/realms/dcm'; print('Token for issuer:', iu in d)" || echo "File empty or missing"
# Keyring: confirm a subsequent load finds nothing (logout success message is primary signal)
```

**Expected:** No token stored for the issuer URL (file and/or keyring).

**Step 3: Verify API call after logout (soft passthrough)**

```bash
dcm policy list 2>&1
```

**Expected:** With `issuer-url` still in config but TokenStore empty, the CLI sends no Authorization header (REQ-TRN-110). Against an auth-enabled control plane, expect HTTP 401 from the server. The CLI MUST NOT invent a local "credentials needed" failure before the request.

#### Cleanup

None.

---



### TC-05: `dcm login` requires --issuer-url

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Manual
**Requires:** Global setup

#### Description

Running `dcm login` without `--issuer-url` (and no `DCM_ISSUER_URL` env var or config file value) returns an actionable error.

#### Prerequisites

- No `issuer-url` in `~/.dcm/config.yaml`
- `DCM_ISSUER_URL` not set



#### Steps

**Step 1: Clear issuer-url from config**

```bash
# Remove or edit config to remove issuer-url
rm -f ~/.dcm/config.yaml
unset DCM_ISSUER_URL
```

**Step 2: Run login without issuer-url**

```bash
dcm login 2>&1
```

**Expected:** Error message: `--issuer-url is required (or set DCM_ISSUER_URL)`. Exit code 1.

#### Cleanup

None.

---



### TC-06: Static token via DCM_TOKEN

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** Global setup

#### Description

Setting `DCM_TOKEN` (or `--token`) bypasses the OIDC flow entirely and injects the provided Bearer token directly. No refresh logic, no keyring interaction.

#### Prerequisites

- Stack running with auth enabled



#### Steps

**Step 1: Obtain a token via curl**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://keycloak:8080/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
```

**Step 2: Use DCM_TOKEN for API call**

```bash
DCM_TOKEN=$TOKEN dcm policy list --control-plane-url http://localhost:8080
```

**Expected:** HTTP 200 response. Token injected as `Authorization: Bearer <token>`.

**Step 3: Use --token flag**

```bash
dcm policy list --token "$TOKEN" --control-plane-url http://localhost:8080
```

**Expected:** HTTP 200 response. Same behavior as env var.

**Step 4: Verify static token is not persisted to config**

```bash
cat ~/.dcm/config.yaml 2>/dev/null || true
```

**Expected:** No `token:` (or equivalent) field written to the config file (REQ-ACFG-060 / AC-ACFG-050).

#### Cleanup

None.

---



### TC-07: Static token takes precedence over stored token

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** TC-01, TC-06

#### Description

When both `DCM_TOKEN` and a stored OIDC token exist, `DCM_TOKEN` takes precedence. Prove precedence by showing the stored token works, then an invalid static token makes the same call fail.

#### Prerequisites

- Stored token from `dcm login`



#### Steps

**Step 1: Login to store a token**

```bash
dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth.

**Step 2: List policies with the stored token**

```bash
dcm policy list --control-plane-url http://localhost:8080
```

**Expected:** HTTP 200 response with a policies list (empty or populated). Stored OIDC token is used.

**Step 3: Override with an invalid static token**

```bash
DCM_TOKEN=not-a-valid-token dcm policy list --control-plane-url http://localhost:8080 2>&1; echo "Exit code: $?"
```

**Expected:** Command fails (non-zero exit). Control plane rejects the request (HTTP 401 or equivalent auth error). Success here would mean the stored OIDC token was used instead of `DCM_TOKEN`.

#### Cleanup

```bash
unset DCM_TOKEN
dcm logout
```

---



### TC-08: HTTP scheme warning

**Priority:** P2 (important)
**Type:** Security
**Method:** Manual
**Requires:** TC-01 or TC-06

#### Description

When the control-plane URL uses `http://` (not `https://`), the CLI warns once on stderr about sending Bearer tokens over unencrypted HTTP.

#### Prerequisites

- Tokens stored or `DCM_TOKEN` set
- Control-plane URL uses `http://`



#### Steps

**Step 1: Run command against HTTP endpoint**

```bash
dcm policy list --control-plane-url http://localhost:8080 2>&1 | grep -i "unencrypted"
```

**Expected:** stderr contains: `Warning: sending Bearer token over unencrypted HTTP connection`

**Step 2: Run a second command**

```bash
dcm sp provider list --control-plane-url http://localhost:8080 2>&1 | grep -i "unencrypted"
```

**Expected:** Warning appears once per CLI invocation (new process = new warning).

#### Cleanup

None.

---



### TC-09: No auth when issuer-url is empty

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** Auth-disabled stack (see Global Setup)

#### Description

When `issuer-url` is empty (default), the CLI sends no Authorization header. On an auth-disabled control plane this is the zero-auth path (AUTH_DISABLED compatibility).

#### Prerequisites

- Stack running with `AUTH_DISABLED=true` (see Global Setup auth-disabled procedure)
- No `issuer-url` in config, no `DCM_ISSUER_URL`, no `DCM_TOKEN`. Clear first if needed:

```bash
rm -f ~/.dcm/config.yaml
unset DCM_ISSUER_URL DCM_TOKEN
```



#### Steps

**Step 1: Run command without auth**

```bash
dcm policy list --control-plane-url http://localhost:8080
```

**Expected:** HTTP 200. No `Authorization` header sent. No warning about HTTP Bearer tokens.

#### Cleanup

Restore the auth-enabled stack (Global Setup Step 2) before continuing other cases.

---



### TC-10: `dcm logout` when not logged in

**Priority:** P2 (important)
**Type:** Negative
**Method:** Manual
**Requires:** Global setup

#### Description

Running `dcm logout` when no token is stored should not error - it should print a message indicating the user is already logged out.

#### Prerequisites

- No stored tokens. Clear first if needed:

```bash
rm -f ~/.dcm/tokens.json
# Clear keyring entry if present (service dcm-cli, account = issuer URL)
```



#### Steps

**Step 1: Run logout**

```bash
dcm logout --issuer-url http://keycloak:8080/realms/dcm 2>&1
```

**Expected:** Exits cleanly with message indicating no stored credentials or already logged out. Exit code 0.

#### Cleanup

None.

---



### TC-11: Config persistence from login

**Priority:** P1 (critical)
**Type:** Functional
**Method:** Manual
**Requires:** Global setup

#### Description

`dcm login` writes `issuer-url` and `control-plane-url` to `~/.dcm/config.yaml` so subsequent commands work with zero flags.

#### Prerequisites

- No existing `~/.dcm/config.yaml`



#### Steps

**Step 1: Remove existing config**

```bash
rm -f ~/.dcm/config.yaml
```

**Step 2: Run login with both flags**

```bash
dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth.

**Step 3: Verify config written**

```bash
cat ~/.dcm/config.yaml
```

**Expected:** Contains both `issuer-url` and `control-plane-url` values.

**Step 4: Run command with no flags**

```bash
dcm policy list
```

**Expected:** Command succeeds using config file values for both URLs.

#### Cleanup

```bash
dcm logout
```

---



### TC-12: DCM_ISSUER_URL environment variable

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** Global setup

#### Description

`DCM_ISSUER_URL` env var configures the issuer URL without needing a flag or config file. Also validates config precedence for issuer URL: flags > env vars > config file (REQ-ACFG-070).

#### Prerequisites

- No `issuer-url` in config file (clear before Step 1 if needed)



#### Steps

**Step 1: Login via env var**

```bash
rm -f ~/.dcm/config.yaml
unset DCM_ISSUER_URL
DCM_ISSUER_URL=http://keycloak:8080/realms/dcm dcm login --control-plane-url http://localhost:8080
```

Complete browser auth.

**Expected:** Login succeeds. `DCM_ISSUER_URL` used as issuer URL.

**Step 2: Use env var for authenticated command**

```bash
DCM_ISSUER_URL=http://keycloak:8080/realms/dcm dcm policy list --control-plane-url http://localhost:8080
```

**Expected:** Command succeeds with stored token.

**Step 3: Flag overrides env**

```bash
DCM_ISSUER_URL=http://keycloak:8080/realms/dcm dcm logout || true
DCM_ISSUER_URL=http://invalid.example/realms/dcm dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth.

**Expected:** Login succeeds using `--issuer-url`. A wrong `DCM_ISSUER_URL` alone would fail discovery; success means the flag won.

**Step 4: Env overrides config file**

```bash
DCM_ISSUER_URL=http://keycloak:8080/realms/dcm dcm logout || true
printf 'issuer-url: http://invalid.example/realms/dcm\ncontrol-plane-url: http://localhost:8080\n' > ~/.dcm/config.yaml
DCM_ISSUER_URL=http://keycloak:8080/realms/dcm dcm login --control-plane-url http://localhost:8080
```

Complete browser auth.

**Expected:** Login succeeds using `DCM_ISSUER_URL`. Success with a wrong config `issuer-url` means env beat the config file.

#### Cleanup

```bash
DCM_ISSUER_URL=http://keycloak:8080/realms/dcm dcm logout || true
rm -f ~/.dcm/config.yaml
unset DCM_ISSUER_URL
```

---



### TC-13: Token file permissions

**Priority:** P1 (critical)
**Type:** Security
**Method:** Manual
**Requires:** Global setup (file-store path)

#### Description

When the file-based TokenStore is active, `~/.dcm/tokens.json` must have mode `0600` and `~/.dcm/` must be `0700` (REQ-TOK-050).

#### Prerequisites

- File fallback active (keyring unavailable). Force on Linux Secret Service before login by running the command with an **empty** `DBUS_SESSION_BUS_ADDRESS` prefix (`DBUS_SESSION_BUS_ADDRESS=`). That is not `unset` - it sets the var to `""` for that process only so Secret Service cannot reach the session bus, `go-keyring` probe fails, and the CLI falls back to `~/.dcm/tokens.json`:

```bash
rm -f ~/.dcm/tokens.json
DBUS_SESSION_BUS_ADDRESS= dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth. Confirm `~/.dcm/tokens.json` exists; if not, keyring still won and this case is N/A on this host.

#### Steps

**Step 1: Check file permissions**

```bash
stat -c '%a %n' ~/.dcm/tokens.json
```

**Expected:** `600 /home/<user>/.dcm/tokens.json`

**Step 2: Check directory permissions**

```bash
stat -c '%a %n' ~/.dcm/
```

**Expected:** `700 /home/<user>/.dcm/`

#### Cleanup

```bash
dcm logout
```

---



### TC-14: Login 5-minute timeout

**Priority:** P2 (important)
**Type:** Negative
**Method:** Manual
**Requires:** Global setup

#### Description

`dcm login` has a 5-minute timeout for the device flow. If the user does not complete browser authentication within 5 minutes, the command times out with an error.

#### Prerequisites

- Stack running with auth enabled



#### Steps

**Step 1: Start login but do NOT complete browser auth**

```bash
timeout 330 dcm login --issuer-url http://keycloak:8080/realms/dcm 2>&1; echo "Exit code: $?"
```

**Expected:** After approximately 5 minutes, the command exits with a timeout or context deadline error.

#### Cleanup

None.

---



### TC-15: Expired static token returns server 401

**Priority:** P2 (important)
**Type:** Negative
**Method:** Manual
**Requires:** Global setup

#### Description

When `DCM_TOKEN` contains an expired JWT, the server returns 401. The CLI does not attempt OIDC refresh for static tokens (REQ-TRN-030). This case does **not** validate REQ-TRN-080 refresh-failure messaging (see TC-19).

#### Prerequisites

- Stack running with auth enabled



#### Steps

**Step 1: Obtain and wait for token to expire**

```bash
TOKEN=$(curl -s -d 'grant_type=password&client_id=dcm-proxy&client_secret=<DCM_PROXY_SECRET>&username=dcm-admin&password=<DCM_DEV_USER_PASSWORD>' http://keycloak:8080/realms/dcm/protocol/openid-connect/token | jq -r .access_token)
echo "Waiting 310s..."
sleep 310
```

**Step 2: Use expired token**

```bash
DCM_TOKEN=$TOKEN dcm policy list --control-plane-url http://localhost:8080 2>&1
```

**Expected:** HTTP 401 from server. No refresh attempt.

#### Cleanup

None.

---



### TC-16: Token redaction in output

**Priority:** P1 (critical)
**Type:** Security
**Method:** Manual
**Requires:** TC-01

#### Description

Token values must never appear in CLI output, error messages, or debug logs. The `TokenData.String()` method returns `[REDACTED]`.

#### Prerequisites

- Stored tokens from login



#### Steps

**Step 1: Run command with verbose error (invalid control-plane URL)**

```bash
dcm policy list --control-plane-url http://invalid-host:9999 2>&1
```

**Expected:** Error message does NOT contain any JWT or token string. Only connection error.

#### Cleanup

None.

---



### TC-17: Concurrent CLI invocations with stored token

**Priority:** P2 (important)
**Type:** Edge Case
**Method:** Manual
**Requires:** TC-01

#### Description

Multiple concurrent `dcm` commands sharing the same token store do not corrupt the token file or cause auth failures.

#### Prerequisites

- Valid tokens stored from `dcm login`



#### Steps

**Step 1: Login to store a token**

```bash
dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth.

**Step 2: Fire 5 concurrent commands**

```bash
for i in $(seq 5); do
  dcm policy list &
done
wait
echo "All commands completed"
```

**Expected:** All 5 commands succeed. No token corruption or auth errors.

**Step 3: Verify token store integrity (file store only)**

```bash
test -f ~/.dcm/tokens.json && python3 -c "import json; json.load(open('$HOME/.dcm/tokens.json')); print('Valid JSON')" || echo "Keyring backend active; skip file integrity check"
```

**Expected:** `Valid JSON` when file store is active; otherwise skip.

#### Cleanup

```bash
dcm logout
```

---



### TC-18: `dcm login` with existing config preserves other settings

**Priority:** P2 (important)
**Type:** Functional
**Method:** Manual
**Requires:** Global setup

#### Description

When `dcm login` writes `issuer-url` to `~/.dcm/config.yaml`, it does not clobber existing settings like `output-format` or `timeout`.

#### Prerequisites

- An existing config file with custom settings



#### Steps

**Step 1: Create config with custom settings**

```bash
mkdir -p ~/.dcm
cat > ~/.dcm/config.yaml << 'EOF'
output-format: json
timeout: 60
control-plane-url: http://old-server:8080
EOF
```

**Step 2: Run login**

```bash
dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth.

**Step 3: Verify config merged**

```bash
cat ~/.dcm/config.yaml
```

**Expected:** `output-format: json` and `timeout: 60` preserved. `issuer-url` and `control-plane-url` updated.

#### Cleanup

```bash
dcm logout
```

---



### TC-19: OIDC refresh failure instructs `dcm login`

**Priority:** P1 (critical)
**Type:** Negative
**Method:** Manual
**Requires:** Global setup (file-store path preferred)

#### Description

When stored credentials cannot be refreshed (invalid refresh token after access-token expiry), AuthTransport MUST return an error that instructs the user to run `dcm login` (REQ-TRN-080 / AC-TRN-060).

#### Prerequisites

- Stack running with auth enabled
- File store active so the refresh token can be corrupted (same force technique as TC-13), or equivalent keyring edit tooling



#### Steps

**Step 1: Login with file store**

```bash
rm -f ~/.dcm/tokens.json
DBUS_SESSION_BUS_ADDRESS= dcm login --issuer-url http://keycloak:8080/realms/dcm --control-plane-url http://localhost:8080
```

Complete browser auth. Confirm `~/.dcm/tokens.json` exists.

**Step 2: Corrupt the refresh token and force access-token expiry**

`IsExpired` prefers the JWT `exp` claim on `access_token` and only falls back to the stored `expiry` field for opaque tokens. Backdating `expiry` alone does **not** trigger refresh while the JWT is still valid. Replace `access_token` with a non-JWT so the CLI treats the session as expired and attempts refresh.

```bash
python3 << 'PY'
import json, time
from pathlib import Path
p = Path.home() / ".dcm" / "tokens.json"
d = json.loads(p.read_text())
iu = "http://keycloak:8080/realms/dcm"
d[iu]["refresh_token"] = "invalid-refresh-token"
d[iu]["access_token"] = "not-a-jwt"  # forces IsExpired; JWT exp would ignore backdated expiry
d[iu]["expiry"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 60))
p.write_text(json.dumps(d))
print("Corrupted refresh token and invalidated access token")
PY
```

**Step 3: Run an authenticated command (keep file-store force)**

```bash
DBUS_SESSION_BUS_ADDRESS= dcm policy list --control-plane-url http://localhost:8080 2>&1
```

**Expected:** Command fails with exit code 1. Error includes an instruction to run `dcm login` and the OAuth refresh failure, for example:

```text
failed to connect to control plane at http://localhost:8080: Get "http://localhost:8080/api/v1alpha1/policies": authentication expired, run 'dcm login' to re-authenticate: oauth2: "invalid_grant" "Invalid refresh token"
```

Do **not** treat a bare HTTP 401 from the control plane as a pass for this case - that means refresh was never attempted (for example only `expiry` was backdated while the JWT was still valid).

#### Cleanup

```bash
dcm logout || true
rm -f ~/.dcm/tokens.json
```

---



## Unit Test Coverage

The following unit tests (in the CLI repository, when the OIDC implementation is present) validate the auth package without requiring a live Keycloak instance:


| Test File                         | Count | Coverage Area                                                                                          |
| --------------------------------- | ----- | ------------------------------------------------------------------------------------------------------ |
| `internal/auth/auth_test.go`      | 8     | Device flow with mock OIDC server, revocation, username extraction                                     |
| `internal/auth/token_test.go`     | 22    | TokenStore save/load/delete, file permissions, inaccessible-path errors, issuer normalization, IsExpired |
| `internal/auth/transport_test.go` | 9     | Static token injection, stored token, refresh, passthrough, HTTP warning                               |

### FileStore inaccessible paths (`token_test.go`)

Negative-path coverage for a pre-existing inaccessible store dir/file. These are unit-only (via `NewFileStoreWithDir` temp dirs); TC-13 already covers the happy-path create-on-save modes (`0700` / `0600`). Do not exercise by chmod'ing `~/.dcm` in manual runs - that also blocks `config.yaml`.

| Spec | Setup | Expected error substring |
| ---- | ----- | ------------------------ |
| Save cannot write to an unwritable store directory | Store dir mode `0555` (readable so `readAll` sees `IsNotExist`, not writable) | `writing token file` |
| Save cannot create the store directory | Parent dir mode `0555`; store path is `parent/nested` | `creating token directory` |
| Load and Save cannot read an unreadable `tokens.json` | `tokens.json` mode `000` | `reading token file` |

Specs skip when `os.Geteuid() == 0` (root ignores mode bits). Mode `000` on the store/parent dir is not used for the write/create cases: `Save` calls `readAll` first and would fail with `reading token file` before `writeAll`.

Run from the CLI repository with:

```bash
make test
# or narrowly:
go test ./internal/auth/ -args -ginkgo.focus='Inaccessible paths'
```

**Expected:** Auth-related specs pass. Exact suite counts may change as the implementation evolves.

---



## Test Case Dependency Graph

```
Global Setup (OIDC-capable install, auth-enabled stack)
    |
    +-- TC-05 (login requires issuer-url)
    |
    +-- TC-09 (no auth / AUTH_DISABLED stack) --- restore auth-enabled after
    |
    +-- TC-01 (interactive login) --+-- TC-02 (authenticated API call)
    |                               |
    |                               +-- TC-03 (token auto-refresh)
    |                               |
    |                               +-- TC-04 (logout + soft passthrough) --- TC-10 (logout when not logged in)
    |                               |
    |                               +-- TC-08 (HTTP scheme warning)
    |                               |
    |                               +-- TC-11 (config persistence)
    |                               |
    |                               +-- TC-16 (token redaction)
    |                               |
    |                               +-- TC-17 (concurrent invocations)
    |
    +-- TC-13 (file-store permissions; may use forced fallback login)
    |
    +-- TC-19 (OIDC refresh failure -> dcm login)
    |
    +-- TC-06 (static DCM_TOKEN) ---+-- TC-07 (static overrides stored)
    |                               |
    |                               +-- TC-15 (expired static token)
    |
    +-- TC-12 (DCM_ISSUER_URL env var)
    |
    +-- TC-14 (login 5-min timeout)
    |
    +-- TC-18 (config merge on login)
```

---



## Risk Observations

1. **OIDC-capable release required**: Not every GitHub release includes authentication. Confirm `dcm login --help` before execution.
2. **Keyring availability in CI/containers**: Keyring probe may fail in headless environments. File fallback activates automatically, but first invocation has a small delay from the probe. File-only assertions (TC-13, TC-19, optional TC-03/17 checks) must force or detect file fallback. Confirm file store by removing `~/.dcm/tokens.json` and re-running a command - a 401 / missing auth (both with and without `DBUS_SESSION_BUS_ADDRESS=`) means file store is live; success without the env var means keyring still holds credentials.
3. **Token in process listing**: `--token <jwt>` flag value is visible in `/proc/<pid>/cmdline`. Prefer `DCM_TOKEN` env var for sensitive environments. Environment variables are also visible to the same user via `/proc/<pid>/environ` but require same-UID access.
4. **No HTTPS enforcement on issuer URL**: The CLI does not reject `http://` issuer URLs. In production, Keycloak should always be behind TLS. The CLI warns about HTTP for API calls but not for the issuer URL itself.
5. **Single-process refresh lock**: The `sync.Mutex` in `AuthTransport` only protects against concurrent goroutines within a single process. Multiple `dcm` processes may race on refresh token usage if Keycloak has `revoke-refresh-token=true`.
6. **Browser auto-open**: `xdg-open` / `open` / `cmd start` may fail in headless environments. The CLI prints the URL to stderr as fallback.
7. **Issuer hostname**: Discovery `issuer` must match `--issuer-url`. The auth Compose model advertises `http://keycloak:8080/realms/dcm`. Keep that issuer for control-plane reachability; add a host `/etc/hosts` entry so the CLI can resolve `keycloak`. Do not override `KC_HOSTNAME` to `localhost:8180` - the control-plane container cannot use that issuer.
8. **TC-19 expiry field is insufficient**: `IsExpired` reads JWT `exp` from `access_token` before `TokenData.Expiry`. TC-19 must invalidate `access_token` (or wait for real JWT expiry) or refresh is never attempted.

---



## Version History


| Version | Date       | Changes                                                                                                                                                                 |
| ------- | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1.4     | 2026-08-05 | Unit coverage: FileStore inaccessible-path specs (unwritable dir, uncreatable nested dir, unreadable tokens.json); refresh auth unit counts |
| 1.3     | 2026-08-05 | TC-19: invalidate access_token (JWT exp preferred over stored expiry); document observed invalid_grant error |
| 1.2     | 2026-08-05 | Align issuer with stock compose: use `http://keycloak:8080/realms/dcm` + host `/etc/hosts`; document why `localhost:8180` KC_HOSTNAME override fails for control-plane  |
| 1.1     | 2026-08-03 | Auditor fixes: OIDC-capable install gate (no branch/PR pins), issuer matching, dual auth stack for TC-09, storage-aware assertions, TC-19 refresh failure, AC alignment |
| 1.0     | 2026-07-22 | Initial test plan: 18 test cases covering login, logout, token storage, auto-refresh, static token, config persistence, security, edge cases                            |


