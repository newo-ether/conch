package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/newo-ether/conch/buildinfo"
	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

// The intermediary has no API key. It forwards the authenticated request, then
// replaces the completed response with a plaintext stale-key error.
func TestUnauthenticatedRejectionCannotReplayAcceptedExecution(t *testing.T) {
	const apiKey = "isolated-transport-security-fixture"
	kp, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	executor := &handler.ExecuteHandler{
		Executor: shell.NewExecutor(5*time.Second, 5*time.Second),
		APIKey:   []byte(apiKey), KeyPair: kp,
	}
	accepted := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())(executor)
	mux := http.NewServeMux()
	mux.Handle("POST /execute", accepted)
	mux.HandleFunc("GET /public-key", signedPublicKeyHandler(t, []byte(apiKey), kp))
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildinfo.Current("conch"))
	})
	native := httptest.NewServer(mux)
	defer native.Close()
	var requests atomic.Int32
	intermediary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward, err := http.NewRequestWithContext(r.Context(), r.Method, native.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		forward.Header = r.Header.Clone()
		response, err := http.DefaultClient.Do(forward)
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		if r.URL.Path == "/execute" && requests.Add(1) == 1 {
			_, _ = io.Copy(io.Discard, response.Body)
			http.Error(w, `{"error":"decryption failed"}`, http.StatusBadRequest)
			return
		}
		for key, values := range response.Header {
			w.Header()[key] = values
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
	transport := NewTransport(intermediary.URL, apiKey)
	_, callErr := transport.Execute(context.Background(), command, 5000, "")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "once"); count != 1 {
		t.Fatalf("unauthenticated response replayed accepted execution: side effects=%d, HTTP calls=%d", count, requests.Load())
	}
	if callErr == nil {
		t.Fatal("replaced response must report uncertain delivery, not success")
	}
}

func TestEncryptedStreamRejectsDuplicatedAuthenticatedFrame(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	line, err := conchcrypto.EncryptEvent(key, "line", 0, []byte(`{"line":"one","stream":"stdout"}`))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := conchcrypto.EncryptEvent(key, "result", 1, []byte(`{"exit_code":0}`))
	if err != nil {
		t.Fatal(err)
	}
	stream := fmt.Sprintf("event: line\ndata: %s\n\nevent: line\ndata: %s\n\nevent: result\ndata: %s\n\n", line, line, terminal)
	if events, err := parseSSE(strings.NewReader(stream), key); err == nil {
		t.Fatalf("duplicated encrypted frame accepted: %d events", len(events))
	}
}

func TestStreamMutationCannotLookLikeSuccessfulCompletion(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	otherKey := []byte("abcdef0123456789abcdef0123456789")
	frame := func(event string, sequence uint64, payload string, secret []byte) string {
		encoded, err := conchcrypto.EncryptEvent(secret, event, sequence, []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("event: %s\ndata: %s\n\n", event, encoded)
	}
	a := frame("line", 0, `{"line":"first","stream":"stdout"}`, key)
	b := frame("line", 1, `{"line":"second","stream":"stdout"}`, key)
	terminal := frame("result", 2, `{"exit_code":0}`, key)
	if events, err := parseSSE(strings.NewReader(a+b+terminal), key); err != nil || len(events) != 3 {
		t.Fatalf("legitimate stream failed: %v", err)
	}
	for name, wire := range map[string]string{
		"duplicate":          a + a + b + terminal,
		"reorder":            b + a + terminal,
		"removed first":      b + terminal,
		"removed middle":     a + terminal,
		"truncated terminal": a + b,
		"renamed event":      strings.Replace(a, "event: line", "event: warning", 1) + b + terminal,
		"another request":    frame("line", 0, `{"line":"first","stream":"stdout"}`, otherKey) + b + terminal,
		"post-terminal":      a + b + terminal + a,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSSE(strings.NewReader(wire), key); err == nil {
				t.Fatal("altered stream accepted")
			}
		})
	}
}
