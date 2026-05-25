package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/newo-ether/conch/config"
	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/handler"
	"github.com/newo-ether/conch/shell"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[conch] ")

	cfg := config.Load()

	if cfg.APIKey == "" && !cfg.AllowNoAuth {
		log.Fatal("CONCH_API_KEY is required (or set CONCH_ALLOW_NO_AUTH=true for development)")
	}

	executor := shell.NewExecutor(cfg.Timeout, cfg.MaxTimeout)
	nonceTracker := crypto.NewNonceTracker()

	var keyPair *crypto.KeyPair
	var apiKeyBytes []byte
	if cfg.APIKey != "" {
		apiKeyBytes = []byte(cfg.APIKey)
		var err error
		keyPair, err = crypto.GenerateKeyPair()
		if err != nil {
			log.Fatalf("failed to generate key pair: %v", err)
		}
		log.Printf("server public key: %s", keyPair.PublicKeyBase64())
	} else {
		log.Println("WARNING: running without authentication (CONCH_ALLOW_NO_AUTH=true)")
	}

	rateLimiter := handler.NewRateLimiter(5, 10)
	authMw := handler.AuthMiddleware(apiKeyBytes, nonceTracker)

	executeHandler := &handler.ExecuteHandler{
		Executor: executor,
		APIKey:   apiKeyBytes,
		KeyPair:  keyPair,
	}
	fileReadHandler := &handler.FileReadHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileWriteHandler := &handler.FileWriteHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileGlobHandler := &handler.FileGlobHandler{APIKey: apiKeyBytes, KeyPair: keyPair}
	fileGrepHandler := &handler.FileGrepHandler{APIKey: apiKeyBytes, KeyPair: keyPair}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.HealthHandler)
	mux.Handle("/execute", authMw(executeHandler))
	mux.Handle("/file/read", authMw(fileReadHandler))
	mux.Handle("/file/write", authMw(fileWriteHandler))
	mux.Handle("/file/glob", authMw(fileGlobHandler))
	mux.Handle("/file/grep", authMw(fileGrepHandler))

	var h http.Handler = mux
	if cfg.APIKey != "" {
		h = rateLimiter.Middleware(mux)
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      h,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	// run: starts the HTTP server
	run := func() error {
		log.Printf("listening on %s", addr)
		err := srv.ListenAndServe()
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	}

	// stop: gracefully shuts down the server
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}

	// Try Windows service path; on other platforms this is a no-op.
	if runService("Conch", run, stop) {
		// Running as Windows service — runService blocks until SCM stops us.
		log.Println("service stopped")
		return
	}

	// Standard path (Linux, Termux, macOS, or Windows interactive)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("received %v, shutting down...", sig)
		stop()
	}()

	if err := run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
	log.Println("server stopped")
}
