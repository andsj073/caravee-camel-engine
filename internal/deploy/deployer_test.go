package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploy(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr)

	// Deploy a route
	routeID := "test-integration.tick"
	yaml := `- route:
    id: test-integration.tick
    from:
      uri: timer:tick?period=5000
      steps:
        - log:
            message: "Hello test"
`

	err := deployer.Deploy(routeID, yaml, nil)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Check file was created
	expectedFile := filepath.Join(routesDir, "test-integration-tick.yaml")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Errorf("Route file not created: %s", expectedFile)
	}

	// Read and verify content
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("Failed to read route file: %v", err)
	}

	if !strings.Contains(string(content), "timer:tick") {
		t.Error("Route YAML does not contain expected URI")
	}
}

func TestUndeploy(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr)

	// Deploy two routes for the same integration
	yaml1 := `- route:
    id: test-integration.route1
    from:
      uri: timer:tick
`
	yaml2 := `- route:
    id: test-integration.route2
    from:
      uri: timer:tock
`

	deployer.Deploy("test-integration.route1", yaml1, nil)
	deployer.Deploy("test-integration.route2", yaml2, nil)

	// Verify both files exist
	file1 := filepath.Join(routesDir, "test-integration-route1.yaml")
	file2 := filepath.Join(routesDir, "test-integration-route2.yaml")

	if _, err := os.Stat(file1); os.IsNotExist(err) {
		t.Fatalf("Route1 file not created")
	}
	if _, err := os.Stat(file2); os.IsNotExist(err) {
		t.Fatalf("Route2 file not created")
	}

	// Undeploy the integration
	err := deployer.Undeploy("test-integration")
	if err != nil {
		t.Fatalf("Undeploy failed: %v", err)
	}

	// Both files should be removed
	if _, err := os.Stat(file1); !os.IsNotExist(err) {
		t.Error("Route1 file was not removed")
	}
	if _, err := os.Stat(file2); !os.IsNotExist(err) {
		t.Error("Route2 file was not removed")
	}
}

func TestSecretResolution(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr)

	// YAML with secret placeholder
	yaml := `- route:
    id: test.secure
    from:
      uri: timer:tick
      steps:
        - setHeader:
            name: X-API-Key
            constant: "{{ MY_SECRET }}"
        - log:
            message: "test"
`

	bundleSecrets := map[string]string{
		"MY_SECRET": "secret123",
	}

	err := deployer.Deploy("test.secure", yaml, bundleSecrets)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Read deployed file
	content, err := os.ReadFile(filepath.Join(routesDir, "test-secure.yaml"))
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	contentStr := string(content)

	// Secret should be resolved
	if !strings.Contains(contentStr, "secret123") {
		t.Error("Secret not resolved in YAML")
	}

	// Placeholder should be removed
	if strings.Contains(contentStr, "{{ MY_SECRET }}") {
		t.Error("Secret placeholder still present")
	}
}

func TestSecretResolutionPriority(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	// Create local secrets.env file (priority 1)
	os.MkdirAll(secretsDir, 0755)
	secretsEnvFile := filepath.Join(secretsDir, "secrets.env")
	os.WriteFile(secretsEnvFile, []byte("MY_VAR=local-value\n"), 0600)

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr)

	yaml := `- route:
    id: test.priority
    from:
      uri: timer:tick
      steps:
        - log:
            message: "{{ MY_VAR }}"
`

	// Bundle provides different value (priority 3, should be overridden)
	bundleSecrets := map[string]string{
		"MY_VAR": "bundle-value",
	}

	err := deployer.Deploy("test.priority", yaml, bundleSecrets)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(routesDir, "test-priority.yaml"))
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	contentStr := string(content)

	// Should use local secret (priority 1)
	if !strings.Contains(contentStr, "local-value") {
		t.Error("Local secret was not used (priority mismatch)")
	}

	if strings.Contains(contentStr, "bundle-value") {
		t.Error("Bundle secret was used instead of local (priority violation)")
	}
}

func TestListDeployed(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr)

	// Deploy several routes
	deployer.Deploy("integration1.route1", "- route: {}", nil)
	deployer.Deploy("integration2.route1", "- route: {}", nil)

	deployed, err := deployer.ListDeployed()
	if err != nil {
		t.Fatalf("ListDeployed failed: %v", err)
	}

	if len(deployed) != 2 {
		t.Errorf("Expected 2 deployed routes, got %d", len(deployed))
	}

	// Check route IDs are correct (dashes, not dots)
	expected := map[string]bool{
		"integration1-route1": false,
		"integration2-route1": false,
	}

	for _, id := range deployed {
		if _, ok := expected[id]; ok {
			expected[id] = true
		}
	}

	for id, found := range expected {
		if !found {
			t.Errorf("Route %s not found in deployed list", id)
		}
	}
}
