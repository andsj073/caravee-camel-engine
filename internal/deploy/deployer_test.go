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
	deployer := NewDeployer(routesDir, secretMgr, "")

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

	_, err := deployer.Deploy(routeID, yaml, nil, nil)
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
	deployer := NewDeployer(routesDir, secretMgr, "")

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

	deployer.Deploy("test-integration.route1", yaml1, nil, nil)
	deployer.Deploy("test-integration.route2", yaml2, nil, nil)

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

func TestPropertiesFileWritten(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr, "")

	yaml := `- route:
    id: test.props
    from:
      uri: timer:tick
      steps:
        - to:
            uri: "http:{{orders.api.url}}/orders"
`

	properties := map[string]string{
		"orders.api.url": "https://api.example.com",
		"sync.interval":  "5000",
	}

	_, err := deployer.Deploy("test.props", yaml, properties, nil)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Verify .properties file was written
	propsFile := filepath.Join(routesDir, "test-props.properties")
	content, err := os.ReadFile(propsFile)
	if err != nil {
		t.Fatalf("Properties file not created: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "orders.api.url=https://api.example.com") {
		t.Errorf("Properties file missing orders.api.url: %s", contentStr)
	}
	if !strings.Contains(contentStr, "sync.interval=5000") {
		t.Errorf("Properties file missing sync.interval: %s", contentStr)
	}

	// YAML should have {{...}} placeholders resolved against properties map
	yamlContent, err := os.ReadFile(filepath.Join(routesDir, "test-props.yaml"))
	if err != nil {
		t.Fatalf("YAML file not created: %v", err)
	}
	if strings.Contains(string(yamlContent), "{{orders.api.url}}") {
		t.Error("YAML should have {{...}} placeholders resolved, not preserved")
	}
	if !strings.Contains(string(yamlContent), "https://api.example.com") {
		t.Error("YAML should contain resolved URL value")
	}
}

func TestPropertiesOverwriteWarning(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr, "")

	yaml := "- route: {}"

	// First deploy
	deployer.Deploy("test.warn", yaml, map[string]string{"my.key": "value1"}, nil)

	// Second deploy with different value — should warn
	warnings, err := deployer.Deploy("test.warn", yaml, map[string]string{"my.key": "value2"}, nil)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("Expected overwrite warning, got none")
	}
	if !strings.Contains(warnings[0], "my.key") {
		t.Errorf("Warning should mention key name: %v", warnings)
	}
}

func TestLocalSecretInlineResolution(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	// Create local secrets.env file
	os.MkdirAll(secretsDir, 0755)
	secretsEnvFile := filepath.Join(secretsDir, "secrets.env")
	os.WriteFile(secretsEnvFile, []byte("MY_LOCAL_VAR=local-value\n"), 0600)

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr, "")

	yaml := `- route:
    id: test.local
    from:
      uri: timer:tick
      steps:
        - log:
            message: "{{ MY_LOCAL_VAR }}"
`

	_, err := deployer.Deploy("test.local", yaml, nil, nil)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(routesDir, "test-local.yaml"))
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	// Local secrets.env vars are still inlined
	if !strings.Contains(string(content), "local-value") {
		t.Error("Local secret.env var was not inlined in YAML")
	}
}

func TestListDeployed(t *testing.T) {
	tempDir := t.TempDir()
	routesDir := filepath.Join(tempDir, "routes")
	secretsDir := filepath.Join(tempDir, "secrets")

	secretMgr := NewSecretManager(secretsDir)
	deployer := NewDeployer(routesDir, secretMgr, "")

	// Deploy several routes
	deployer.Deploy("integration1.route1", "- route: {}", nil, nil)
	deployer.Deploy("integration2.route1", "- route: {}", nil, nil)

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
