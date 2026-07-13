package agentnode

import (
	"context"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestStopCancelsLifetimeBeforeWorkerStart(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	node := &Node{
		started:  true,
		done:     make(chan struct{}),
		worker:   &openlinker.RuntimeWorker{},
		lifetime: lifetime,
		cancel:   cancel,
	}
	go func() {
		<-lifetime.Done()
		node.mu.Lock()
		if node.started {
			node.started = false
			close(node.done)
		}
		node.mu.Unlock()
	}()

	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := node.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if lifetime.Err() == nil {
		t.Fatal("Stop did not cancel the pending Worker Start lifetime")
	}
}
