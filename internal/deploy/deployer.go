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
	routesDir string
	secrets   *SecretManager
}

// NewDeployer creates a deployer targeting the given routes directory.
func NewDeployer(routesDir string, secrets *SecretManager) *Deployer {
	return &Deployer{
		routesDir: routesDir,
		secrets:   secrets,
	}
}

// Deploy writes a route YAML file after substituting secrets.
func (d *Deployer) Deploy(routeID, camelYAML string, bundleSecrets map[string]string) error {
	if err := os.MkdirAll(d.routesDir, 0755); err != nil {
		return fmt.Errorf("create routes dir: %w", err)
	}

	// Substitute secrets: {{ VAR }} → value
	resolved := d.resolveSecrets(camelYAML, bundleSecrets)

	// Write file — replace dots with dashes (Camel treats dots as extension separators)
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
