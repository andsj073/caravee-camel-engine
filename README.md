# Caravee Engine

A lightweight agent that turns any Apache Camel runtime into a managed engine in the [Caravee](https://caravee.io) integration platform.

## Quick Start

```bash
docker run -d \
  -e CARAVEE_CLOUD=https://api.caravee.io/pair?tenant=abc&otp=x7k9m2 \
  -v caravee-data:/data \
  ghcr.io/caravee/engine:latest
```

That's it. Your Camel runtime is now managed by Caravee Cloud.

## What It Does

The Caravee Engine agent is a small Go binary that runs alongside Apache Camel. It:

- **Connects** to Caravee Cloud via secure WebSocket
- **Deploys** integration routes by writing Camel YAML files (Camel's file watcher hot-reloads them)
- **Undeploys** by removing route files
- **Reports health** by polling Camel's MicroProfile Health endpoints
- **Manages secrets** — decrypts cloud-provided secrets with the engine's RSA private key

The agent is **headless** — no UI, no HTTP endpoints for humans. All management happens through Caravee Cloud.

## How It Works

```
Caravee Cloud ◄──WSS──► Caravee Agent ──file──► Apache Camel
                              │                      │
                              ▼                      ▼
                         /data/routes/         Route execution
                         /data/secrets.env     Hot-reload
```

1. Agent connects to cloud via WebSocket
2. Cloud sends deploy commands with route definitions
3. Agent writes YAML files to `/data/routes/`
4. Camel's file watcher detects changes and hot-reloads routes
5. Agent polls `/q/health` to verify routes are running
6. Agent reports status back to cloud

## Configuration

| Environment Variable | Required | Default | Description |
|---------------------|----------|---------|-------------|
| `CARAVEE_CLOUD` | Yes (first boot) | — | Pairing URL from Caravee Cloud |
| `CARAVEE_ROUTES_DIR` | No | `/data/routes` | Route YAML output directory |
| `CARAVEE_DATA_DIR` | No | `/data` | Base data directory |
| `CARAVEE_HEALTH_URL` | No | `http://localhost:8080/q/health` | Camel health endpoint |
| `CARAVEE_LOG_LEVEL` | No | `info` | Log level (debug/info/warn/error) |

## Local Secrets

Mount a secrets file for credentials that should never leave the engine:

```bash
docker run -d \
  -e CARAVEE_CLOUD=https://... \
  -v caravee-data:/data \
  -v ./secrets.env:/data/secrets.env:ro \
  ghcr.io/caravee/engine:latest
```

```env
# secrets.env
SALESFORCE_CLIENT_ID=abc123
SALESFORCE_CLIENT_SECRET=s3cr3t
```

Local secrets take priority over cloud-provided encrypted secrets.

## Architecture

The agent is designed to work with **any** Apache Camel distribution that supports:
- File-based route loading (YAML)
- MicroProfile Health endpoints (`/q/health`)

Default: Camel Quarkus. But Camel Spring Boot, Camel K, or standalone Camel work too.

## Building

```bash
# Build the agent
go build -o caravee-agent ./cmd/agent

# Build the Docker image
docker build -t caravee-engine:latest .

# Run tests
go test ./...
```

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Configuration](docs/CONFIGURATION.md)
- [Secrets Management](docs/SECRETS.md)
- [Development Guide](docs/DEVELOPMENT.md)
- [WSS Protocol](docs/PROTOCOL.md)

## License

Business Source License 1.1 — converts to Apache License 2.0 on 2029-03-16.

See [LICENSE](LICENSE) for details.
