package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/newo-ether/conch/config"
	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

func main() {
	cfg := config.Load()

	if cfg.APIKey == "" && !cfg.AllowNoAuth {
		log.Fatalf("CONCH_API_KEY is required. Set CONCH_ALLOW_NO_AUTH=true to override (insecure).")
	}
	if cfg.APIKey == "" {
		log.Println("WARNING: CONCH_API_KEY is not set — all requests will be allowed without authentication")
	}

	// Generate persistent X25519 key pair
	keyPair, err := crypto.GenerateKeyPair()
	if err != nil {
		log.Fatalf("failed to generate X25519 key pair: %v", err)
	}
	log.Printf("Server public key: %s", keyPair.PublicKeyBase64())

	nonceTracker := crypto.NewNonceTracker()
	rateLimiter := handler.NewRateLimiter(20, 40) // 20 req/s sustained, burst 40
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
	mux.Handle("POST /file/read", auth(fileReadHandler))
	mux.Handle("POST /file/image", auth(fileImageHandler))
	mux.Handle("POST /file/write", auth(fileWriteHandler))
	mux.Handle("POST /file/glob", auth(fileGlobHandler))
	mux.Handle("POST /file/grep", auth(fileGrepHandler))
	mux.HandleFunc("GET /health", handler.HealthHandler)
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
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("conch listening on %s:%d", cfg.Host, cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
}
