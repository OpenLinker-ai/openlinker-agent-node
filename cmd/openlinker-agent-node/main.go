package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kinzhi/openlinker-agent-node/internal/agentnode"
)

type nodeRunner interface {
	Start(context.Context) error
	Stop(context.Context) error
}

type notifyContextFunc func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)

func main() {
	os.Exit(run(agentnode.NewFromEnv, signal.NotifyContext, agentnode.DefaultShutdownTimeout, os.Stderr))
}

func run(newNode func() (*agentnode.Node, error), notify notifyContextFunc, shutdownTimeout time.Duration, stderr io.Writer) int {
	node, err := newNode()
	return runNode(func() (nodeRunner, error) { return node, err }, notify, shutdownTimeout, stderr)
}

func runNode(newNode func() (nodeRunner, error), notify notifyContextFunc, shutdownTimeout time.Duration, stderr io.Writer) int {
	node, err := newNode()
	if err != nil {
		fmt.Fprintf(stderr, "openlinker-agent-node: %v\n", err)
		return 2
	}

	ctx, stopSignals := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if err := node.Start(ctx); err != nil {
		fmt.Fprintf(stderr, "openlinker-agent-node: %v\n", err)
		return 1
	}

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := node.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "openlinker-agent-node shutdown: %v\n", err)
		return 1
	}
	return 0
}
