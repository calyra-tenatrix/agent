package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/calyra-tenatrix/agent/internal/agent"
	"github.com/calyra-tenatrix/agent/internal/config"
	"github.com/calyra-tenatrix/agent/internal/updater"
)

var version = "dev"

func main() {
	mode := flag.String("mode", "run", "Mode to run: 'run' or 'update'")
	configPath := flag.String("config", "/etc/tenatrix/config.json", "Path to config file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create context that listens for interrupt signals
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Received shutdown signal, gracefully shutting down...")
		cancel()
	}()

	switch *mode {
	case "run":
		log.Printf("Starting Tenatrix Agent v%s", version)
		if err := agent.Run(ctx, cfg, version); err != nil {
			log.Fatalf("Agent failed: %v", err)
		}
	case "update":
		log.Printf("Checking for updates (current version: %s)", version)
		if err := updater.CheckAndUpdate(ctx, cfg, version); err != nil {
			log.Fatalf("Update failed: %v", err)
		}
	default:
		log.Fatalf("Unknown mode: %s (use 'run' or 'update')", *mode)
	}
}
