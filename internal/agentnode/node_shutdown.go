package agentnode

import (
	"context"
	"errors"
	"os"
	"time"
)

// ShutdownTimeoutError reports that a signal-initiated shutdown did not finish
// within ProcessShutdownTimeout, or that a second signal arrived first.
type ShutdownTimeoutError struct {
	Err error
}

func (err *ShutdownTimeoutError) Error() string { return err.Err.Error() }

func (err *ShutdownTimeoutError) Unwrap() error { return err.Err }

// errSecondSignal ends a shutdown that is still running when another signal
// arrives; it takes the same exit path as the timeout.
var errSecondSignal = errors.New("second signal received before shutdown finished")

// Serve runs the Node until it stops on its own or a signal arrives. As
// before, the first signal cancels the run context, which also stops the
// local listeners, and stops the Node through the SDK Worker's Stop; Stop
// runs once, on its own goroutine, bounded by ProcessShutdownTimeout. A nil
// result means the Node stopped cleanly; *ShutdownTimeoutError means the
// budget expired, or a second signal arrived, before it did.
func (node *Node) Serve(signals <-chan os.Signal) error {
	return node.serve(signals, ProcessShutdownTimeout)
}

func (node *Node) serve(signals <-chan os.Signal, budget time.Duration) error {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	stopped := make(chan error, 1)
	go func() {
		stopped <- node.Start(runCtx)
	}()

	select {
	case err := <-stopped:
		return unexpectedStop(err)
	case <-signals:
	}
	cancelRun()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), budget)
	defer cancelShutdown()
	stopResult := make(chan error, 1)
	go func() {
		stopResult <- node.Stop(shutdownCtx)
	}()
	for {
		select {
		case err := <-stopped:
			return unexpectedStop(err)
		case err := <-stopResult:
			stopResult = nil
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && node.Logger != nil {
				node.Logger.Printf("openlinker agent node shutdown: %v", err)
			}
		case <-signals:
			select {
			case err := <-stopped:
				return unexpectedStop(err)
			default:
			}
			return &ShutdownTimeoutError{Err: errSecondSignal}
		case <-shutdownCtx.Done():
			select {
			case err := <-stopped:
				return unexpectedStop(err)
			default:
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
