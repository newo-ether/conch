package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/newo-ether/conch/buildinfo"
	"github.com/newo-ether/conch/config"
	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(buildinfo.String("conch"))
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	if cfg.APIKey == "" && !cfg.AllowNoAuth {
		log.Fatalf("CONCH_API_KEY is required. Set CONCH_ALLOW_NO_AUTH=true to override (insecure).")
	}
	if cfg.APIKey == "" {
		log.Println("WARNING: CONCH_API_KEY is not set — all requests will be allowed without authentication")
	}

	// Generate the process-local X25519 key pair advertised by /version
	keyPair, err := crypto.GenerateKeyPair()
	if err != nil {
		log.Fatalf("failed to generate X25519 key pair: %v", err)
	}
	log.Printf("Server public key: %s", keyPair.PublicKeyBase64())

	nonceTracker := crypto.NewNonceTracker()
	rateLimiter := handler.NewRateLimiter(20, 40, cfg.TrustedProxies...) // 20 req/s sustained, burst 40
	defer rateLimiter.Close()
	apiKeyBytes := []byte(cfg.APIKey)

	executor := shell.NewExecutor(cfg.Timeout, cfg.MaxTimeout)
	jobManager, err := shell.NewJobManager(
		executor,
		cfg.JobDir,
		cfg.JobRetention,
		cfg.MaxJobRuntime,
		cfg.MaxJobOutputBytes,
		cfg.MaxJobs,
	)
	if err != nil {
		log.Fatalf("failed to initialize shell job manager: %v", err)
	}
	executeHandler := &handler.ExecuteHandler{
		Executor: executor,
		APIKey:   apiKeyBytes,
		KeyPair:  keyPair,
	}

	mux := http.NewServeMux()
	auth := handler.AuthMiddleware(apiKeyBytes, nonceTracker)

	fileReadHandler := &handler.FileReadHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileImageHandler := &handler.FileImageHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileWriteHandler := &handler.FileWriteHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileEditHandler := &handler.FileEditHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileGlobHandler := &handler.FileGlobHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileGrepHandler := &handler.FileGrepHandler{APIKey: apiKeyBytes, KeyPair: keyPair}

	mux.Handle("POST /execute", auth(executeHandler))
	mux.Handle("POST /jobs/start", auth(&handler.JobHandler{
		Action: "start", Jobs: jobManager, APIKey: apiKeyBytes, KeyPair: keyPair,
	}))
	mux.Handle("POST /jobs/list", auth(&handler.JobHandler{
		Action: "list", Jobs: jobManager, APIKey: apiKeyBytes, KeyPair: keyPair,
	}))
	mux.Handle("POST /jobs/get", auth(&handler.JobHandler{
		Action: "get", Jobs: jobManager, APIKey: apiKeyBytes, KeyPair: keyPair,
	}))
	mux.Handle("POST /jobs/stop", auth(&handler.JobHandler{
		Action: "stop", Jobs: jobManager, APIKey: apiKeyBytes, KeyPair: keyPair,
	}))
	mux.Handle("POST /jobs/ack", auth(&handler.JobHandler{
		Action: "ack", Jobs: jobManager, APIKey: apiKeyBytes, KeyPair: keyPair,
	}))
	mux.Handle("POST /file/read", auth(fileReadHandler))
	mux.Handle("POST /file/image", auth(fileImageHandler))
	mux.Handle("POST /file/write", auth(fileWriteHandler))
	mux.Handle("POST /file/edit", auth(fileEditHandler))
	mux.Handle("POST /file/glob", auth(fileGlobHandler))
	mux.Handle("POST /file/grep", auth(fileGrepHandler))
	mux.HandleFunc("GET /health", handler.HealthHandler)
	mux.HandleFunc("GET /version", handler.VersionHandler)
	mux.HandleFunc("GET /public-key", func(w http.ResponseWriter, r *http.Request) {
		nonce, err := crypto.GenerateNonce()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		pubKey := keyPair.PublicKeyBase64()
		sig := crypto.SignPayload(apiKeyBytes, nonce, pubKey)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"public_key": pubKey,
			"nonce":      nonce,
			"signature":  sig,
		})
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:           rateLimiter.Middleware(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0, // SSE requires unbounded write timeout
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("conch listening on %s:%d", cfg.Host, cfg.Port)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Println("shutdown signal received")
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("ERROR: HTTP server stopped unexpectedly: %v", err)
		}
		stop()
	}
	log.Println("shutting down...")

	jobShutdownCtx, cancelJobs := context.WithTimeout(context.Background(), 10*time.Second)
	if err := jobManager.Close(jobShutdownCtx); err != nil {
		log.Printf("ERROR: background job shutdown incomplete: %v", err)
	}
	cancelJobs()

	shutdownCtx, cancelServer := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("ERROR: HTTP shutdown incomplete: %v", err)
	}
	cancelServer()
}
