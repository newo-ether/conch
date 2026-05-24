package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/newo-ether/conch/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[conch-mcp] ")

	serverURL := os.Getenv("CONCH_SERVER_URL")
	apiKey := os.Getenv("CONCH_API_KEY")

	if serverURL == "" {
		log.Fatal("CONCH_SERVER_URL is required (e.g. http://192.168.1.100:14216)")
	}
	if apiKey == "" {
		log.Fatal("CONCH_API_KEY is required")
	}

	transport := mcp.NewTransport(serverURL, apiKey)

	log.Printf("connecting to %s ...", serverURL)
	if err := transport.Initialize(context.Background()); err != nil {
		log.Fatalf("failed to initialize: %v", err)
	}
	log.Println("connected, server public key verified")

	server := mcp.NewServer(transport)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Println("MCP server ready (stdio mode)")
	if err := server.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
