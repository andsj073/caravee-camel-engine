#!/bin/sh
set -e
echo "🐪 Caravee Engine (Go Agent + Camel) starting..."

# Start the existing Camel Quarkus app in background
# The existing caravee-engine image runs Camel via its own entrypoint
# We need to find the Quarkus jar
CAMEL_JAR="/app/quarkus-app/quarkus-run.jar"

if [ -f "$CAMEL_JAR" ]; then
  echo "Starting Camel Quarkus: $CAMEL_JAR"
  java -XX:+UseContainerSupport -XX:MaxRAMPercentage=75.0 -jar "$CAMEL_JAR" &
  CAMEL_PID=$!

  # Wait for health
  echo "Waiting for Camel runtime..."
  HEALTH_URL="${CARAVEE_HEALTH_URL:-http://localhost:8090/health}"
  TIMEOUT=120
  ELAPSED=0
  until curl -sf "${HEALTH_URL}/live" > /dev/null 2>&1; do
    sleep 2
    ELAPSED=$((ELAPSED + 2))
    if [ $ELAPSED -ge $TIMEOUT ]; then
      echo "ERROR: Camel did not start in ${TIMEOUT}s"
      exit 1
    fi
  done
  echo "✅ Camel ready (${ELAPSED}s)"
else
  echo "⚠️  No Camel JAR found — running agent standalone"
fi

# Start Go agent in foreground
exec caravee-agent "$@"
