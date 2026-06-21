package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kinzhi/openlinker-agent-node/internal/agentnode"
)

func main() {
	node, err := agentnode.NewFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "openlinker-agent-node: %v\n", err)
		os.Exit(2)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if err := node.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "openlinker-agent-node: %v\n", err)
		os.Exit(1)
	}

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), agentnode.DefaultShutdownTimeout)
	defer cancel()
	if err := node.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "openlinker-agent-node shutdown: %v\n", err)
		os.Exit(1)
	}
}
