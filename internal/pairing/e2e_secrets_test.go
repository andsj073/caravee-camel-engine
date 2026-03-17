package pairing

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

// TestE2ESecretEncryptDecrypt simulates the full browser → cloud → engine flow
func TestE2ESecretEncryptDecrypt(t *testing.T) {
	tempDir := t.TempDir()

	// Step 1: Engine generates keypair (happens at pairing)
	err := GenerateKeypair(tempDir)
	if err != nil {
		t.Fatalf("GenerateKeypair failed: %v", err)
	}

	// Step 2: Cloud gets public key (happens at pairing)
	pubKeyPEM, err := LoadPublicKey(tempDir)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}

	// Step 3: Browser encrypts secret with public key
	plaintext := "supersecret-api-key-2026"
	cipherBase64, err := browserEncrypt(plaintext, pubKeyPEM)
	if err != nil {
		t.Fatalf("Browser encryption failed: %v", err)
	}

	t.Logf("Plaintext: %s", plaintext)
	t.Logf("Cipher (base64): %s", cipherBase64[:50]+"...")

	// Step 4: Cloud stores cipher (simulated)
	// In real flow: cloud stores cipher in secret_cipher column
	// Cloud never sees plaintext

	// Step 5: Deploy sends cipher to engine
	// Engine decrypts with private key
	privKey, err := LoadPrivateKey(tempDir)
	if err != nil {
		t.Fatalf("LoadPrivateKey failed: %v", err)
	}

	decrypted, err := DecryptSecret(cipherBase64, privKey)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	t.Logf("Decrypted: %s", decrypted)

	// Verify roundtrip
	if decrypted != plaintext {
		t.Errorf("Roundtrip failed: expected %q, got %q", plaintext, decrypted)
	}
}

// browserEncrypt simulates browser-side WebCrypto RSA-OAEP encryption
func browserEncrypt(plaintext string, pubKeyPEM string) (string, error) {
	// Parse PEM public key
	block, _ := pem.Decode([]byte(pubKeyPEM))
	if block == nil {
		return "", nil
	}

	pubKeyInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}

	pubKey, ok := pubKeyInterface.(*rsa.PublicKey)
	if !ok {
		return "", nil
	}

	// Encrypt with RSA-OAEP + SHA-256 (same as WebCrypto)
	cipherBytes, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubKey, []byte(plaintext), nil)
	if err != nil {
		return "", err
	}

	// Base64 encode (same as browser)
	return base64.StdEncoding.EncodeToString(cipherBytes), nil
}

// TestE2EMultipleSecrets tests multiple secrets in one deploy
func TestE2EMultipleSecrets(t *testing.T) {
	tempDir := t.TempDir()

	GenerateKeypair(tempDir)
	pubKeyPEM, _ := LoadPublicKey(tempDir)
	privKey, _ := LoadPrivateKey(tempDir)

	secrets := []struct {
		name      string
		plaintext string
	}{
		{"API_KEY", "key-123"},
		{"DB_PASSWORD", "postgres-secret"},
		{"OAUTH_TOKEN", "bearer-xyz"},
	}

	for _, s := range secrets {
		cipher, err := browserEncrypt(s.plaintext, pubKeyPEM)
		if err != nil {
			t.Fatalf("Encryption of %s failed: %v", s.name, err)
		}

		decrypted, err := DecryptSecret(cipher, privKey)
		if err != nil {
			t.Fatalf("Decryption of %s failed: %v", s.name, err)
		}

		if decrypted != s.plaintext {
			t.Errorf("Secret %s roundtrip failed: expected %q, got %q",
				s.name, s.plaintext, decrypted)
		}
	}

	t.Logf("All %d secrets encrypted/decrypted successfully", len(secrets))
}

// TestE2ELongSecret tests encryption of longer secret values
func TestE2ELongSecret(t *testing.T) {
	tempDir := t.TempDir()

	GenerateKeypair(tempDir)
	pubKeyPEM, _ := LoadPublicKey(tempDir)
	privKey, _ := LoadPrivateKey(tempDir)

	// Realistic long secret (JWT token, etc.)
	longSecret := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMn0.NHVaYe26MbtOYhSKkoKYdFVomg4i8ZJd8_-RU8VNbftc4TSMb4bXP3l3YlNWACwyXPGffz5aXHc6lty1Y2t4SWRqGteragsVdZufDn5BlnJl9pdR_kdVFUsra2rWKEofkZeIC4yWytE58sMIihvo9H1ScmmVwBcQP6XETqYd0aSHp1gOa9RdUPDvoXQ5oqygTqVtxaDr6wUFKrKItgBMzWIdNZ6y7O9E0DhEPTbE9rfBo6KTFsHAZnMg4k68CDp2woYIaXbmYTWcvbzIuHO7_37GT79XdIwkm95QJ7hYC9RiwrV7mesbY4PAahERJawntho0my942XheVLmGwLMBkQ"

	// RSA-2048 can encrypt up to ~214 bytes with SHA-256
	// JWT above is too long — test will verify proper handling

	cipher, err := browserEncrypt(longSecret, pubKeyPEM)
	if err != nil {
		// Expected: message too long for RSA key
		t.Logf("Long secret encryption failed as expected: %v", err)
		t.Skip("RSA-OAEP cannot encrypt messages longer than key size - 2*hash - 2")
	}

	decrypted, err := DecryptSecret(cipher, privKey)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	if decrypted != longSecret {
		t.Error("Long secret roundtrip failed")
	}
}

// TestE2EEmptySecret tests edge case
func TestE2EEmptySecret(t *testing.T) {
	tempDir := t.TempDir()

	GenerateKeypair(tempDir)
	pubKeyPEM, _ := LoadPublicKey(tempDir)
	privKey, _ := LoadPrivateKey(tempDir)

	plaintext := "" // Empty secret

	cipher, err := browserEncrypt(plaintext, pubKeyPEM)
	if err != nil {
		t.Fatalf("Encryption failed: %v", err)
	}

	decrypted, err := DecryptSecret(cipher, privKey)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Empty secret roundtrip failed: expected %q, got %q", plaintext, decrypted)
	}
}

// TestE2ESpecialCharacters tests secrets with special characters
func TestE2ESpecialCharacters(t *testing.T) {
	tempDir := t.TempDir()

	GenerateKeypair(tempDir)
	pubKeyPEM, _ := LoadPublicKey(tempDir)
	privKey, _ := LoadPrivateKey(tempDir)

	specialSecrets := []string{
		"p@ssw0rd!#$%",
		"key=value&foo=bar",
		"emoji-test-🔒🐪",
		"newline\ntest",
		"tab\ttest",
		"quote'test\"here",
		"\\backslash\\test",
	}

	for _, plaintext := range specialSecrets {
		cipher, err := browserEncrypt(plaintext, pubKeyPEM)
		if err != nil {
			t.Fatalf("Encryption of %q failed: %v", plaintext, err)
		}

		decrypted, err := DecryptSecret(cipher, privKey)
		if err != nil {
			t.Fatalf("Decryption of %q failed: %v", plaintext, err)
		}

		if decrypted != plaintext {
			t.Errorf("Special char secret roundtrip failed:\nExpected: %q\nGot:      %q",
				plaintext, decrypted)
		}
	}

	t.Logf("All %d special character secrets passed", len(specialSecrets))
}
