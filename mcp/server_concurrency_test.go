package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

func TestServerProcessesConcurrentRequestsAndCancellation(t *testing.T) {
	const apiKey = "stdio-concurrency-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	executor := shell.NewExecutor(30*time.Second, 30*time.Second)
	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	mux.Handle("POST /execute", auth(&handler.ExecuteHandler{
		Executor: executor,
		APIKey:   []byte(apiKey),
		KeyPair:  keyPair,
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
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	transport := NewTransport(httpServer.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	server := NewServer(map[string]*Transport{"test": transport}, nil)

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(context.Background(), inputReader, outputWriter)
		_ = outputWriter.Close()
	}()

	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	encoder := json.NewEncoder(inputWriter)
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "shell_execute",
			"arguments": map[string]any{
				"device":     "test",
				"command":    command,
				"timeout_ms": 30000,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}); err != nil {
		t.Fatal(err)
	}

	responses := make(chan Response, 2)
	scanErrors := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(outputReader)
		for scanner.Scan() {
			var response Response
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				scanErrors <- err
				return
			}
			responses <- response
		}
		scanErrors <- scanner.Err()
	}()

	select {
	case response := <-responses:
		if response.ID != float64(2) {
			t.Fatalf("first response id = %#v, want concurrent tools/list id 2", response.ID)
		}
	case err := <-scanErrors:
		t.Fatalf("output scan failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("tools/list was blocked behind long shell_execute")
	}

	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/cancelled",
		"params":  map[string]any{"requestId": 1},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responses:
		if response.ID != float64(1) || response.Error == nil || response.Error.Code != -32800 {
			t.Fatalf("cancel response = %#v", response)
		}
	case err := <-scanErrors:
		t.Fatalf("output scan failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled shell request did not terminate")
	}

	_ = inputWriter.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after input closed")
	}
}
