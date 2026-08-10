package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/newo-ether/conch/buildinfo"
	"github.com/newo-ether/conch/mcp"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(buildinfo.String("conch-mcp"))
		return
	}

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

		var initialization sync.WaitGroup
		var statusMu sync.Mutex
		for name, cfg := range devices {
			if cfg.URL == "" || cfg.Key == "" {
				unavailable[name] = "url and key are required"
				log.Printf("[%s] unavailable: %s", name, unavailable[name])
				continue
			}
			transport := mcp.NewTransport(cfg.URL, cfg.Key)
			transports[name] = transport
			initialization.Add(1)
			go func(name string, cfg mcp.DeviceConfig, transport *mcp.Transport) {
				defer initialization.Done()
				log.Printf("[%s] connecting to %s ...", name, cfg.URL)
				connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := transport.Initialize(connectCtx)
				cancel()
				if err != nil {
					statusMu.Lock()
					unavailable[name] = err.Error()
					statusMu.Unlock()
					log.Printf("[%s] offline at startup; tool calls will reconnect: %v", name, err)
					return
				}
				log.Printf("[%s] connected, server public key verified", name)
			}(name, cfg, transport)
		}
		initialization.Wait()
		if len(transports) == 0 {
			log.Fatalf("no valid Conch device configuration (%d invalid)", len(unavailable))
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
		transports["default"] = t
		log.Printf("connecting to %s ...", serverURL)
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := t.Initialize(connectCtx)
		cancel()
		if err != nil {
			unavailable["default"] = err.Error()
			log.Printf("server offline at startup; tool calls will reconnect: %v", err)
		} else {
			log.Println("connected, server public key verified")
		}
	}

	server := mcp.NewServerWithUnavailable(transports, devices, unavailable)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("MCP server ready with %d device(s) (stdio mode)", len(transports))
	if err := server.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
