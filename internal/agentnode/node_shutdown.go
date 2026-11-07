package agentnode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// ShutdownTimeoutError reports that a signal-initiated shutdown did not finish
// within ProcessShutdownTimeout.
type ShutdownTimeoutError struct {
	Err error
}

func (err *ShutdownTimeoutError) Error() string { return err.Err.Error() }

func (err *ShutdownTimeoutError) Unwrap() error { return err.Err }

// Shutdown gracefully stops the Node. First the public A2A listener stops
// accepting connections and requests; its in-flight requests may finish within
// DefaultShutdownTimeout and Shutdown waits for them, whether or not the Worker
// is ready, so it cannot create new Core work while admission is fenced and an
// active request is not cut off by the process exiting. A ready SDK Worker is
// then drained: the SDK fences new admission, asks Core to drain the Session
// and lets in-flight Attempts finish within DefaultDrainTimeout. A Worker that
// has not reported ready has opened no admission and is stopped directly. The
// localhost helper keeps serving in-flight Runs until Stop. Drain fails closed
// inside the SDK, and Stop always follows, so in-flight work still receives
// the SDK Worker's own shutdown grace when the drain cannot complete.
func (node *Node) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	node.mu.Lock()
	started, ready, worker := node.started, node.ready, node.worker
	node.mu.Unlock()
	if !started {
		return node.Stop(ctx)
	}

	var publicStopped <-chan error
	if node.PublicA2A != nil {
		// Same bound the listener had when the signal stopped it directly.
		publicCtx, cancelPublic := context.WithTimeout(ctx, DefaultShutdownTimeout)
		defer cancelPublic()
		publicStopped = node.PublicA2A.beginStop(publicCtx)
	}
	// Only the drain depends on readiness.
	if ready && worker != nil {
		err := worker.Drain(ctx, openlinker.RuntimeWorkerDrainOptions{Timeout: node.effectiveDrainTimeout()})
		if err != nil && !errors.Is(err, context.Canceled) && node.Logger != nil {
			node.Logger.Printf("openlinker agent node drain did not complete; stopping: %v", err)
		}
	}
	stopErr := node.Stop(ctx)
	if publicStopped != nil {
		select {
		case publicErr := <-publicStopped:
			if stopErr == nil && publicErr != nil && !errors.Is(publicErr, context.Canceled) {
				stopErr = fmt.Errorf("stop public A2A listener: %w", publicErr)
			}
		case <-ctx.Done():
		}
	}
	return stopErr
}

func (node *Node) effectiveDrainTimeout() time.Duration {
	if node.drainTimeout > 0 {
		return node.drainTimeout
	}
	return DefaultDrainTimeout
}

// Serve runs the Node until it stops on its own or a signal arrives. The first
// signal starts a graceful Shutdown bounded by ProcessShutdownTimeout; a second
// signal abandons the drain and stops the Worker directly. A nil result means
// the Node stopped cleanly; *ShutdownTimeoutError means it did not stop in time.
func (node *Node) Serve(signals <-chan os.Signal) error {
	return node.serve(signals, ProcessShutdownTimeout)
}

func (node *Node) serve(signals <-chan os.Signal, budget time.Duration) error {
	// The signal must not cancel the Start parent directly: that would stop the
	// Worker immediately and leave nothing to drain.
	startCtx, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	stopped := make(chan error, 1)
	go func() {
		stopped <- node.Start(startCtx)
	}()

	select {
	case err := <-stopped:
		return unexpectedStop(err)
	case <-signals:
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), budget)
	defer cancelShutdown()
	drainCtx, cancelDrain := context.WithCancel(shutdownCtx)
	defer cancelDrain()
	go func() {
		select {
		case <-signals:
			if node.Logger != nil {
				node.Logger.Printf("openlinker agent node received a second signal; stopping without waiting for the drain")
			}
			cancelDrain()
		case <-drainCtx.Done():
		}
	}()
	if err := node.Shutdown(drainCtx); err != nil && !errors.Is(err, context.Canceled) && node.Logger != nil {
		node.Logger.Printf("openlinker agent node shutdown: %v", err)
	}
	// Covers a signal that arrived before Start marked the Node as started.
	cancelStart()

	select {
	case err := <-stopped:
		return unexpectedStop(err)
	case <-shutdownCtx.Done():
		select {
		case err := <-stopped:
			return unexpectedStop(err)
		default:
		}
		return &ShutdownTimeoutError{Err: shutdownCtx.Err()}
	}
}

func unexpectedStop(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
