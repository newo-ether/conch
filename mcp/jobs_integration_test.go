package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	conchcrypto "github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

func TestTransportDurableJobLifecycleOverEncryptedProtocol(t *testing.T) {
	const apiKey = "job-integration-key"
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := shell.NewJobManager(
		shell.NewExecutor(5*time.Second, 5*time.Second),
		t.TempDir(),
		time.Hour,
		5*time.Second,
		64*1024,
		20,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})

	mux := http.NewServeMux()
	auth := handler.AuthMiddleware([]byte(apiKey), conchcrypto.NewNonceTracker())
	for action, path := range map[string]string{
		"start": "/jobs/start",
		"list":  "/jobs/list",
		"get":   "/jobs/get",
		"stop":  "/jobs/stop",
		"ack":   "/jobs/ack",
	} {
		mux.Handle("POST "+path, auth(&handler.JobHandler{
			Action:  action,
			Jobs:    manager,
			APIKey:  []byte(apiKey),
			KeyPair: keyPair,
		}))
	}
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

	transport := NewTransport(server.URL, apiKey)
	if err := transport.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	command := "printf 'job-output\n'"
	if runtime.GOOS == "windows" {
		command = "Write-Output 'job-output'"
	}
	started, err := transport.StartJob(context.Background(), command, 0, "")
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	var finished *ShellJobResult
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		finished, err = transport.GetJob(context.Background(), started.JobID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if finished.State != shell.JobStateRunning &&
			finished.State != shell.JobStateStopping &&
			finished.State != shell.JobStateSettling {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finished == nil || finished.State != shell.JobStateSucceeded {
		t.Fatalf("finished = %#v", finished)
	}
	if finished.Output == "" {
		t.Fatal("terminal job output missing")
	}

	list, err := transport.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list.Jobs) != 1 || list.Jobs[0].Output != "" {
		t.Fatalf("list result = %#v", list.Jobs)
	}
	ack, err := transport.AcknowledgeJob(context.Background(), started.JobID)
	if err != nil {
		t.Fatalf("AcknowledgeJob: %v", err)
	}
	if !ack.Acknowledged {
		t.Fatalf("ack = %#v", ack)
	}
}
