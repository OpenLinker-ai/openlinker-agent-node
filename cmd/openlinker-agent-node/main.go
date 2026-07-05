package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-agent-node/internal/agentnode"
)

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags)

	node, err := agentnode.NewFromEnv()
	if err != nil {
		logger.Fatalf("openlinker agent node config: %v", err)
	}
	node.Logger = logger

	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	errCh := make(chan error, 1)
	go func() {
		errCh <- node.Start(runCtx)
	}()

	select {
	case err := <-errCh:
		exitOnUnexpectedError(logger, err)
	case <-runCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), agentnode.DefaultShutdownTimeout)
		defer cancel()
		if err := node.Stop(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("openlinker agent node shutdown: %v", err)
		}
		waitForStop(logger, errCh, shutdownCtx)
	}
}

func waitForStop(logger *log.Logger, errCh <-chan error, shutdownCtx context.Context) {
	select {
	case err := <-errCh:
		exitOnUnexpectedError(logger, err)
	case <-shutdownCtx.Done():
		logger.Fatalf("openlinker agent node shutdown timed out: %v", shutdownCtx.Err())
	}
}

func exitOnUnexpectedError(logger *log.Logger, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatalf("openlinker agent node stopped: %v", err)
	}
}
