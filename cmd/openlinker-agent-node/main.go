package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-agent-node/internal/agentnode"
)

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if len(os.Args) > 1 {
		if os.Args[1] == "runtime-credential" {
			if err := prepareCredentialCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				logger.Fatal(err)
			}
			return
		}
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			if _, err := fmt.Fprintln(os.Stdout, agentnode.AgentNodeVersion); err != nil {
				logger.Fatal("openlinker agent node version output failed")
			}
			return
		}
		if len(os.Args) == 2 && os.Args[1] == "--check-config" {
			node, err := agentnode.NewFromEnv()
			if err != nil {
				logger.Fatalf("openlinker agent node config: %v", err)
			}
			if err := json.NewEncoder(os.Stdout).Encode(node.ConfigurationReport()); err != nil {
				logger.Fatal("openlinker agent node configuration output failed")
			}
			return
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runTransportCommand(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
			logger.Fatal(err)
		}
		return
	}

	node, err := agentnode.NewFromEnv()
	if err != nil {
		logger.Fatalf("openlinker agent node config: %v", err)
	}
	node.Logger = logger

	// A buffered channel keeps both the first (drain) and a second (stop without
	// draining) signal while the Node is shutting down.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := node.Serve(signals); err != nil {
		var timeout *agentnode.ShutdownTimeoutError
		if errors.As(err, &timeout) {
			logger.Fatalf("openlinker agent node shutdown timed out: %v", timeout.Err)
		}
		logger.Fatalf("openlinker agent node stopped: %v", err)
	}
}
