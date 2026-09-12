package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/conch/crypto"
)

func TestOldOrReplayedHandshakeCannotDispatchMutations(t *testing.T) {
	key := []byte("isolated-preflight-authority")
	pair, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"old", "replayed", "forged"} {
		t.Run(variant, func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/public-key" {
					mutations.Add(1)
					w.WriteHeader(500)
					return
				}
				doc := crypto.SignHandshake(key, pair.PublicKeyBase64(), "server-nonce", r.URL.Query().Get("challenge"))
				switch variant {
				case "old":
					doc.Features = ""
					doc.FeaturesSignature = ""
				case "replayed":
					doc = crypto.SignHandshake(key, pair.PublicKeyBase64(), "server-nonce", "earlier-challenge")
				case "forged":
					doc.FeaturesSignature = "untrusted"
				}
				_ = json.NewEncoder(w).Encode(doc)
			}))
			defer server.Close()
			transport := NewTransport(server.URL, string(key))
			if err := transport.doFileRequest(context.Background(), "/file/write", []byte(`{"path":"unused","content":"unused"}`), &struct{}{}); err == nil {
				t.Fatal("unsafe server accepted")
			}
			if mutations.Load() != 0 {
				t.Fatal("request escaped before security preflight")
			}
		})
	}
}
