package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/kinzhi/openlinker-agent-node/internal/agentnode"
	"github.com/stretchr/testify/require"
)

func TestRunReportsConfigurationError(t *testing.T) {
	var stderr bytes.Buffer
	code := run(
		func() (*agentnode.Node, error) { return nil, errors.New("missing runtime token") },
		canceledNotify,
		time.Second,
		&stderr,
	)

	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "openlinker-agent-node: missing runtime token")
}

func TestRunNodeReportsStartError(t *testing.T) {
	node := &fakeNodeRunner{startErr: errors.New("connector failed")}
	var stderr bytes.Buffer

	code := runNode(func() (nodeRunner, error) { return node, nil }, canceledNotify, time.Second, &stderr)

	require.Equal(t, 1, code)
	require.Equal(t, 1, node.starts)
	require.Zero(t, node.stops)
	require.Contains(t, stderr.String(), "openlinker-agent-node: connector failed")
}

func TestRunNodeStopsAfterSignalContext(t *testing.T) {
	node := &fakeNodeRunner{}
	var capturedSignals []os.Signal
	notify := func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		capturedSignals = append([]os.Signal(nil), signals...)
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() {}
	}

	code := runNode(func() (nodeRunner, error) { return node, nil }, notify, 25*time.Millisecond, &bytes.Buffer{})

	require.Equal(t, 0, code)
	require.Equal(t, 1, node.starts)
	require.Equal(t, 1, node.stops)
	require.Equal(t, []os.Signal{os.Interrupt, syscall.SIGTERM}, capturedSignals)
	require.NotNil(t, node.stopDeadline)
	require.WithinDuration(t, time.Now().Add(25*time.Millisecond), *node.stopDeadline, 50*time.Millisecond)
}

func TestRunNodeIgnoresCanceledShutdown(t *testing.T) {
	node := &fakeNodeRunner{stopErr: context.Canceled}

	code := runNode(func() (nodeRunner, error) { return node, nil }, canceledNotify, time.Second, &bytes.Buffer{})

	require.Equal(t, 0, code)
	require.Equal(t, 1, node.stops)
}

func TestRunNodeReportsShutdownError(t *testing.T) {
	node := &fakeNodeRunner{stopErr: errors.New("flush failed")}
	var stderr bytes.Buffer

	code := runNode(func() (nodeRunner, error) { return node, nil }, canceledNotify, time.Second, &stderr)

	require.Equal(t, 1, code)
	require.Equal(t, 1, node.stops)
	require.Contains(t, stderr.String(), "openlinker-agent-node shutdown: flush failed")
}

func canceledNotify(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	cancel()
	return ctx, func() {}
}

type fakeNodeRunner struct {
	startErr     error
	stopErr      error
	starts       int
	stops        int
	startCtx     context.Context
	stopCtx      context.Context
	stopDeadline *time.Time
}

func (n *fakeNodeRunner) Start(ctx context.Context) error {
	n.starts++
	n.startCtx = ctx
	return n.startErr
}

func (n *fakeNodeRunner) Stop(ctx context.Context) error {
	n.stops++
	n.stopCtx = ctx
	if deadline, ok := ctx.Deadline(); ok {
		n.stopDeadline = &deadline
	}
	return n.stopErr
}
