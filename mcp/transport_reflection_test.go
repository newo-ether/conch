package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/newo-ether/conch/buildinfo"
	"github.com/newo-ether/conch/crypto"
)

func TestReflectedRequestCannotForgeAFileReadResponse(t *testing.T) {
	key := []byte("isolated-reflection-authority")
	pair, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	authority := http.NewServeMux()
	authority.HandleFunc("GET /public-key", signedPublicKeyHandler(t, key, pair))
	authority.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildinfo.Current("conch"))
	})
	server := httptest.NewServer(authority)
	defer server.Close()
	// The intermediary needs no key or plaintext. It echoes opaque request bytes
	// instead of forwarding the read, while forwarding the genuine handshake.
	intermediary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file/read" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, r.Body)
			return
		}
		response, err := http.Get(server.URL + r.URL.RequestURI())
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer intermediary.Close()
	transport := NewTransport(intermediary.URL, string(key))
	result, err := transport.FileRead(context.Background(), "unread-fixture.txt", 0, 128)
	if err == nil {
		t.Fatalf("reflected encrypted request accepted as a successful file read: %+v", result)
	}
}
