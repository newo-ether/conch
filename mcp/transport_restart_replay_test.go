package mcp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

// The intermediary never learns the API key. After forwarding an accepted
// command it replays the packet to a restarted server with a new key and nonce
// store, obtaining a genuine signed rejection for an already executed request.
func TestRestartRejectionCannotReplayAnAlreadyAcceptedCommand(t *testing.T) {
	const key = "restart-replay-fixture"
	backend := func() *httptest.Server {
		pair, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /public-key", signedPublicKeyHandler(t, []byte(key), pair))
		executor := &handler.ExecuteHandler{Executor: shell.NewExecutor(5*time.Second, 5*time.Second), APIKey: []byte(key), KeyPair: pair}
		mux.Handle("POST /execute", handler.AuthMiddleware([]byte(key), crypto.NewNonceTracker())(executor))
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		return server
	}
	original, restarted := backend(), backend()
	var rotated atomic.Bool
	var requests atomic.Int32
	intermediary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		forward := func(server *httptest.Server) *http.Response {
			request, err := http.NewRequestWithContext(r.Context(), r.Method, server.URL+r.URL.RequestURI(), bytes.NewReader(body))
			if err != nil {
				t.Error(err)
				return nil
			}
			request.Header = r.Header.Clone()
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Error(err)
				return nil
			}
			return response
		}
		target := original
		if rotated.Load() {
			target = restarted
		}
		response := forward(target)
		if response == nil {
			return
		}
		if r.URL.Path == "/execute" && requests.Add(1) == 1 {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			rotated.Store(true)
			response = forward(restarted)
			if response == nil {
				return
			}
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			w.Header()[name] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer intermediary.Close()
	marker := filepath.Join(t.TempDir(), "accepted.txt")
	command := "printf 'once\\n' >> '" + marker + "'"
	if runtime.GOOS == "windows" {
		command = "Add-Content -LiteralPath '" + strings.ReplaceAll(marker, "'", "''") + "' -Value 'once'"
	}
	_, callErr := NewTransport(intermediary.URL, key).Execute(context.Background(), command, 5000, "")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "once"); count != 1 || requests.Load() != 1 {
		t.Fatalf("signed restart rejection replayed execution: side effects=%d requests=%d", count, requests.Load())
	}
	if callErr == nil {
		t.Fatal("unknown delivery reported success")
	}
}
