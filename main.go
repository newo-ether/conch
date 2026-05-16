package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/newoether/conch/config"
	"github.com/newoether/conch/handler"
	"github.com/newoether/conch/shell"
)

func main() {
	cfg := config.Load()

	if cfg.APIKey == "" {
		log.Println("WARNING: CONCH_API_KEY is not set — all requests will be allowed without authentication")
	}

	executor := shell.NewExecutor(cfg.Timeout, cfg.MaxTimeout)
	executeHandler := &handler.ExecuteHandler{Executor: executor}

	mux := http.NewServeMux()
	mux.Handle("POST /execute", handler.AuthMiddleware(cfg.APIKey)(executeHandler))
	mux.HandleFunc("GET /health", handler.HealthHandler)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE requires unbounded write timeout
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("conch listening on :%d", cfg.Port)
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
