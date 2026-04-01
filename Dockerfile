# Stage 1: Build Go agent
FROM golang:1.22-alpine AS agent-build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${BUILD_VERSION}" -o caravee-agent ./cmd/agent

# Stage 2: Runtime — Camel Quarkus + Agent
FROM eclipse-temurin:21-jre-alpine

RUN apk add --no-cache curl

# Caravee Agent (Go binary — ~10MB)
COPY --from=agent-build /build/caravee-agent /usr/local/bin/caravee-agent

# Camel Quarkus runtime
# TODO: Copy pre-built Camel Quarkus app from separate build
# COPY --from=camel-build /app/target/quarkus-app /opt/camel

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME /data
EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
