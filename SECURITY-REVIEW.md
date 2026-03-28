# Security Review Report — Caravee Platform

**Date:** 2026-03-28
**Scope:** `andsj073/caravee` (cloud backend, frontend, agents, engine) and `andsj073/caravee-camel-agent` (on-prem Go agent)
**Review Type:** Static analysis and architecture review

---

## Executive Summary

Caravee is an AI-native Integration Platform-as-a-Service (iPaaS) with a cloud backend (Python/FastAPI), React frontend, Java/Quarkus Camel engine, and a Go-based on-premises agent. The platform demonstrates solid foundational security practices — parameterized SQL queries, JWT authentication, RSA-OAEP encryption for secrets, and proper separation of concerns. However, several issues need attention before production deployment, with the most critical being: wildcard CORS with credentials, tenant isolation bypass via header override, verbose error information leakage, missing rate limiting, and subprocess usage in the sandbox runner.

**Finding Summary:**

| Severity | Count |
|----------|-------|
| CRITICAL | 2 |
| HIGH     | 4 |
| MEDIUM   | 8 |
| LOW      | 5 |
| INFO     | 3 |

---

## CRITICAL Findings

### C1: Wildcard CORS with Credentials Enabled

**Location:** `backend/app/main.py:43-49`, `agents/main.py:54`

```python
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)
```

**Impact:** Combining `allow_origins=["*"]` with `allow_credentials=True` allows any website to make authenticated cross-origin requests to the API. An attacker can host a malicious page that makes API calls using a victim's session/JWT cookie, effectively enabling cross-site request forgery for all endpoints.

**Recommendation:** Replace `"*"` with an explicit allowlist of trusted origins (e.g., `["https://app.caravee.io"]`). Use an environment variable for configurability across environments.

---

### C2: Tenant Isolation Bypass via X-Tenant-ID Header

**Location:** `backend/app/middleware/auth.py:50-52`

```python
if x_tenant_id:
    user.tenant_id = x_tenant_id
```

**Impact:** Any authenticated user can override their tenant context by sending an `X-Tenant-ID` header with any tenant ID. This bypasses multi-tenant isolation entirely — a user in Tenant A can access Tenant B's integrations, engines, secrets, and chat sessions simply by setting the header. There is no validation that the user actually belongs to the requested tenant.

**Recommendation:** Validate that the user has an active `tenant_members` record for the requested tenant before accepting the override. Reject unauthorized tenant switches with a 403.

---

## HIGH Findings

### H1: Verbose Error Information Leakage

**Location:** `backend/app/main.py:88-94` and 20+ route files

```python
@app.exception_handler(Exception)
async def global_exception_handler(request: Request, exc: Exception):
    return JSONResponse(
        status_code=500,
        content={"error": "Internal server error", "detail": str(exc)},
    )
```

**Impact:** Internal exception details (including stack traces, SQL errors, file paths, and configuration details) are returned to the client in the `detail` field. This information disclosure aids attackers in understanding the system internals. The same pattern is repeated in individual routes (e.g., `kamelet_integrations.py:438`, `hosted_engines.py:75`, `engines.py:280`).

**Recommendation:** Return generic error messages to clients. Log the full exception server-side. Use error codes instead of raw exception strings.

---

### H2: No Rate Limiting on Any Endpoint

**Location:** Global (no rate limiting middleware found)

**Impact:** All endpoints — including authentication (`/api/v1/auth/magic-link/request`), AI chat (`/api/v1/chat/message`), and deployment (`/api/v1/integrations/{id}/deploy`) — have no rate limiting. This enables:
- Brute-force attacks on magic link tokens
- API abuse and cost escalation via AI chat (Anthropic API calls)
- Denial of service through resource exhaustion

**Recommendation:** Add rate limiting middleware (e.g., `slowapi` or a custom middleware). Priority endpoints:
- Auth endpoints: 5 req/min per IP
- AI chat: 20 req/min per user
- Deployment: 10 req/min per user
- Global: 100 req/min per IP

---

### H3: Subprocess Execution in Sandbox Runner

**Location:** `backend/app/services/sandbox_runner.py:341-346`, `backend/app/services/kamelet_tools.py:318-360`

```python
proc = await asyncio.create_subprocess_exec(
    *cmd,
    stdout=asyncio.subprocess.PIPE,
    stderr=asyncio.subprocess.PIPE,
    env=env,
)
```

**Impact:** The sandbox runner executes `camel-jbang` as a subprocess. While `create_subprocess_exec` is used (avoiding shell injection), the YAML content being run is AI-generated and could potentially include malicious Camel routes that access the host filesystem, make network calls, or consume excessive resources. The `kamelet_tools.py` fallback also uses `subprocess.run` directly.

**Recommendation:**
- Always run sandboxed execution inside a Docker container with resource limits (CPU, memory, network isolation)
- Never fall back to local subprocess execution in production
- Apply filesystem and network restrictions to the sandbox environment
- Set strict timeouts (already partially implemented with `--max-seconds=15`)

---

### H4: Docker Agent Runs as Root

**Location:** `caravee-camel-agent/docker-compose.yml` (agent service)

```yaml
agent:
  user: "0"  # run as root
```

**Impact:** If the agent container is compromised, the attacker gains root-level access. Combined with volume mounts, this could allow host filesystem access and privilege escalation.

**Recommendation:** Use a non-root user. The `Dockerfile.agent` already specifies `USER 65532` (nonroot) — ensure docker-compose matches. Pre-create the `/data` volume with correct ownership.

---

## MEDIUM Findings

### M1: Ephemeral JWT Secret on Restart

**Location:** `backend/app/services/auth_service.py:23-29`

```python
_jwt_secret_from_env = os.getenv("JWT_SECRET")
if not _jwt_secret_from_env:
    _jwt_secret_from_env = secrets.token_hex(32)
```

**Impact:** If `JWT_SECRET` is not set, a random secret is generated at startup. All existing JWTs are invalidated on restart, and if multiple backend instances run concurrently, they will have different secrets, breaking authentication. While a warning is logged, this should fail loudly in production.

**Recommendation:** Require `JWT_SECRET` in production. Raise an error on startup if not set, rather than falling back to an ephemeral secret.

---

### M2: WebSocket Authentication Gap — Agent Connection

**Location:** `caravee-camel-agent/internal/cloud/connection.go`

```go
header.Set("X-Engine-ID", c.identity.EngineID)
header.Set("X-Tenant-ID", c.cfg.TenantID)
ws, _, err := websocket.DefaultDialer.Dial(c.cfg.WSSURL, header)
```

**Impact:** The agent authenticates to the cloud WebSocket using only engine ID and tenant ID headers — no cryptographic proof of identity, no signed tokens, no mutual TLS. If an attacker discovers an engine ID and tenant ID, they could impersonate the engine and receive deployment commands including encrypted secrets.

**Recommendation:** Implement challenge-response authentication using the engine's RSA keypair, or issue short-lived tokens during the pairing flow.

---

### M3: Unvalidated Path Components in Route Deployment

**Location:** `caravee-camel-agent/internal/deploy/deployer.go`

```go
safeID := strings.ReplaceAll(routeID, ".", "-")
filename := safeID + ".yaml"
filePath := filepath.Join(d.routesDir, filename)
```

**Impact:** Route IDs are only sanitized for dots. A crafted route ID containing `../` could write files outside the intended routes directory. Similarly, kamelet names extracted from YAML content are used directly in file paths.

**Recommendation:** Use `filepath.Base()` on the filename to strip directory components. Validate IDs against a strict pattern: `^[a-zA-Z0-9_-]+$`.

---

### M4: No WebSocket Message Size Limits

**Location:** `caravee-camel-agent/internal/cloud/connection.go`

```go
_, data, err := ws.ReadMessage()  // No size limit
```

**Impact:** The agent reads WebSocket messages without size restrictions. A malicious or compromised cloud server could send arbitrarily large messages, causing memory exhaustion and denial of service on the agent.

**Recommendation:** Set `ws.SetReadLimit(maxMessageSize)` to a reasonable value (e.g., 10 MB).

---

### M5: Secret Variable Names Leaked in Logs

**Location:** `caravee-camel-agent/internal/deploy/deployer.go`

```go
slog.Warn("Secret not found, leaving placeholder", "var", varName)
```

**Impact:** Secret variable names are logged in plaintext. While not the secret values themselves, variable names can reveal what services are integrated and what types of credentials exist, aiding reconnaissance.

**Recommendation:** Log a hash or masked version of variable names at WARN level. Only log full names at DEBUG level.

---

### M6: No Concurrency Limits on Message Processing

**Location:** `caravee-camel-agent/internal/cloud/connection.go`

```go
go c.handleMessage(msg)  // Unbounded goroutine spawning
```

**Impact:** Each incoming WebSocket message spawns a new goroutine without limits. A flood of messages could exhaust memory and CPU, causing denial of service.

**Recommendation:** Use a bounded worker pool (e.g., `semaphore` channel) to limit concurrent message processing.

---

### M7: Secrets File Permissions Not Verified

**Location:** `caravee-camel-agent/internal/deploy/secrets.go`

**Impact:** The `secrets.env` file containing plaintext secrets is read without verifying its file permissions. If the file is world-readable (e.g., 0644 instead of 0600), other processes on the host can read all secrets.

**Recommendation:** Check file permissions on load and warn/fail if the file is group or world-readable.

---

### M8: Plaintext Secrets in Deployed YAML

**Location:** `caravee-camel-agent/internal/deploy/deployer.go`

**Impact:** After secret resolution, plaintext secrets are written directly into route YAML files on disk (`0644` permissions). Any process or user that can read the routes directory can extract all secrets.

**Recommendation:** Use `0640` or `0600` permissions for route files. Consider using Camel's built-in property placeholder mechanism with a secrets manager instead of inline substitution.

---

## LOW Findings

### L1: JWT Token Expiry Too Long

**Location:** `backend/app/services/auth_service.py:32`

```python
SESSION_TOKEN_EXPIRY_HOURS = 24 * 7  # 7 days
```

**Impact:** Tokens are valid for 7 days. Compromised tokens have a long exploitation window. No refresh token mechanism exists, so there's no way to revoke individual sessions.

**Recommendation:** Reduce to 24 hours and implement refresh tokens. Consider a token revocation list for compromised tokens.

---

### L2: RSA-2048 Key Size at Minimum Threshold

**Location:** `caravee-camel-agent/internal/pairing/pairing.go`

```go
const KeySize = 2048
```

**Impact:** While 2048-bit RSA is still considered acceptable, it is at the minimum recommendation threshold. No key rotation mechanism exists.

**Recommendation:** Consider upgrading to 4096-bit RSA or ECDSA P-256/P-384 for new deployments. Implement key rotation.

---

### L3: No HTTPS Enforcement

**Location:** Global (no TLS configuration in backend or agents)

**Impact:** The application relies entirely on a reverse proxy for TLS termination. If misconfigured, traffic could flow in plaintext, exposing JWTs, secrets, and user data.

**Recommendation:** Add HSTS headers. Consider adding a check that rejects non-HTTPS requests in production mode.

---

### L4: `yaml.load()` in Frontend Without SafeLoader

**Location:** `frontend/src/pages/IntegrationDesigner.tsx:441,476`

```typescript
const parsed = yaml.load(yamlStr) as any
```

**Impact:** The `js-yaml` library's `yaml.load()` is safe by default in v4+ (unlike Python's PyYAML). Low risk, but worth noting for consistency.

**Recommendation:** Use `yaml.safeLoad()` or verify js-yaml version is >= 4.0 (confirmed: 4.1.1).

---

### L5: No Audit Logging for Security Events

**Location:** Global

**Impact:** While an `audit_events` table exists, security-critical events (failed authentication, tenant switch attempts, deployment commands) don't appear to be consistently logged.

**Recommendation:** Ensure all authentication failures, role changes, tenant switches, and deployment operations are audit-logged with actor, IP, and timestamp.

---

## INFO Findings

### I1: Development Docker Compose Exposes Database

The `docker-compose.yml` files expose database ports to the host. Ensure production deployments restrict database access to internal networks only.

### I2: No Content Security Policy Headers

The frontend does not set CSP headers, which could allow XSS attacks if user-generated content is rendered. No `dangerouslySetInnerHTML` usage was found (good), but CSP would add defense-in-depth.

### I3: Dependencies Are Current

All major dependencies appear to be at recent versions with no known critical CVEs as of the review date. Regular dependency scanning (e.g., `pip-audit`, `npm audit`, `govulncheck`) should be automated in CI.

---

## Positive Security Practices Observed

- **Parameterized SQL queries** throughout — no string interpolation in SQL
- **RSA-OAEP-SHA256** encryption for secrets with private key kept on-premises
- **Pydantic validation** on all API request models
- **`create_subprocess_exec`** used instead of shell execution (avoids shell injection)
- **Magic link tokens** use `secrets.token_urlsafe(32)` with 15-minute expiry
- **One-time magic links** tracked with `used_at` field
- **Soft-delete pattern** for integrations (recoverable)
- **No hardcoded secrets** in source code
- **`.gitignore`** properly excludes `.env` files
- **Structured logging** with configurable levels
- **Minimal Go dependencies** in agent (2 external packages — small attack surface)

---

## Remediation Priority

| Priority | Finding | Effort |
|----------|---------|--------|
| 1 | C1: Fix CORS configuration | Low |
| 2 | C2: Validate tenant membership on X-Tenant-ID override | Low |
| 3 | H2: Implement rate limiting | Medium |
| 4 | H1: Remove error details from API responses | Low |
| 5 | H3: Enforce Docker-only sandbox execution | Medium |
| 6 | H4: Fix Docker Compose to use non-root | Low |
| 7 | M1: Require JWT_SECRET in production | Low |
| 8 | M2: Add cryptographic WSS auth for agents | Medium |
| 9 | M3: Validate path components in agent deployer | Low |
| 10 | M4-M8: Remaining medium findings | Medium |
