package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"github.com/caravee/engine/internal/pairing"
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

	otp := u.Query().Get("otp")
	if otp == "" {
		return nil, fmt.Errorf("pairing URL must contain otp parameter")
	}

	// Generate keypair (if not exists)
	dataDir := filepath.Dir(filepath.Join(".", configFile))
	if err := generateKeypair(dataDir); err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	// Load public key
	pubKey, err := loadPublicKey(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load public key: %w", err)
	}

	// Call pairing endpoint (same base URL as pairing link)
	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	pairEndpoint := baseURL + "/api/v1/pairing/pair"

	slog.Info("Calling pairing endpoint", "url", pairEndpoint, "otp", otp)
	pairResp, err := callPairingEndpoint(pairEndpoint, otp, pubKey)
	if err != nil {
		return nil, fmt.Errorf("pairing request failed: %w", err)
	}

	// Update engine ID in identity file (cloud assigns the final ID)
	identity.EngineID = pairResp.EngineID
	if err := SaveIdentity(dataDir, identity); err != nil {
		slog.Warn("Failed to update engine ID", "error", err)
	}

	slog.Info("Pairing successful", "engine_id", pairResp.EngineID, "tenant_id", pairResp.TenantID)

	return &CloudConfig{
		TenantID: pairResp.TenantID,
		WSSURL:   pairResp.WSSURL,
		Label:    pairResp.Label,
		PairedAt: "",  // TODO: timestamp
	}, nil
}

func generateKeypair(dataDir string) error {
	return pairing.GenerateKeypair(dataDir)
}

func loadPublicKey(dataDir string) (string, error) {
	return pairing.LoadPublicKey(dataDir)
}

func callPairingEndpoint(url, otp, publicKey string) (*pairing.PairResponse, error) {
	return pairing.Pair(url, otp, publicKey)
}
