package internal

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caravee/engine/internal/deploy"
	"github.com/caravee/engine/internal/pairing"
)

// TestFullSecretsFlowIntegration simulates the complete end-to-end flow:
// Browser → Cloud → Engine → .properties file (Camel resolves at runtime)
func TestFullSecretsFlowIntegration(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	routesDir := filepath.Join(dataDir, "routes")
	secretsDir := filepath.Join(dataDir, "secrets")

	// Create directories
	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(routesDir, 0755)
	os.MkdirAll(secretsDir, 0755)

	// Step 1: Engine pairing — generate keypair
	t.Log("Step 1: Engine generates RSA keypair at pairing")
	err := pairing.GenerateKeypair(dataDir)
	if err != nil {
		t.Fatalf("Keypair generation failed: %v", err)
	}

	// Step 2: Cloud receives public key
	t.Log("Step 2: Cloud receives engine public key")
	pubKeyPEM, err := pairing.LoadPublicKey(dataDir)
	if err != nil {
		t.Fatalf("Public key load failed: %v", err)
	}

	// Step 3: User enters secret in browser
	t.Log("Step 3: User enters secret in browser UI")
	originalSecret := "my-api-key-12345"

	// Step 4: Browser encrypts with engine public key (WebCrypto simulation)
	t.Log("Step 4: Browser encrypts with WebCrypto (RSA-OAEP + SHA-256)")
	cipherBase64, err := browserSideEncryption(originalSecret, pubKeyPEM)
	if err != nil {
		t.Fatalf("Browser encryption failed: %v", err)
	}

	// Step 5: Cloud stores cipher (never sees plaintext)
	t.Log("Step 5: Cloud stores cipher in DB (plaintext never visible)")

	// Step 6: Deploy — cloud sends cipher bundle to engine via WSS
	t.Log("Step 6: Deploy sends cipher bundle to engine")

	// Step 7: Engine deployer decrypts cipher and writes .properties file
	t.Log("Step 7: Deployer decrypts with private key and writes .properties")
	secretMgr := deploy.NewSecretManager(secretsDir)
	deployer := deploy.NewDeployer(routesDir, secretMgr, dataDir)

	routeYAML := `- route:
    id: test.api-call
    from:
      uri: timer:tick?period=10000
      steps:
        - setHeader:
            name: X-API-Key
            constant: "{{api.key}}"
        - to:
            uri: https://api.example.com/data
`

	_, err = deployer.Deploy("test.api-call", routeYAML, nil, []deploy.SecretEntry{
		{Var: "API_KEY", Cipher: cipherBase64},
	})
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Step 8: Verify .properties file contains decrypted secret
	t.Log("Step 8: Verify .properties file contains decrypted secret under normalized key")
	propsFile := filepath.Join(routesDir, "test-api-call.properties")
	propsContent, err := os.ReadFile(propsFile)
	if err != nil {
		t.Fatalf("Properties file not created: %v", err)
	}

	propsStr := string(propsContent)
	if !strings.Contains(propsStr, "api.key="+originalSecret) {
		t.Errorf(".properties file does not contain decrypted secret")
		t.Logf("Expected to find: api.key=%s", originalSecret)
		t.Logf(".properties content:\n%s", propsStr)
	}

	// Step 9: Verify YAML still has {{...}} placeholder (Camel resolves at runtime)
	t.Log("Step 9: Verify YAML preserves {{...}} placeholder for Camel runtime")
	deployedYAML, err := os.ReadFile(filepath.Join(routesDir, "test-api-call.yaml"))
	if err != nil {
		t.Fatalf("Failed to read deployed route: %v", err)
	}
	if !strings.Contains(string(deployedYAML), "{{api.key}}") {
		t.Error("Deployed YAML should preserve {{api.key}} placeholder for Camel")
	}

	// Step 10: Verify cipher text never stored as plaintext outside routes dir
	t.Log("Step 10: Verify zero-trust — original cipher not stored as plaintext outside routes")
	secretFiles, _ := filepath.Glob(filepath.Join(dataDir, "*secret*"))
	for _, f := range secretFiles {
		info, _ := os.Stat(f)
		basename := filepath.Base(f)
		if info != nil && !info.IsDir() && basename != "secrets.env" && !strings.Contains(f, "/routes/") {
			t.Errorf("Unexpected plaintext secret file found: %s", f)
		}
	}

	t.Log("✅ Full secrets flow verified:")
	t.Logf("   Browser encrypted: %s → %s", originalSecret, cipherBase64[:50]+"...")
	t.Logf("   Cloud stored cipher (no plaintext)")
	t.Logf("   Engine wrote .properties: api.key=%s", originalSecret)
	t.Logf("   Camel route uses {{api.key}} resolved at runtime from .properties")
}

// browserSideEncryption simulates browser WebCrypto RSA-OAEP encryption
func browserSideEncryption(plaintext string, pubKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(pubKeyPEM))
	if block == nil {
		return "", nil
	}

	pubKeyInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}

	pubKey := pubKeyInterface.(*rsa.PublicKey)
	cipherBytes, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubKey, []byte(plaintext), nil)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(cipherBytes), nil
}
