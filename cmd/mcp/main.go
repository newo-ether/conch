package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/newo-ether/conch/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[conch-mcp] ")

	transports := make(map[string]*mcp.Transport)
	unavailable := make(map[string]string)
	var devices map[string]mcp.DeviceConfig

	if devicesJSON := os.Getenv("CONCH_DEVICES"); devicesJSON != "" {
		if err := json.Unmarshal([]byte(devicesJSON), &devices); err != nil {
			log.Fatalf("CONCH_DEVICES is invalid JSON: %v", err)
		}
		if len(devices) == 0 {
			log.Fatal("CONCH_DEVICES must have at least one device")
		}

		for name, cfg := range devices {
			if cfg.URL == "" || cfg.Key == "" {
				unavailable[name] = "url and key are required"
				log.Printf("[%s] unavailable: %s", name, unavailable[name])
				continue
			}
			t := mcp.NewTransport(cfg.URL, cfg.Key)
			log.Printf("[%s] connecting to %s ...", name, cfg.URL)
			connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := t.Initialize(connectCtx)
			cancel()
			if err != nil {
				unavailable[name] = err.Error()
				log.Printf("[%s] unavailable: %v", name, err)
				continue
			}
			log.Printf("[%s] connected, server public key verified", name)
			transports[name] = t
		}
		if len(transports) == 0 {
			log.Fatalf("no configured Conch device is available (%d failed)", len(unavailable))
		}
	} else {
		serverURL := os.Getenv("CONCH_SERVER_URL")
		apiKey := os.Getenv("CONCH_API_KEY")
		if serverURL == "" {
			log.Fatal("CONCH_SERVER_URL is required (e.g. http://192.168.1.100:14216), or use CONCH_DEVICES")
		}
		if apiKey == "" {
			log.Fatal("CONCH_API_KEY is required, or use CONCH_DEVICES")
		}

		t := mcp.NewTransport(serverURL, apiKey)
		log.Printf("connecting to %s ...", serverURL)
		if err := t.Initialize(context.Background()); err != nil {
			log.Fatalf("failed to initialize: %v", err)
		}
		log.Println("connected, server public key verified")
		transports["default"] = t
	}

	server := mcp.NewServerWithUnavailable(transports, devices, unavailable)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("MCP server ready with %d device(s) (stdio mode)", len(transports))
	if err := server.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
