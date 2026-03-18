package deploy

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SecretManager provides access to local secrets from /data/secrets.env
type SecretManager struct {
	dataDir string
	secrets map[string]string
	mu      sync.RWMutex
	loaded  bool
}

// NewSecretManager creates a secret manager reading from the data directory.
func NewSecretManager(dataDir string) *SecretManager {
	sm := &SecretManager{
		dataDir: dataDir,
		secrets: make(map[string]string),
	}
	sm.load()
	return sm
}

// Get returns a secret value by name. Returns (value, true) if found.
func (sm *SecretManager) Get(name string) (string, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	v, ok := sm.secrets[name]
	return v, ok
}

// Reload re-reads the secrets file.
func (sm *SecretManager) Reload() {
	sm.load()
}

func (sm *SecretManager) load() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	secretsFile := filepath.Join(sm.dataDir, "secrets.env")
	f, err := os.Open(secretsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("Failed to read secrets file", "error", err)
		}
		sm.loaded = true
		return
	}
	defer f.Close()

	sm.secrets = make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		// Strip surrounding quotes
		value = strings.Trim(value, `"'`)
		sm.secrets[key] = value
	}

	slog.Info("Loaded secrets", "count", len(sm.secrets), "file", secretsFile)
	sm.loaded = true
}

// ListKeys returns all secret var names (not values).
func (sm *SecretManager) ListKeys() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	keys := make([]string, 0, len(sm.secrets))
	for k := range sm.secrets {
		keys = append(keys, k)
	}
	return keys
}
