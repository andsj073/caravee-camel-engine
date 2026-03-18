#!/bin/sh
set -e

echo "🐪 Caravee Engine starting..."

mkdir -p /data/routes

# Start Camel Quarkus runtime in background (if available)
if [ -f /opt/camel/quarkus-run.jar ]; then
  echo "Starting Camel Quarkus runtime..."
  java \
    -Dquarkus.http.port="${CARAVEE_CAMEL_PORT:-8090}" \
    -jar /opt/camel/quarkus-run.jar &
  CAMEL_PID=$!

  echo "Waiting for Camel runtime..."
  CAMEL_URL="${CARAVEE_CAMEL_URL:-http://localhost:8090}"
  TIMEOUT=90
  ELAPSED=0
  until curl -sf "${CAMEL_URL}/observe/health/live" > /dev/null 2>&1; do
    sleep 1
    ELAPSED=$((ELAPSED + 1))
    if [ $ELAPSED -ge $TIMEOUT ]; then
      echo "ERROR: Camel runtime did not start within ${TIMEOUT}s"
      exit 1
    fi
  done
  echo "✅ Camel runtime ready (${ELAPSED}s)"
else
  echo "⚠ No Camel runtime found — running in agent-only mode"
fi

# Start Go agent (foreground — container lifecycle follows agent)
exec caravee-engine \
  -data-dir "${CARAVEE_DATA_DIR:-/data}" \
  -routes-dir "${CARAVEE_ROUTES_DIR:-/data/routes}" \
  -camel-url "${CARAVEE_CAMEL_URL:-http://localhost:8090}" \
  "$@"
