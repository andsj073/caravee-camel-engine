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

	// Write file
	filename := routeID + ".yaml"
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
	pattern := filepath.Join(d.routesDir, integrationID+".*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob routes: %w", err)
	}

	// Also try exact match (single-route integration)
	exact := filepath.Join(d.routesDir, integrationID+".yaml")
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
// Lookup order: local secrets > env vars > bundle secrets
func (d *Deployer) resolveSecrets(yaml string, bundleSecrets map[string]string) string {
	result := yaml

	// Find all {{ VAR }} patterns
	for {
		start := strings.Index(result, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start + 2

		varName := strings.TrimSpace(result[start+2 : end-2])

		// Lookup order: local > env > bundle
		value := ""
		if v, ok := d.secrets.Get(varName); ok {
			value = v
		} else if v := os.Getenv(varName); v != "" {
			value = v
		} else if v, ok := bundleSecrets[varName]; ok {
			value = v
		} else {
			slog.Warn("Secret not found", "var", varName)
			// Leave placeholder as-is so it's obvious what's missing
			continue
		}

		result = result[:start] + value + result[end:]
	}

	return result
}
