package agentnode

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const AgentNodeVersion = "openlinker-agent-node/0.1.43"

// Node is the process-level Adapter shell around the Go SDK RuntimeWorker.
// Runtime transport, session, recovery, journal, spool, lease, and cancellation
// semantics live exclusively in openlinker-go.
type Node struct {
	OpenLinkerURL string
	RuntimeURL    string
	Transport     string
	NodeID        string
	AgentID       string
	AgentToken    string
	DataDir       string

	MTLSCertFile   string
	MTLSKeyFile    string
	MTLSCAFile     string
	MTLSServerName string

	Capacity          int64
	ClaimWait         time.Duration
	CommandWait       time.Duration
	HeartbeatInterval time.Duration
	RetryMinimum      time.Duration
	RetryMaximum      time.Duration

	Adapter   Adapter
	Helper    *LocalHelperServer
	PublicA2A *PublicA2AServer
	Logger    *log.Logger

	mu       sync.Mutex
	started  bool
	done     chan struct{}
	worker   *openlinker.RuntimeWorker
	lifetime context.Context
	cancel   context.CancelFunc
}

func (node *Node) Start(parent context.Context) (retErr error) {
	if parent == nil {
		parent = context.Background()
	}
	if node.Adapter == nil {
		return errors.New("adapter is required")
	}

	node.mu.Lock()
	if node.started {
		node.mu.Unlock()
		return errors.New("agent node is already started")
	}
	node.started = true
	node.done = make(chan struct{})
	node.lifetime, node.cancel = context.WithCancel(parent)
	node.mu.Unlock()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
		defer cancel()
		if err := node.stopAdapters(shutdownCtx); retErr == nil && err != nil {
			retErr = err
		}
		node.mu.Lock()
		if node.started {
			node.started = false
			close(node.done)
		}
		node.worker = nil
		node.cancel = nil
		node.mu.Unlock()
	}()

	worker, err := openlinker.NewRuntimeWorker(openlinker.RuntimeWorkerConfig{
		PlatformURL: node.OpenLinkerURL,
		RuntimeURL:  node.RuntimeURL,
		Transport:   openlinker.RuntimeTransportMode(node.Transport),
		NodeID:      node.NodeID,
		NodeVersion: AgentNodeVersion,
		AgentID:     node.AgentID,
		AgentToken:  node.AgentToken,
		DataDir:     node.DataDir,
		MTLS: openlinker.RuntimeMTLSConfig{
			CertFile:   node.MTLSCertFile,
			KeyFile:    node.MTLSKeyFile,
			CAFile:     node.MTLSCAFile,
			ServerName: node.MTLSServerName,
		},
		Capacity:          node.Capacity,
		ClaimWait:         node.ClaimWait,
		CommandWait:       node.CommandWait,
		HeartbeatInterval: node.HeartbeatInterval,
		RetryMinimum:      node.RetryMinimum,
		RetryMaximum:      node.RetryMaximum,
		Handler:           runtimeAdapterHandler{node: node},
		Logger:            node.Logger,
	})
	if err != nil {
		return err
	}

	if node.Helper != nil {
		if err := node.Helper.Start(node.lifetime); err != nil {
			return err
		}
	}
	if node.PublicA2A != nil {
		node.PublicA2A.Slug = strings.TrimSpace(node.PublicA2A.Slug)
		if node.PublicA2A.Slug == "" {
			node.PublicA2A.Slug = "agent-node"
		}
		proxy, err := node.newPublicA2AProxy(node.lifetime)
		if err != nil {
			return fmt.Errorf("create public A2A Core proxy: %w", err)
		}
		if err := node.PublicA2A.setProxy(proxy); err != nil {
			proxy.Close()
			return err
		}
		if err := node.PublicA2A.Start(node.lifetime); err != nil {
			return err
		}
	}

	node.mu.Lock()
	node.worker = worker
	node.mu.Unlock()
	return worker.Start(node.lifetime)
}

func (node *Node) newPublicA2AProxy(ctx context.Context) (publicA2AProxy, error) {
	if node.PublicA2A == nil {
		return nil, errors.New("public A2A server is not configured")
	}
	return openlinker.NewRuntimeA2AProxy(ctx, openlinker.RuntimeA2AProxyConfig{
		PlatformURL: node.OpenLinkerURL,
		RuntimeURL:  node.RuntimeURL,
		AgentToken:  node.AgentToken,
		AgentSlug:   node.PublicA2A.Slug,
		DataDir:     node.DataDir,
		NodeID:      node.NodeID,
		AgentID:     node.AgentID,
		NodeVersion: AgentNodeVersion,
		Capacity:    node.Capacity,
		MTLS: openlinker.RuntimeMTLSConfig{
			CertFile:   node.MTLSCertFile,
			KeyFile:    node.MTLSKeyFile,
			CAFile:     node.MTLSCAFile,
			ServerName: node.MTLSServerName,
		},
	})
}

func (node *Node) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	node.mu.Lock()
	if !node.started {
		node.mu.Unlock()
		return nil
	}
	worker := node.worker
	done := node.done
	cancel := node.cancel
	node.mu.Unlock()

	// Cancel the shell lifetime even when the SDK Worker has been constructed
	// but has not yet entered Start. Worker.Stop is intentionally idempotent in
	// that window and cannot replace cancellation of the pending Start call.
	if cancel != nil {
		cancel()
	}
	if worker != nil {
		if err := worker.Stop(ctx); err != nil {
			return err
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (node *Node) stopAdapters(ctx context.Context) error {
	if node.cancel != nil {
		node.cancel()
	}
	var firstErr error
	if node.Helper != nil {
		firstErr = node.Helper.Stop(ctx)
	}
	if node.PublicA2A != nil {
		if err := node.PublicA2A.Stop(ctx); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type runtimeAdapterHandler struct {
	node *Node
}

func (handler runtimeAdapterHandler) Handle(
	ctx context.Context,
	assignment openlinker.RuntimeContext,
) (result openlinker.RuntimeResult, resultErr error) {
	defer func() {
		if recover() != nil {
			result = openlinker.RuntimeResult{
				Status: "failed",
				Error:  &openlinker.RuntimeHandlerError{Code: "ADAPTER_PANIC", Message: "adapter panicked"},
			}
			resultErr = nil
		}
	}()
	runCtx := RunContext{
		RunID:    assignment.RunID,
		AgentID:  assignment.AgentID,
		Input:    assignment.Input,
		Metadata: JSONMap(assignment.Metadata),
		Source:   "agent_runtime",
	}
	runCtx.emitChecked = assignment.Emit
	runCtx.Emit = func(eventType string, payload any) {
		if err := assignment.Emit(eventType, payload); err != nil && handler.node.Logger != nil {
			handler.node.Logger.Printf("runtime Event was not persisted: %v", err)
		}
	}
	runCtx.CallAgent = func(
		callCtx context.Context,
		targetAgentID string,
		input any,
		options CallAgentOptions,
	) (any, error) {
		return assignment.CallAgent(callCtx, targetAgentID, input, openlinker.RuntimeCallOptions{
			IdempotencyKey: options.IdempotencyKey,
			Reason:         options.Reason,
			Metadata:       options.Metadata,
		})
	}

	var helperSession *LocalHelperSession
	if handler.node.Helper != nil {
		helperSession = handler.node.Helper.CreateSession(assignment.RunID, &runCtx)
		runCtx.Helper = helperSession.Info
		defer helperSession.Close()
	}

	raw, err := handler.node.Adapter.Run(ctx, assignment.Input, runCtx)
	if err != nil {
		adapterErr := normalizeAgentError(err)
		return openlinker.RuntimeResult{
			Status: "failed",
			Error:  &openlinker.RuntimeHandlerError{Code: adapterErr.Code, Message: adapterErr.Message},
		}, nil
	}
	adapterResult := normalizeAdapterResult(raw)
	events := make([]openlinker.RuntimeEvent, len(adapterResult.Events))
	for index, event := range adapterResult.Events {
		events[index] = openlinker.RuntimeEvent{EventType: event.EventType, Payload: event.Payload}
	}
	var runtimeErr *openlinker.RuntimeHandlerError
	if adapterResult.Error != nil {
		runtimeErr = &openlinker.RuntimeHandlerError{Code: adapterResult.Error.Code, Message: adapterResult.Error.Message}
	}
	return openlinker.RuntimeResult{
		Status: adapterResult.Status,
		Output: adapterResult.Output,
		Events: events,
		Error:  runtimeErr,
	}, nil
}
