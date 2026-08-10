package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/conch/buildinfo"
	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

func TestTransportRefreshesRotatedKeyWithoutDuplicateCommandExecution(t *testing.T) {
	const apiKey = "execute-rotation-key"
	currentKey, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	executeHandler := &handler.ExecuteHandler{
		Executor: shell.NewExecutor(5*time.Second, 5*time.Second),
		APIKey:   []byte(apiKey),
		KeyPair:  currentKey,
	}
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /execute", auth(executeHandler))
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
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(buildinfo.Current("conch"))
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
	executeHandler.KeyPair = rotatedKey

	marker := filepath.Join(t.TempDir(), "side-effect.txt")
	command := "printf 'once\\n' >> '" + marker + "'"
	if runtime.GOOS == "windows" {
		command = "$p = '" + strings.ReplaceAll(marker, "'", "''") +
			"'; Add-Content -LiteralPath $p -Value 'once'"
	}
	events, err := transport.Execute(context.Background(), command, 5000, "")
	if err != nil {
		t.Fatalf("Execute after key rotation: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].ExitCode == nil ||
		*events[len(events)-1].ExitCode != 0 {
		t.Fatalf("events = %#v", events)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "once") != 1 {
		t.Fatalf("command side effect executed more than once: %q", data)
	}
}
