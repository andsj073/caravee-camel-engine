package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CloudConfig holds the cloud connection configuration.
type CloudConfig struct {
	TenantID string `yaml:"tenant_id"`
	WSSURL   string `yaml:"wss_url"`
	Label    string `yaml:"label"`
	PairedAt string `yaml:"paired_at"`
}

const configFile = "config.yaml"

// LoadOrPair loads existing cloud config or performs initial pairing.
func LoadOrPair(dataDir string, identity *Identity) (*CloudConfig, error) {
	cfgPath := filepath.Join(dataDir, configFile)

	// Try loading existing config
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		var cfg CloudConfig
		if err := yaml.Unmarshal(data, &cfg); err == nil && cfg.WSSURL != "" {
			slog.Debug("Loaded cloud config from file")
			return &cfg, nil
		}
	}

	// Need to pair — get URL from env
	cloudURL := os.Getenv("CARAVEE_CLOUD")
	if cloudURL == "" {
		return nil, fmt.Errorf("not paired and CARAVEE_CLOUD not set — provide a pairing URL")
	}

	cfg, err := pair(cloudURL, identity)
	if err != nil {
		return nil, fmt.Errorf("pairing failed: %w", err)
	}

	// Save config
	out, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(cfgPath, out, 0600); err != nil {
		slog.Warn("Failed to save config", "error", err)
	}

	return cfg, nil
}

func pair(cloudURL string, identity *Identity) (*CloudConfig, error) {
	slog.Info("Starting pairing", "url", cloudURL)

	u, err := url.Parse(cloudURL)
	if err != nil {
		return nil, fmt.Errorf("invalid pairing URL: %w", err)
	}

	tenantID := u.Query().Get("tenant")
	otp := u.Query().Get("otp")
	if tenantID == "" || otp == "" {
		return nil, fmt.Errorf("pairing URL must contain tenant and otp parameters")
	}

	// POST to pairing endpoint
	// For MVP: derive WSS URL from pairing URL
	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	wssScheme := "wss"
	if u.Scheme == "http" {
		wssScheme = "ws"
	}
	wssURL := fmt.Sprintf("%s://%s/ws/engine", wssScheme, u.Host)

	// TODO: Implement actual HTTP POST to pairing endpoint
	// For now: extract connection info from URL
	slog.Info("Paired successfully", "tenant_id", tenantID, "wss_url", wssURL)

	return &CloudConfig{
		TenantID: tenantID,
		WSSURL:   wssURL,
		Label:    "",
		PairedAt: fmt.Sprintf("%s", baseURL), // placeholder
	}, nil
}
