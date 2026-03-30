package deploy

import (
	"crypto/rsa"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caravee/engine/internal/pairing"
)

// SecretEntry describes a secret variable sent from cloud.
type SecretEntry struct {
	Var    string
	Cipher string // base64 RSA-OAEP encrypted value
	Value  string // plaintext fallback (dev mode)
}

// Deployer writes and removes route YAML files in the routes directory.
// Camel's file watcher picks up changes automatically.
type Deployer struct {
	routesDir  string
	kameletDir string // /data/kamelets — Kamelet definitions (name.kamelet.yaml)
	secrets    *SecretManager
	privKey    *rsa.PrivateKey // For decrypting cloud secrets
}

// NewDeployer creates a deployer targeting the given routes directory.
// dataDir is used to load the engine RSA private key for secret decryption.
func NewDeployer(routesDir string, secrets *SecretManager, dataDir string) *Deployer {
	// Kamelet dir is sibling of routes dir: /data/routes → /data/kamelets
	kameletDir := filepath.Join(filepath.Dir(routesDir), "kamelets")
	var privKey *rsa.PrivateKey
	if dataDir != "" {
		var err error
		privKey, err = pairing.LoadPrivateKey(dataDir)
		if err != nil {
			slog.Warn("Deployer: private key not loaded — cipher secrets unavailable", "error", err)
		}
	}
	return &Deployer{
		routesDir:  routesDir,
		kameletDir: kameletDir,
		secrets:    secrets,
		privKey:    privKey,
	}
}

// isKamelet returns true if the YAML looks like a Kamelet definition.
func isKamelet(camelYAML string) bool {
	return strings.Contains(camelYAML, "kind: Kamelet")
}

// kameletName extracts metadata.name from a Kamelet YAML.
func kameletName(camelYAML string) string {
	for _, line := range strings.Split(camelYAML, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return ""
}

// Deploy writes a YAML file — Kamelets go to /data/kamelets/, routes to /data/routes/.
// It also writes a .properties file alongside routes with merged properties and decrypted secrets.
// Returns warnings (e.g. overwritten property values) and any fatal error.
func (d *Deployer) Deploy(routeID, camelYAML string, properties map[string]string, secrets []SecretEntry) ([]string, error) {
	var warnings []string

	// Build merged properties map: start with plaintext properties, then overlay secrets.
	merged := make(map[string]string, len(properties))
	for k, v := range properties {
		merged[k] = v
	}
	for _, s := range secrets {
		key := normalizeVarKey(s.Var)
		var plaintext string
		if s.Cipher != "" {
			if d.privKey == nil {
				slog.Warn("Cannot decrypt secret — no private key", "var", s.Var)
				continue
			}
			var err error
			plaintext, err = pairing.DecryptSecret(s.Cipher, d.privKey)
			if err != nil {
				slog.Error("Failed to decrypt secret", "var", s.Var, "error", err)
				continue
			}
		} else if s.Value != "" {
			plaintext = s.Value
		} else {
			continue
		}
		merged[key] = plaintext
	}

	// Merge local secrets.env vars — cloud-decrypted values win.
	for _, key := range d.secrets.ListKeys() {
		if _, exists := merged[key]; !exists {
			if v, ok := d.secrets.Get(key); ok {
				merged[key] = v
			}
		}
	}

	// Step 1: Resolve {{property.name}} from cloud properties (engine > project scope).
	resolved, unresolved := ResolvePlaceholders(camelYAML, merged)
	for _, key := range unresolved {
		warnings = append(warnings, fmt.Sprintf("Unresolved: {{%s}} — no value set at engine or project scope", key))
	}

	// Step 2: Resolve ${SECRET_VAR} from decrypted secrets bundle + local secrets.env + OS env (self-hosted fallback).
	secretsMap := make(map[string]string)
	for _, key := range d.secrets.ListKeys() {
		if v, ok := d.secrets.Get(key); ok {
			secretsMap[key] = v
		}
	}
	// Cloud-decrypted secrets (already in merged) — add to secretsMap too
	for k, v := range merged {
		secretsMap[k] = v
	}
	resolved, unresolvedSecrets := ResolveSecretRefs(resolved, secretsMap)
	for _, key := range unresolvedSecrets {
		warnings = append(warnings, fmt.Sprintf("Unresolved: ${%s} — secret not in bundle, secrets.env, or OS env", key))
	}

	if isKamelet(resolved) {
		// Kamelet: write to /data/kamelets/name.kamelet.yaml — no .properties file needed.
		if err := os.MkdirAll(d.kameletDir, 0755); err != nil {
			return warnings, fmt.Errorf("create kamelets dir: %w", err)
		}
		name := kameletName(resolved)
		if name == "" {
			name = strings.ReplaceAll(routeID, ".", "-")
		}
		filename := name + ".kamelet.yaml"
		filePath := filepath.Join(d.kameletDir, filename)
		if err := os.WriteFile(filePath, []byte(resolved), 0644); err != nil {
			return warnings, fmt.Errorf("write kamelet file %s: %w", filename, err)
		}
		slog.Info("Kamelet deployed", "name", name, "file", filePath)
		return warnings, nil
	}

	// Integration route: write to /data/routes/
	if err := os.MkdirAll(d.routesDir, 0755); err != nil {
		return warnings, fmt.Errorf("create routes dir: %w", err)
	}
	safeID := strings.ReplaceAll(routeID, ".", "-")

	// Write .properties file if there are any properties/secrets to write.
	if len(merged) > 0 {
		propsFile := filepath.Join(d.routesDir, safeID+".properties")
		// Check existing file for overwrites and warn.
		if existing, err := readPropertiesFile(propsFile); err == nil {
			for k, newVal := range merged {
				if oldVal, ok := existing[k]; ok && oldVal != newVal {
					warnings = append(warnings, fmt.Sprintf("Overwrote %s (was '%s', now '%s')", k, oldVal, newVal))
				}
			}
		}
		if err := writePropertiesFile(propsFile, merged); err != nil {
			return warnings, fmt.Errorf("write properties file: %w", err)
		}
		slog.Info("Properties written", "route_id", routeID, "keys", len(merged))
	}

	filename := safeID + ".yaml"
	filePath := filepath.Join(d.routesDir, filename)
	if err := os.WriteFile(filePath, []byte(resolved), 0644); err != nil {
		return warnings, fmt.Errorf("write route file %s: %w", filename, err)
	}
	slog.Info("Route deployed", "route_id", routeID, "file", filePath)
	return warnings, nil
}

// normalizeVarKey converts UPPER_SNAKE_CASE to lower.dot.case for Camel property keys.
// Example: "KAFKA_BROKERS" → "kafka.brokers", "API_KEY" → "api.key"
// Already-dotted keys (e.g. "sync.interval.ms") are lowercased only.
func normalizeVarKey(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", "."))
}

// readPropertiesFile parses a Java-style key=value properties file.
func readPropertiesFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result, nil
}

// writePropertiesFile writes a sorted key=value properties file.
func writePropertiesFile(path string, props map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	lines := make([]string, 0, len(props))
	for k, v := range props {
		lines = append(lines, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(lines)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// Undeploy removes all route files for an integration.
// Integration routes are namespaced: {integration_id}.{route_name}.yaml
func (d *Deployer) Undeploy(integrationID string) error {
	// Routes use dashes: {integration}-{route}.yaml
	safeID := strings.ReplaceAll(integrationID, ".", "-")
	pattern := filepath.Join(d.routesDir, safeID+"-*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob routes: %w", err)
	}

	// Also try exact match (single-route integration)
	exact := filepath.Join(d.routesDir, safeID+".yaml")
	if _, err := os.Stat(exact); err == nil {
		matches = append(matches, exact)
	}

	if len(matches) == 0 {
		slog.Warn("No route files found for integration", "integration_id", integrationID, "pattern", pattern)
		return nil
	}

	for _, f := range matches {
		if err := os.Remove(f); err != nil {
			slog.Warn("Failed to remove route file", "file", f, "error", err)
		} else {
			slog.Info("Route file removed", "file", filepath.Base(f))
		}
	}

	slog.Info("Integration undeployed", "integration_id", integrationID, "files_removed", len(matches))
	return nil
}

// ListDeployed returns route IDs from files currently in the routes directory.
func (d *Deployer) ListDeployed() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(d.routesDir, "*.yaml"))
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(matches))
	for _, f := range matches {
		name := filepath.Base(f)
		id := strings.TrimSuffix(name, ".yaml")
		ids = append(ids, id)
	}
	return ids, nil
}

// resolveSecrets substitutes {{ VAR }} placeholders with local secret values.
// Lookup order: local secrets (secrets.env) > env vars.
// Camel-style {{property.name}} references are left as-is for the Camel runtime to resolve.
// Unresolved placeholders are left as-is.
func (d *Deployer) resolveSecrets(yaml string) string {
	var buf strings.Builder
	remaining := yaml

	for {
		start := strings.Index(remaining, "{{")
		if start == -1 {
			buf.WriteString(remaining)
			break
		}

		end := strings.Index(remaining[start:], "}}")
		if end == -1 {
			// Unclosed {{ — write rest as-is
			buf.WriteString(remaining)
			break
		}
		end += start + 2

		varName := strings.TrimSpace(remaining[start+2 : end-2])

		// Write everything before the placeholder
		buf.WriteString(remaining[:start])

		// Lookup order: local secrets > env vars
		if v, ok := d.secrets.Get(varName); ok {
			buf.WriteString(v)
		} else if v := os.Getenv(varName); v != "" {
			buf.WriteString(v)
		} else {
			// Leave placeholder as-is (may be a Camel property reference resolved at runtime)
			buf.WriteString(remaining[start:end])
		}

		remaining = remaining[end:]
	}

	return buf.String()
}

// HasVar checks if a var name is available (local secrets > env vars).
// Returns (value, true) if found, ("", false) if missing.
// ListVarNames returns just names for backward compat.
func (d *Deployer) ListVarNames() []string {
	vars := d.ListLocalVars()
	names := make([]string, len(vars))
	for i, v := range vars {
		names[i] = v.Name
	}
	return names
}

func (d *Deployer) HasVar(varName string) (string, bool) {
	if v, ok := d.secrets.Get(varName); ok {
		return v, true
	}
	if v := os.Getenv(varName); v != "" {
		return v, true
	}
	return "", false
}

// ListVarNames returns all var names known to this engine (from secrets.env + env).
// LocalVar describes a var available on the engine with its source.
type LocalVar struct {
	Name   string
	Source string // "secrets.env" or "env"
}

// ListLocalVars returns all vars available on this engine with source info.
func (d *Deployer) ListLocalVars() []LocalVar {
	var result []LocalVar

	// secrets.env vars (highest priority)
	for _, name := range d.secrets.ListKeys() {
		result = append(result, LocalVar{Name: name, Source: "secrets.env"})
	}

	// OS env vars (only UPPER_SNAKE_CASE that aren't already in secrets)
	secretNames := make(map[string]bool)
	for _, lv := range result {
		secretNames[lv.Name] = true
	}
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 && isBindingVar(parts[0]) && !secretNames[parts[0]] {
			result = append(result, LocalVar{Name: parts[0], Source: "env"})
		}
	}
	return result
}

// isBindingVar returns true if the env var name looks like a user binding var
// (ALL_CAPS_WITH_UNDERSCORES, not a system var like PATH, HOME, etc.)
func isBindingVar(name string) bool {
	if len(name) < 3 {
		return false
	}
	// Exclude common system vars
	systemPrefixes := []string{"PATH", "HOME", "USER", "SHELL", "TERM", "LANG", "LC_", "XDG_", "DBUS_", "SSH_", "DISPLAY", "CARAVEE_", "QUARKUS_", "JAVA_"}
	for _, p := range systemPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	// Must be UPPER_SNAKE_CASE
	for _, c := range name {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}
