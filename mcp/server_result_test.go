package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/newo-ether/conch/buildinfo"
	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
)

// ToolCallResult no longer has a StructuredContent field, so the "clients must
// see the full payload" property of these tests is enforced at compile time;
// the runtime assertions here pin the content text itself.

func TestFormatShellOutputCarriesFullTextWithoutStructuredContent(t *testing.T) {
	code := 7
	events := []LineEvent{
		{Line: "first"},
		{Line: "second", Stream: "stderr"},
		{Error: "boom"},
		{ExitCode: &code},
	}
	result := formatShellOutput(events)
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}
	text := result.Content[0].Text
	for _, want := range []string{"first", "[stderr] second", "[error] boom", "Exit code: 7"} {
		if !strings.Contains(text, want) {
			t.Fatalf("content text missing %q: %q", want, text)
		}
	}
}

func TestFileReadResultCarriesContentWithoutStructuredContent(t *testing.T) {
	const apiKey = "file-read-shape-key"
	fileHandler := &handler.FileReadHandler{
		APIKey:  []byte(apiKey),
		KeyPair: mustKeyPair(t),
	}
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /file/read", auth(fileHandler))
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"public_key": fileHandler.KeyPair.PublicKeyBase64(),
			"nonce":      nonce,
			"signature":  conchcrypto.SignPayload([]byte(apiKey), nonce, fileHandler.KeyPair.PublicKeyBase64()),
		})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildinfo.Current("conch"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	path := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(path, []byte("payload-one\npayload-two"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewServer(
		map[string]*Transport{"quantum": transport},
		map[string]DeviceConfig{"quantum": {URL: server.URL, Key: apiKey}},
	)
	result, rpcErr := s.doFileRead(context.Background(), transport, map[string]interface{}{
		"path": path,
	})
	if rpcErr != nil {
		t.Fatalf("doFileRead error: %v", rpcErr)
	}
	if result.IsError {
		t.Fatalf("IsError = true")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "payload-one") || !strings.Contains(text, "payload-two") {
		t.Fatalf("content text missing payload: %q", text)
	}
}

func mustKeyPair(t *testing.T) *conchcrypto.KeyPair {
	t.Helper()
	pair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
