package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
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
	mux.Handle("POST /execute", auth(&handler.ExecuteHandler{
		Jobs: manager, APIKey: []byte(apiKey), KeyPair: keyPair,
	}))
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		nonce, err := conchcrypto.GenerateNonce()
		if err != nil {
			http.Error(w, "nonce generation failed", http.StatusInternalServerError)
			return
		}
		publicKey := keyPair.PublicKeyBase64()
		_ = json.NewEncoder(w).Encode(conchcrypto.SignHandshake([]byte(apiKey), publicKey, nonce, r.URL.Query().Get("challenge")))
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
	started, err := transport.StartJob(context.Background(), command, 0, "", "Verify durable background output")
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
	if finished.Output == "" || finished.Description != "Verify durable background output" {
		t.Fatalf("terminal output or description missing: %#v", finished)
	}

	events, err := transport.Execute(context.Background(), command, 5000, "", "Verify retained foreground output")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var foregroundID string
	for _, event := range events {
		if event.ExitCode != nil {
			foregroundID = event.JobID
		}
	}
	foreground, err := transport.GetJob(context.Background(), foregroundID)
	if err != nil || foreground.State != shell.JobStateSucceeded || foreground.Output == "" ||
		foreground.Description != "Verify retained foreground output" {
		t.Fatalf("foreground result = %#v, err = %v", foreground, err)
	}

	list, err := transport.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list.Jobs) != 2 || list.Jobs[0].Output != "" {
		t.Fatalf("list result = %#v", list.Jobs)
	}
	page, err := transport.ListJobs(context.Background(), 0, 1)
	if err != nil || len(page.Jobs) != 1 || !page.HasMore || page.NextOffset != 1 || page.Total != 2 {
		t.Fatalf("first page = %#v, err = %v", page, err)
	}
	next, err := transport.ListJobs(context.Background(), page.NextOffset, 1)
	if err != nil || len(next.Jobs) != 1 || next.HasMore || next.Jobs[0].JobID == page.Jobs[0].JobID {
		t.Fatalf("next page = %#v, err = %v", next, err)
	}
	ack, err := transport.AcknowledgeJob(context.Background(), started.JobID)
	if err != nil {
		t.Fatalf("AcknowledgeJob: %v", err)
	}
	if !ack.Acknowledged {
		t.Fatalf("ack = %#v", ack)
	}
	retained, err := transport.GetJob(context.Background(), started.JobID)
	if err != nil || retained.Output != finished.Output || retained.Description != finished.Description {
		t.Fatalf("ack lost retained result: %#v, err = %v", retained, err)
	}
	timeoutCommand := "sleep 10"
	if runtime.GOOS == "windows" {
		timeoutCommand = "Start-Sleep -Seconds 10"
	}
	failedEvents, err := transport.Execute(context.Background(), timeoutCommand, 100, "", "Verify failed foreground retention")
	if err != nil {
		t.Fatalf("timed-out Execute: %v", err)
	}
	var failedID string
	for _, event := range failedEvents {
		if event.Error != "" {
			failedID = event.JobID
		}
	}
	failed, err := transport.GetJob(context.Background(), failedID)
	if err != nil || failed.State != shell.JobStateFailed || failed.Error == "" ||
		failed.Description != "Verify failed foreground retention" {
		t.Fatalf("failed foreground snapshot must stay retrievable: %#v, err = %v", failed, err)
	}

	t.Run("missing workdir preserves actionable errors", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing folder")
		for _, background := range []bool{false, true} {
			var id string
			if background {
				started, err := transport.StartJob(context.Background(), command, 5000, missing)
				if err != nil {
					t.Fatalf("StartJob: %v", err)
				}
				id = started.JobID
			} else {
				events, err := transport.Execute(context.Background(), command, 5000, missing)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				for _, event := range events {
					if event.Error != "" {
						id = event.JobID
					}
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				job, err := transport.GetJob(context.Background(), id)
				if err != nil {
					t.Fatalf("GetJob: %v", err)
				}
				if job.State == shell.JobStateFailed {
					if !strings.Contains(job.Error, "workdir does not exist:") ||
						!strings.Contains(job.Error, "missing folder") || job.Output != "" || job.ExitCode != nil {
						t.Fatalf("missing workdir result: %#v", job)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("missing workdir did not fail: %#v", job)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	})
}
