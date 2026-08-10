package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
)

func TestTransportRecoversWhenServerComesOnlineAfterStartup(t *testing.T) {
	const apiKey = "offline-recovery-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	editHandler := &handler.FileEditHandler{APIKey: []byte(apiKey), KeyPair: keyPair}
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /file/edit", auth(editHandler))
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		publicKey := keyPair.PublicKeyBase64()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"public_key": publicKey,
			"nonce":      nonce,
			"signature":  conchcrypto.SignPayload([]byte(apiKey), nonce, publicKey),
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err == nil {
		t.Fatal("startup initialization unexpectedly succeeded")
	}
	if available, _ := transport.Status(); available {
		t.Fatal("offline transport reported available")
	}

	path := filepath.Join(t.TempDir(), "recovered.txt")
	if err := os.WriteFile(path, []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	ready.Store(true)
	if _, err := transport.FileEdit(
		context.Background(),
		path,
		"before",
		"after",
		false,
		"",
	); err != nil {
		t.Fatalf("FileEdit after server recovery: %v", err)
	}
	if available, statusErr := transport.Status(); !available || statusErr != "" {
		t.Fatalf("recovered status = %v, %q", available, statusErr)
	}
}

func TestTransportRefreshesRotatedServerKeyBeforeRetryingSafeEdit(t *testing.T) {
	const apiKey = "rotation-key"
	currentKey, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	editHandler := &handler.FileEditHandler{APIKey: []byte(apiKey), KeyPair: currentKey}
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /file/edit", auth(editHandler))
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		publicKey := currentKey.PublicKeyBase64()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"public_key": publicKey,
			"nonce":      nonce,
			"signature":  conchcrypto.SignPayload([]byte(apiKey), nonce, publicKey),
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	rotatedKey, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	currentKey = rotatedKey
	editHandler.KeyPair = rotatedKey

	path := filepath.Join(t.TempDir(), "rotated.txt")
	if err := os.WriteFile(path, []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.FileEdit(
		context.Background(),
		path,
		"before",
		"after",
		false,
		"",
	); err != nil {
		t.Fatalf("FileEdit after key rotation: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after" {
		t.Fatalf("content = %q, want after", data)
	}
}

func TestTransportFileEditPreservesLargeFileTailOverEncryptedProtocol(t *testing.T) {
	const apiKey = "integration-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /file/edit", auth(&handler.FileEditHandler{
		APIKey:  []byte(apiKey),
		KeyPair: keyPair,
	}))
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		publicKey := keyPair.PublicKeyBase64()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"public_key": publicKey,
			"nonce":      nonce,
			"signature":  conchcrypto.SignPayload([]byte(apiKey), nonce, publicKey),
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	path := filepath.Join(t.TempDir(), "large.txt")
	prefix := strings.Repeat("p", (1<<20)+64)
	tail := strings.Repeat("tail", 1024)
	if err := os.WriteFile(path, []byte(prefix+"needle"+tail), 0644); err != nil {
		t.Fatal(err)
	}

	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	result, err := transport.FileEdit(
		context.Background(),
		path,
		"needle",
		"replacement",
		false,
		"",
	)
	if err != nil {
		t.Fatalf("FileEdit: %v", err)
	}
	if result.Replacements != 1 || result.SHA256 == "" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != prefix+"replacement"+tail {
		t.Fatalf("encrypted MCP edit corrupted large file: got %d bytes", len(data))
	}
}
