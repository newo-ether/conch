package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/newo-ether/conch/buildinfo"
	conchcrypto "github.com/newo-ether/conch/crypto"
)

func TestTransportNegotiatesServerVersionAndCapabilities(t *testing.T) {
	const apiKey = "version-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /public-key", signedPublicKeyHandler(t, []byte(apiKey), keyPair))
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildinfo.Current("conch"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	metadata := transport.Metadata()
	if metadata.ProtocolVersion != buildinfo.ProtocolVersion || len(metadata.Capabilities) == 0 {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestTransportRejectsUnsupportedServerProtocol(t *testing.T) {
	const apiKey = "version-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /public-key", signedPublicKeyHandler(t, []byte(apiKey), keyPair))
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		metadata := buildinfo.Current("conch")
		metadata.ProtocolVersion = "999"
		_ = json.NewEncoder(w).Encode(metadata)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	transport := NewTransport(server.URL, apiKey)
	err = transport.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported Conch protocol") {
		t.Fatalf("Initialize error = %v", err)
	}
}

func signedPublicKeyHandler(
	t *testing.T,
	apiKey []byte,
	keyPair *conchcrypto.KeyPair,
) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		publicKey := keyPair.PublicKeyBase64()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"public_key": publicKey,
			"nonce":      nonce,
			"signature":  conchcrypto.SignPayload(apiKey, nonce, publicKey),
		})
	}
}
