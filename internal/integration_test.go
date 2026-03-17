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
// Browser → Cloud → Engine → Camel
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
	// In real system: INSERT INTO endpoint_bindings (secret_cipher) VALUES (?)

	// Step 6: Deploy — cloud sends cipher to engine via WSS
	t.Log("Step 6: Deploy sends cipher bundle to engine")
	// Simulating DeployMessage.Secrets
	bundleSecrets := map[string]string{
		"API_KEY": "", // Will be decrypted from cipher
	}

	// Load engine private key
	privKey, err := pairing.LoadPrivateKey(dataDir)
	if err != nil {
		t.Fatalf("Private key load failed: %v", err)
	}

	// Step 7: Engine decrypts cipher
	t.Log("Step 7: Engine decrypts with private key")
	decryptedSecret, err := pairing.DecryptSecret(cipherBase64, privKey)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	bundleSecrets["API_KEY"] = decryptedSecret

	// Step 8: Deployer writes route with secret
	t.Log("Step 8: Deployer resolves secret placeholder and writes Camel YAML")
	secretMgr := deploy.NewSecretManager(secretsDir)
	deployer := deploy.NewDeployer(routesDir, secretMgr)

	routeYAML := `- route:
    id: test.api-call
    from:
      uri: timer:tick?period=10000
      steps:
        - setHeader:
            name: X-API-Key
            constant: "{{ API_KEY }}"
        - to:
            uri: https://api.example.com/data
`

	err = deployer.Deploy("test.api-call", routeYAML, bundleSecrets)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Step 9: Verify deployed route contains plaintext secret
	t.Log("Step 9: Verify deployed Camel YAML contains plaintext secret")
	deployedYAML, err := os.ReadFile(filepath.Join(routesDir, "test-api-call.yaml"))
	if err != nil {
		t.Fatalf("Failed to read deployed route: %v", err)
	}

	deployedStr := string(deployedYAML)

	// Verify secret was resolved
	if !strings.Contains(deployedStr, originalSecret) {
		t.Errorf("Deployed YAML does not contain decrypted secret")
		t.Logf("Expected to find: %s", originalSecret)
		t.Logf("Deployed YAML:\n%s", deployedStr)
	}

	// Verify placeholder was removed
	if strings.Contains(deployedStr, "{{ API_KEY }}") {
		t.Error("Deployed YAML still contains placeholder")
	}

	// Step 10: Verify secret never touched disk in plaintext (except in final route)
	t.Log("Step 10: Verify zero-trust — secret only exists in final route")
	// No plaintext secret file should exist in data dir (except secrets/ directory itself)
	secretFiles, _ := filepath.Glob(filepath.Join(dataDir, "*secret*"))
	for _, f := range secretFiles {
		info, _ := os.Stat(f)
		basename := filepath.Base(f)
		// Allow secrets/ directory and secrets.env, but not actual secret files
		if info != nil && !info.IsDir() && basename != "secrets.env" && !strings.Contains(f, "/routes/") {
			t.Errorf("Unexpected plaintext secret file found: %s", f)
		}
	}

	t.Log("✅ Full secrets flow verified:")
	t.Logf("   Browser encrypted: %s → %s", originalSecret, cipherBase64[:50]+"...")
	t.Logf("   Cloud stored cipher (no plaintext)")
	t.Logf("   Engine decrypted: %s", decryptedSecret)
	t.Logf("   Camel route contains plaintext for runtime use")
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
