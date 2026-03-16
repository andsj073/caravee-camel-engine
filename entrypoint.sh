#!/bin/sh
set -e

echo "🐪 Caravee Engine starting..."

# Start Camel runtime in background (if available)
if [ -f /opt/camel/quarkus-run.jar ]; then
  echo "Starting Camel Quarkus runtime..."
  java -jar /opt/camel/quarkus-run.jar &
  CAMEL_PID=$!

  # Wait for Camel health endpoint
  echo "Waiting for Camel runtime..."
  HEALTH_URL="${CARAVEE_HEALTH_URL:-http://localhost:8080/q/health}"
  TIMEOUT=60
  ELAPSED=0
  until curl -sf "${HEALTH_URL}/live" > /dev/null 2>&1; do
    sleep 1
    ELAPSED=$((ELAPSED + 1))
    if [ $ELAPSED -ge $TIMEOUT ]; then
      echo "ERROR: Camel runtime did not start within ${TIMEOUT}s"
      exit 1
    fi
  done
  echo "Camel runtime ready (${ELAPSED}s)"
else
  echo "WARNING: No Camel runtime found at /opt/camel/quarkus-run.jar"
  echo "Running agent in standalone mode (for development)"
fi

# Start agent (foreground — container lifecycle tied to agent)
exec caravee-agent "$@"
