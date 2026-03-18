# caravee-camel-agent

The Caravee engine agent — a lightweight Go binary that bridges your Camel runtime with the Caravee cloud.

## What this does

- **Pairs** with the Caravee cloud via a one-time token (OTP)
- **Deploys** integration YAML files to the Camel sidecar's routes directory
- **Monitors** Camel metrics and pushes error events to cloud
- **Executes** route commands (suspend/resume) via the Camel management API

## What this does NOT do

No integration logic. No data processing. No business code.  
~2000 lines of Go — audit it yourself in an evening.

## Architecture

```
┌─────────────────────────────────────────┐
│  Your host / container                  │
│                                         │
│  ┌──────────────────────────────┐       │
│  │  caravee-camel-runtime       │ :8090 │  ← Apache Foundation image
│  │  (Camel Quarkus)             │       │
│  └──────────────────────────────┘       │
│            ↕ localhost                  │
│  ┌──────────────────────────────┐       │
│  │  caravee-camel-agent (this)  │       │  ← Our code, open source
│  │  (Go binary)                 │       │
│  └──────────────────────────────┘       │
│            ↕ WSS                        │
└─────────────────────────────────────────┘
             ↕
    Caravee Cloud (backend)
```

## Quick start

```bash
# Generate OTP in Caravee UI (Connect Engine button)
$env:CARAVEE_CLOUD='http://your-cloud/api/v1/pairing/pair?otp=XXXX-XXXX-XXXX'
docker compose up -d
```

This starts:
1. `postgres:16-alpine` — local database for JDBC sinks
2. `caravee-camel-runtime:latest` — Apache Camel Quarkus runtime
3. `caravee-camel-agent:latest` — this agent

## Images

| Image | Source | Trust |
|-------|--------|-------|
| `ghcr.io/andsj073/caravee-camel-runtime:latest` | [caravee-camel-runtime](https://github.com/andsj073/caravee-camel-runtime) | Apache artifacts from Maven Central |
| `ghcr.io/andsj073/caravee-camel-agent:agent` | This repo | ~2000 lines Go, open source |

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `CARAVEE_CLOUD` | — | Pairing URL with OTP (required on first start) |
| `CARAVEE_CAMEL_URL` | `http://localhost:8090` | Camel sidecar base URL |
| `CARAVEE_DATA_DIR` | `/data` | Agent data directory |
| `CARAVEE_ROUTES_DIR` | `/data/routes` | Route YAML hot-reload directory |
| `CARAVEE_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

## Related repos

| Repo | Description |
|------|-------------|
| [caravee-camel-runtime](https://github.com/andsj073/caravee-camel-runtime) | Apache Camel Quarkus runtime (no Caravee code) |
| [caravee](https://github.com/andsj073/caravee) | Cloud backend + frontend |
