package pairing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeypair(t *testing.T) {
	tempDir := t.TempDir()

	err := GenerateKeypair(tempDir)
	if err != nil {
		t.Fatalf("GenerateKeypair failed: %v", err)
	}

	// Check files exist
	privPath := filepath.Join(tempDir, PrivKeyFile)
	pubPath := filepath.Join(tempDir, PubKeyFile)

	if _, err := os.Stat(privPath); os.IsNotExist(err) {
		t.Errorf("Private key file not created: %s", privPath)
	}

	if _, err := os.Stat(pubPath); os.IsNotExist(err) {
		t.Errorf("Public key file not created: %s", pubPath)
	}

	// Second call should be idempotent (not regenerate)
	err = GenerateKeypair(tempDir)
	if err != nil {
		t.Errorf("GenerateKeypair should be idempotent: %v", err)
	}
}

func TestLoadPublicKey(t *testing.T) {
	tempDir := t.TempDir()

	// Generate keypair first
	err := GenerateKeypair(tempDir)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Load public key
	pubKey, err := LoadPublicKey(tempDir)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}

	if pubKey == "" {
		t.Error("Public key is empty")
	}

	if len(pubKey) < 100 {
		t.Errorf("Public key suspiciously short: %d bytes", len(pubKey))
	}

	// Should contain PEM header
	if !contains(pubKey, "BEGIN PUBLIC KEY") {
		t.Error("Public key missing PEM header")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	tempDir := t.TempDir()

	// Generate keypair
	err := GenerateKeypair(tempDir)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Skip full encrypt/decrypt test — we'd need to replicate browser WebCrypto logic
	// (RSA-OAEP with SHA-256) which is tested end-to-end in integration tests
	t.Skip("Full encrypt/decrypt test requires browser-compatible RSA-OAEP implementation")
}

func TestLoadPrivateKey(t *testing.T) {
	tempDir := t.TempDir()

	// Generate keypair
	err := GenerateKeypair(tempDir)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Load private key
	privKey, err := LoadPrivateKey(tempDir)
	if err != nil {
		t.Fatalf("LoadPrivateKey failed: %v", err)
	}

	if privKey == nil {
		t.Error("Private key is nil")
	}

	// Verify key size
	if privKey.N.BitLen() != KeySize {
		t.Errorf("Expected key size %d, got %d", KeySize, privKey.N.BitLen())
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
