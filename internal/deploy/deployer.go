package deploy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Deployer writes and removes route YAML files in the routes directory.
// Camel's file watcher picks up changes automatically.
type Deployer struct {
	routesDir  string
	kameletDir string // /data/kamelets — Kamelet definitions (name.kamelet.yaml)
	secrets    *SecretManager
}

// NewDeployer creates a deployer targeting the given routes directory.
func NewDeployer(routesDir string, secrets *SecretManager) *Deployer {
	// Kamelet dir is sibling of routes dir: /data/routes → /data/kamelets
	kameletDir := filepath.Join(filepath.Dir(routesDir), "kamelets")
	return &Deployer{
		routesDir:  routesDir,
		kameletDir: kameletDir,
		secrets:    secrets,
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
func (d *Deployer) Deploy(routeID, camelYAML string, bundleSecrets map[string]string) error {
	resolved := d.resolveSecrets(camelYAML, bundleSecrets)

	if isKamelet(resolved) {
		// Kamelet: write to /data/kamelets/name.kamelet.yaml
		if err := os.MkdirAll(d.kameletDir, 0755); err != nil {
			return fmt.Errorf("create kamelets dir: %w", err)
		}
		name := kameletName(resolved)
		if name == "" {
			name = strings.ReplaceAll(routeID, ".", "-")
		}
		filename := name + ".kamelet.yaml"
		filePath := filepath.Join(d.kameletDir, filename)
		if err := os.WriteFile(filePath, []byte(resolved), 0644); err != nil {
			return fmt.Errorf("write kamelet file %s: %w", filename, err)
		}
		slog.Info("Kamelet deployed", "name", name, "file", filePath)
		return nil
	}

	// Integration route: write to /data/routes/
	if err := os.MkdirAll(d.routesDir, 0755); err != nil {
		return fmt.Errorf("create routes dir: %w", err)
	}
	safeID := strings.ReplaceAll(routeID, ".", "-")
	filename := safeID + ".yaml"
	filePath := filepath.Join(d.routesDir, filename)
	if err := os.WriteFile(filePath, []byte(resolved), 0644); err != nil {
		return fmt.Errorf("write route file %s: %w", filename, err)
	}
	slog.Info("Route deployed", "route_id", routeID, "file", filePath)
	return nil
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

// resolveSecrets substitutes {{ VAR }} placeholders with secret values.
// Lookup order: local secrets > env vars > bundle secrets.
// Unresolved placeholders are left as-is.
func (d *Deployer) resolveSecrets(yaml string, bundleSecrets map[string]string) string {
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

		// Lookup order: local > env > bundle
		if v, ok := d.secrets.Get(varName); ok {
			buf.WriteString(v)
		} else if v := os.Getenv(varName); v != "" {
			buf.WriteString(v)
		} else if v, ok := bundleSecrets[varName]; ok {
			buf.WriteString(v)
		} else {
			slog.Warn("Secret not found, leaving placeholder", "var", varName)
			buf.WriteString(remaining[start:end]) // preserve original {{varName}}
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
