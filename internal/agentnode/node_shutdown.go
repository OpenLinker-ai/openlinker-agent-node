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
		options := openlinker.RuntimeWorkerDrainOptions{Timeout: node.effectiveDrainTimeout()}
		var err error
		if node.drainWorker != nil {
			err = node.drainWorker(ctx, worker, options)
		} else {
			err = worker.Drain(ctx, options)
		}
		// An abandoned drain (second signal or budget) is not a drain failure.
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) && node.Logger != nil {
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

	// The shutdown sequence can block in SDK store I/O (for example a stalled
	// fsync under a store mutex), so it runs on its own goroutine; this one
	// enforces the process budget and reacts to a second signal regardless.
	forced := make(chan struct{})
	go func() {
		select {
		case <-signals:
			if node.Logger != nil {
				node.Logger.Printf("openlinker agent node received a second signal; stopping without waiting for the drain")
			}
			close(forced)
			cancelDrain()
			cancelStart()
			// Request the stop directly too: the drain may be blocked and never
			// reach the Stop that follows it inside Shutdown.
			go func() { _ = node.Stop(shutdownCtx) }()
		case <-drainCtx.Done():
		}
	}()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		if err := node.Shutdown(drainCtx); err != nil && !errors.Is(err, context.Canceled) && node.Logger != nil {
			node.Logger.Printf("openlinker agent node shutdown: %v", err)
		}
		// Covers a signal that arrived before Start marked the Node as started.
		cancelStart()
	}()

	// A graceful shutdown ends when Start has returned and Shutdown has finished
	// (it waits for in-flight public A2A requests). After a second signal only
	// Start has to return. The budget bounds both, as before.
	var (
		startErr     error
		startDone    bool
		shutdownOver bool
		forcedStop   bool
	)
	stoppedChannel, shutdownChannel, forcedChannel := stopped, shutdownDone, forced
	for {
		if startDone && (shutdownOver || forcedStop) {
			return unexpectedStop(startErr)
		}
		select {
		case startErr = <-stoppedChannel:
			startDone, stoppedChannel = true, nil
		case <-shutdownChannel:
			shutdownOver, shutdownChannel = true, nil
		case <-forcedChannel:
			forcedStop, forcedChannel = true, nil
		case <-shutdownCtx.Done():
			// Prefer a completion that raced with the deadline.
			select {
			case startErr = <-stoppedChannel:
				startDone = true
			default:
			}
			select {
			case <-shutdownChannel:
				shutdownOver = true
			default:
			}
			if startDone && (shutdownOver || forcedStop) {
				return unexpectedStop(startErr)
			}
			return &ShutdownTimeoutError{Err: shutdownCtx.Err()}
		}
	}
}

func unexpectedStop(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
