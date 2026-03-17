package pairing

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

const (
	KeySize    = 2048
	PrivKeyFile = "engine-key.pem"
	PubKeyFile  = "engine-key.pub"
)

type PairRequest struct {
	OTP        string         `json:"otp"`
	PublicKey  string         `json:"public_key"`
	EngineInfo map[string]any `json:"engine_info,omitempty"`
}

type PairResponse struct {
	EngineID  string `json:"engine_id"`
	TenantID  string `json:"tenant_id"`
	WSSURL    string `json:"wss_url"`
	Label     string `json:"label,omitempty"`
}

// GenerateKeypair generates an RSA keypair and saves to /data/
func GenerateKeypair(dataDir string) error {
	privPath := filepath.Join(dataDir, PrivKeyFile)
	pubPath := filepath.Join(dataDir, PubKeyFile)

	// Check if keys already exist
	if _, err := os.Stat(privPath); err == nil {
		slog.Info("Keypair already exists", "path", privPath)
		return nil
	}

	slog.Info("Generating RSA keypair", "bits", KeySize)
	privKey, err := rsa.GenerateKey(rand.Reader, KeySize)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	// Save private key
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	// Save public key
	pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	if err := os.WriteFile(pubPath, pubPEM, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	slog.Info("Keypair generated", "private", privPath, "public", pubPath)
	return nil
}

// LoadPublicKey reads the PEM-encoded public key
func LoadPublicKey(dataDir string) (string, error) {
	pubPath := filepath.Join(dataDir, PubKeyFile)
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("read public key: %w", err)
	}
	return string(data), nil
}

// Pair calls the cloud pairing endpoint with OTP + public key
func Pair(pairingURL, otp string, publicKey string) (*PairResponse, error) {
	req := PairRequest{
		OTP:       otp,
		PublicKey: publicKey,
		EngineInfo: map[string]any{
			"os":      "linux",
			"arch":    "amd64",
			"version": "0.1.0",
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := http.Post(pairingURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("POST pairing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pairing failed (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var pairResp PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&pairResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	slog.Info("Pairing successful", "engine_id", pairResp.EngineID, "wss_url", pairResp.WSSURL)
	return &pairResp, nil
}
