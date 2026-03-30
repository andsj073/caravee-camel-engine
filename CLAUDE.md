# CLAUDE.md — caravee-camel-agent

Lightweight Go binary that bridges a Camel runtime with the Caravee cloud.
**~2000 lines of Go. No business logic. No data processing.**

---

## Team

- **Andreas** — Product Owner
- **Mo** — Architect + Ops (orchestrates, never codes directly)
- **Claude Code** — Dev Lead (all implementation)

---

## What this does

- **Pairs** with Caravee cloud via OTP (`internal/pairing/`)
- **Deploys** integration YAML to Camel's routes dir (`internal/deploy/`)
- **Resolves** variables: profile-key filtering, secret decryption, {{placeholder}} substitution
- **Monitors** Camel metrics via Prometheus scrape + pushes error events to cloud (`internal/monitor/`)
- **Executes** route commands: suspend/resume (`internal/cloud/`)
- **Reports** run history from local SQLite (`internal/runlog/`)

## What this does NOT do

No integration logic. No data processing. Never reads route payloads.

---

## Architecture

```
Caravee Cloud (backend)
        ↕ WSS (outbound-only, agent initiates)
caravee-camel-agent (this repo)
        ↕ localhost HTTP
caravee-camel-runtime (Apache Camel Quarkus, untouched)
        ↓ file watch
/data/routes/*.yaml   ← agent writes resolved YAML here
```

---

## Key Packages

| Package | Responsibility |
|---|---|
| `internal/cloud/` | WebSocket connection + message dispatch |
| `internal/cloud/messages.go` | All message types (cloud↔agent protocol) |
| `internal/cloud/connection.go` | WS connect, reconnect, command dispatch |
| `internal/deploy/` | Route file writer + variable resolver |
| `internal/deploy/deployer.go` | Deploy() — writes resolved YAML to /data/routes/ |
| `internal/deploy/resolver.go` | ParseProperties, ResolveProfile, ResolvePlaceholders |
| `internal/deploy/secrets.go` | SecretManager — reads /data/secrets.env |
| `internal/pairing/` | OTP pairing + RSA keypair (private key at /data/engine-key.pem) |
| `internal/pairing/decrypt.go` | DecryptSecret — RSA-OAEP decryption of cloud-sent ciphers |
| `internal/monitor/` | Prometheus scraper → Camel metrics → cloud events |
| `internal/runlog/` | SQLite run history (per-route exchange tracking) |
| `internal/config/` | Config loading (env vars, data dir) |
| `cmd/agent/main.go` | Entry point |

---

## Variable Resolution (critical — read this)

The agent resolves ALL variable placeholders BEFORE writing YAML to engine.
Camel runtime never sees unresolved placeholders.

### Two syntaxes, two systems

| | App vars | Secret vars |
|---|---|---|
| Syntax | `{{app.property}}` | `${ENV_VAR}` |
| Scope | Engine or project | Engine only (PKI-bound to engine keypair) |
| Source | Cloud DB engine_vars (is_secret=0) or project_properties | Cloud DB engine_vars (is_secret=1, RSA-encrypted cipher) |
| Delivered via | DeployMessage.Properties (plaintext map) | DeployMessage.Secrets (cipher → agent decrypts) |
| Local fallback | None — missing = deploy blocked | secrets.env or OS env (self-hosted "bring your own") |
| Future | — | HashiCorp Vault, other KV stores (planned, not yet) |

**App vars** are engine- or project-scoped non-secret config. If not set in cloud, deploy is blocked.

**Secret vars** are always engine-scoped (each engine has its own RSA keypair — you can't encrypt "for a project").
Our primary delivery mechanism is cloud-encrypted (zero-trust). secrets.env/OS env is a self-hosted convenience — we support it but don't own it.

### Profile key filtering (app vars)
- Engine has optional `profile_key` (e.g. "prod")
- Properties with `%prod.key=value` override non-prefixed `key=value` when profile_key=prod
- Resolved by agent at deploy time

---

## Build & Run

```bash
# Build
go build ./cmd/agent

# Run (dev)
BACKEND_WS_URL=ws://localhost:8100/ws/engine \
DATA_DIR=/data \
./agent

# Run tests
go test ./...
```

Key env vars:
```
BACKEND_WS_URL=ws://localhost:8100/ws/engine
DATA_DIR=/data                    # routes dir = DATA_DIR/routes/
ENGINE_PRIVATE_KEY_PATH=/data/engine-key.pem
CARAVEE_ENGINE_ID=...             # pre-set to avoid duplicate engine rows on K8s restarts
```

---

## Cloud ↔ Agent Protocol (messages.go)

Inbound from cloud:
- `deploy` — DeployMessage: integration YAML + properties + encrypted secrets
- `undeploy` — remove route files
- `suspend_route` / `resume_route` — Camel management API calls
- `check_vars` — verify which vars are available locally
- `ping` / `telemetry` / `get_engine_metrics` / `get_route_metrics`

Outbound to cloud:
- `connected` — on WS connect, includes localVars + deployed routes
- `deploy_result` — includes warnings for unresolved placeholders
- `route_error` — pushed on metric anomalies
- `run_history` / `run_event` — observability
- `pong` / telemetry responses

---

## Conventions

- No CGO dependencies
- Errors are logged with `slog` — never panics in production paths
- Secrets never logged (not even as warnings)
- All file writes are atomic where possible (write to tmp → rename)
- Agent must survive engine restarts — reconnect with backoff
- `DATA_DIR` is the single source of truth for file paths

---

## Common Gotchas

1. **Private key location:** `/data/engine-key.pem` — generated on first start if missing. Public key sent to cloud at pairing.
2. **SIGKILL tolerance:** Agent may be killed mid-deploy on K8s. Routes dir is the ground truth — reconcile on reconnect.
3. **UPPER_SNAKE to dotted:** engine_vars from cloud use UPPER_SNAKE_CASE. Normalize to dotted.lower.case before property lookup.
4. **Profile key empty:** If engine has no profile_key, only non-prefixed properties are used.
5. **secrets.env format:** KEY=value, one per line, # comments, surrounding quotes stripped.
6. **Camel routes dir:** Camel hot-reloads from `/data/routes/*.yaml` — agent just writes files, never talks to Camel directly for deploy.
