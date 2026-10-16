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

	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
)

// newFileToolsTestTransport serves the real encrypted file handlers.
func newFileToolsTestTransport(t *testing.T) *Transport {
	t.Helper()
	const apiKey = "file-tools-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /file/edit", auth(&handler.FileEditHandler{APIKey: []byte(apiKey), KeyPair: keyPair}))
	mux.Handle("POST /file/grep", auth(&handler.FileGrepHandler{APIKey: []byte(apiKey), KeyPair: keyPair}))
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(conchcrypto.SignHandshake([]byte(apiKey), keyPair.PublicKeyBase64(), nonce, r.URL.Query().Get("challenge")))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return transport
}

func TestTransportFileEditBatchRoundTrip(t *testing.T) {
	transport := newFileToolsTestTransport(t)
	path := filepath.Join(t.TempDir(), "batch.txt")
	if err := os.WriteFile(path, []byte("a b c\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := transport.FileEditBatch(context.Background(), path, []FileEditOperation{
		{OldString: "a", NewString: "1"},
		{OldString: "c", NewString: "3"},
	}, "")
	if err != nil {
		t.Fatalf("FileEditBatch: %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("replacements = %d, want 2", result.Replacements)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "1 b 3\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestTransportFileEditBatchExplainsOldServer(t *testing.T) {
	// An older server ignores the unknown edits field and answers exactly like a
	// current server does for a request with neither edits nor old_string, so a
	// nil batch reproduces the legacy response end to end.
	transport := newFileToolsTestTransport(t)
	path := filepath.Join(t.TempDir(), "legacy.txt")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := transport.FileEditBatch(context.Background(), path, nil, "")
	if err == nil || !strings.Contains(err.Error(), "does not support batched edits") {
		t.Fatalf("err = %v, want old-server hint", err)
	}
}

func TestTransportFileGrepContext(t *testing.T) {
	transport := newFileToolsTestTransport(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\nTWO\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := transport.FileGrep(context.Background(), FileGrepOptions{
		Pattern:         "two",
		Path:            dir,
		CaseInsensitive: true,
		Context:         1,
	})
	if err != nil {
		t.Fatalf("FileGrep: %v", err)
	}
	if !result.ContextSupported || len(result.Matches) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	m := result.Matches[0]
	if m.Line != 2 || len(m.Before) != 1 || m.Before[0] != "one" || len(m.After) != 1 || m.After[0] != "three" {
		t.Fatalf("unexpected match: %+v", m)
	}
}
