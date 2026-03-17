package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/caravee/engine/internal/config/crypto"
)

// Identity holds the stable engine identity.
type Identity struct {
	EngineID  string
	PublicKey string // PEM-encoded RSA public key
	DataDir   string
}

// LoadOrCreateIdentity loads existing identity or creates a new one.
func LoadOrCreateIdentity(dataDir string) (*Identity, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	engineID, err := loadOrCreateEngineID(dataDir)
	if err != nil {
		return nil, err
	}

	// Keypair will be created in Phase 2 (PKI)
	// For now, just return identity without keys
	return &Identity{
		EngineID: engineID,
		DataDir:  dataDir,
	}, nil
}

func loadOrCreateEngineID(dataDir string) (string, error) {
	idFile := filepath.Join(dataDir, "engine-id")

	// Check env var first
	if envID := os.Getenv("CARAVEE_ENGINE_ID"); envID != "" {
		slog.Debug("Using engine ID from env", "engine_id", envID)
		return envID, nil
	}

	// Try reading existing file
	data, err := os.ReadFile(idFile)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			slog.Debug("Loaded engine ID from file", "engine_id", id)
			return id, nil
		}
	}

	// Generate new ID
	id := generateEngineID()
	if err := os.WriteFile(idFile, []byte(id+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write engine-id: %w", err)
	}
	slog.Info("Generated new engine ID", "engine_id", id)
	return id, nil
}

func generateEngineID() string {
	// Simple UUID-based ID
	b := make([]byte, 6)
	f, _ := os.Open("/dev/urandom")
	f.Read(b)
	f.Close()
	return fmt.Sprintf("caravee-engine-%x", b)
}

// SaveIdentity updates the engine ID file (used after pairing assigns final ID).
func SaveIdentity(dataDir string, identity *Identity) error {
	idFile := filepath.Join(dataDir, "engine-id")
	return os.WriteFile(idFile, []byte(identity.EngineID+"\n"), 0600)
}

// Placeholder for crypto sub-package
var _ = crypto.Placeholder
