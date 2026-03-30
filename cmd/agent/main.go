package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caravee/engine/internal/camel"
	"github.com/caravee/engine/internal/cloud"
	"github.com/caravee/engine/internal/config"
	"github.com/caravee/engine/internal/deploy"
)

var version = "dev"

func main() {
	dataDir  := flag.String("data-dir",  envOrDefault("CARAVEE_DATA_DIR",  "/data"),                    "Base data directory")
	routesDir := flag.String("routes-dir", envOrDefault("CARAVEE_ROUTES_DIR", "/data/routes"),            "Route YAML output directory")
	camelURL := flag.String("camel-url", envOrDefault("CARAVEE_CAMEL_URL", "http://localhost:8090"),     "Camel sidecar base URL")
	logLevel := flag.String("log-level", envOrDefault("CARAVEE_LOG_LEVEL", "info"),                      "Log level (debug/info/warn/error)")
	flag.Parse()

	setupLogging(*logLevel)
	slog.Info("Caravee Engine Agent starting", "version", version, "data_dir", *dataDir, "camel_url", *camelURL)

	identity, err := config.LoadOrCreateIdentity(*dataDir)
	if err != nil {
		slog.Error("Failed to initialize identity", "error", err)
		os.Exit(1)
	}
	slog.Info("Engine identity", "engine_id", identity.EngineID)

	cfg, err := config.LoadOrPair(*dataDir, identity)
	if err != nil {
		slog.Error("Failed to configure cloud connection", "error", err)
		os.Exit(1)
	}
	slog.Info("Cloud connection configured", "tenant_id", cfg.TenantID, "wss_url", cfg.WSSURL)

	secretMgr    := deploy.NewSecretManager(*dataDir)
	deployer     := deploy.NewDeployer(*routesDir, secretMgr, *dataDir)
	camelClient  := camel.New(*camelURL)

	conn := cloud.NewConnection(cfg, identity, deployer, camelClient, version)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("Received signal, shutting down", "signal", sig)
		conn.Close()
		os.Exit(0)
	}()

	if err := conn.Run(); err != nil {
		slog.Error("Cloud connection failed permanently", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func setupLogging(level string) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(handler))
	fmt.Fprintf(os.Stderr, "🐪 Caravee Engine Agent %s\n", version)
}
