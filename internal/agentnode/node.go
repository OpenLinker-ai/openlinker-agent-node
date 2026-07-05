package agentnode

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type Node struct {
	APIBase    string
	AgentToken string
	Connector  Connector
	Adapter    Adapter
	Helper     *LocalHelperServer
	PublicA2A  *PublicA2AServer
	Logger     *log.Logger

	ctx        context.Context
	cancel     context.CancelFunc
	queue      chan Assignment
	workerDone chan struct{}
	stopOnce   sync.Once
}

func (n *Node) Start(parent context.Context) error {
	if n.APIBase == "" {
		return fmt.Errorf("api base is required")
	}
	if n.AgentToken == "" {
		return fmt.Errorf("agent token is required")
	}
	if n.Connector == nil {
		return fmt.Errorf("connector is required")
	}
	if n.Adapter == nil {
		return fmt.Errorf("adapter is required")
	}
	if parent == nil {
		parent = context.Background()
	}
	n.ctx, n.cancel = context.WithCancel(parent)
	n.queue = make(chan Assignment, 16)
	n.workerDone = make(chan struct{})

	if n.Helper != nil {
		if err := n.Helper.Start(n.ctx); err != nil {
			return err
		}
	}
	if n.PublicA2A != nil {
		if n.PublicA2A.Adapter == nil {
			n.PublicA2A.Adapter = n.Adapter
		}
		if err := n.PublicA2A.Start(n.ctx); err != nil {
			return err
		}
	}

	go n.worker()
	return n.Connector.Start(n.ctx, ConnectorHandlers{
		OnReady: func(message JSONMap) {
			n.logf("agent node ready: %v", message["agent_id"])
		},
		OnAssigned: func(assignment Assignment) {
			select {
			case n.queue <- assignment:
			case <-n.ctx.Done():
			}
		},
		OnError: func(err error) {
			n.logf("agent node connector error: %v", err)
		},
	})
}

func (n *Node) Stop(ctx context.Context) error {
	var err error
	n.stopOnce.Do(func() {
		if n.cancel != nil {
			n.cancel()
		}
		if n.Connector != nil {
			err = n.Connector.Stop(ctx)
		}
		if n.workerDone != nil {
			select {
			case <-n.workerDone:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		if n.Helper != nil {
			if helperErr := n.Helper.Stop(ctx); helperErr != nil && err == nil {
				err = helperErr
			}
		}
		if n.PublicA2A != nil {
			if publicErr := n.PublicA2A.Stop(ctx); publicErr != nil && err == nil {
				err = publicErr
			}
		}
	})
	return err
}

func (n *Node) worker() {
	defer close(n.workerDone)
	for {
		select {
		case <-n.ctx.Done():
			return
		case assignment := <-n.queue:
			n.processAssignment(assignment)
		}
	}
}

func (n *Node) processAssignment(assignment Assignment) {
	startedAt := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			result := RunResult{
				Status:     "failed",
				DurationMS: maxDurationMS(startedAt),
				Error: &AgentError{
					Code:    "ADAPTER_PANIC",
					Message: fmt.Sprintf("%v", recovered),
				},
			}
			if n.Connector != nil {
				if err := n.Connector.CompleteRun(n.ctx, assignment.RunID, result); err != nil {
					n.logf("agent node panic result failed: %v", err)
				}
			}
		}
	}()
	var mu sync.Mutex
	bufferedEvents := make([]RunEvent, 0)
	a2aClient := AgentA2AClient{
		APIBase:    n.APIBase,
		AgentToken: n.AgentToken,
	}
	runCtx := RunContext{
		RunID:        assignment.RunID,
		AgentID:      assignment.AgentID,
		Input:        assignment.Input,
		Metadata:     normalizeMetadata(assignment.Metadata),
		Source:       assignment.Source,
		A2A:          normalizeA2A(assignment.A2A),
		Conversation: assignment.Conversation,
	}
	runCtx.Emit = func(eventType string, payload any) {
		event := RunEvent{EventType: eventType, Payload: payload}
		mu.Lock()
		bufferedEvents = append(bufferedEvents, event)
		mu.Unlock()
		if n.Connector.SupportsLiveEvents() {
			if err := n.Connector.SendRunEvent(n.ctx, assignment.RunID, event); err != nil {
				n.logf("agent node run.event failed: %v", err)
			}
		}
	}
	runCtx.CallAgent = func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error) {
		currentRunID := options.CurrentRunID
		if currentRunID == "" {
			currentRunID = stringFromMap(runCtx.A2A, "current_run_id")
		}
		if currentRunID == "" {
			currentRunID = assignment.RunID
		}
		if options.Endpoint == "" {
			options.Endpoint = stringFromMap(runCtx.A2A, "call_agent_endpoint")
		}
		if options.ContextID == "" {
			options.ContextID = stringFromMap(runCtx.A2A, "protocol_context_id")
		}
		if options.TraceID == "" {
			options.TraceID = stringFromMap(runCtx.A2A, "trace_id")
		}
		if len(options.ReferenceTaskIDs) == 0 {
			options.ReferenceTaskIDs = stringSliceFromMap(runCtx.A2A, "reference_task_ids")
		}
		return a2aClient.CallAgent(ctx, currentRunID, targetAgentID, input, options)
	}

	var helperSession *LocalHelperSession
	if n.Helper != nil {
		helperSession = n.Helper.CreateSession(assignment.RunID, &runCtx)
		runCtx.Helper = helperSession.Info
	}
	if helperSession != nil {
		defer helperSession.Close()
	}

	result := RunResult{Status: "success", DurationMS: maxDurationMS(startedAt)}
	raw, err := n.Adapter.Run(n.ctx, assignment.Input, runCtx)
	if err != nil {
		result.Status = "failed"
		result.Error = normalizeAgentError(err)
	} else {
		normalized := normalizeAdapterResult(raw)
		result.Status = normalized.Status
		result.Output = normalized.Output
		result.Error = normalized.Error
		if n.Connector.SupportsLiveEvents() {
			result.Events = normalized.Events
		} else {
			mu.Lock()
			result.Events = append(append([]RunEvent{}, bufferedEvents...), normalized.Events...)
			mu.Unlock()
		}
	}
	result.DurationMS = maxDurationMS(startedAt)
	if err := n.Connector.CompleteRun(n.ctx, assignment.RunID, result); err != nil {
		n.logf("agent node run.result failed: %v", err)
	}
}

func normalizeAdapterResult(raw any) AdapterResult {
	switch typed := raw.(type) {
	case AdapterResult:
		return fillAdapterDefaults(typed)
	case *AdapterResult:
		if typed == nil {
			return AdapterResult{Status: "success", Output: JSONMap{}}
		}
		return fillAdapterDefaults(*typed)
	case map[string]any:
		if _, hasStatus := typed["status"]; hasStatus {
			return adapterResultFromMap(typed)
		}
		if _, hasOutput := typed["output"]; hasOutput {
			return adapterResultFromMap(typed)
		}
		if _, hasEvents := typed["events"]; hasEvents {
			return adapterResultFromMap(typed)
		}
	}
	return AdapterResult{Status: "success", Output: raw}
}

func adapterResultFromMap(value map[string]any) AdapterResult {
	result := AdapterResult{
		Status: fmt.Sprint(value["status"]),
		Output: value["output"],
		Events: eventsFromAny(value["events"]),
	}
	if result.Status == "" || result.Status == "<nil>" {
		result.Status = "success"
	}
	if result.Output == nil {
		copy := JSONMap{}
		for key, raw := range value {
			if key != "status" && key != "output" && key != "events" && key != "error" {
				copy[key] = raw
			}
		}
		result.Output = copy
	}
	return result
}

func fillAdapterDefaults(result AdapterResult) AdapterResult {
	if result.Status == "" {
		result.Status = "success"
	}
	if result.Output == nil && result.Error == nil {
		result.Output = JSONMap{}
	}
	return result
}

func eventsFromAny(value any) []RunEvent {
	switch typed := value.(type) {
	case []RunEvent:
		return typed
	case []any:
		events := make([]RunEvent, 0, len(typed))
		for _, item := range typed {
			if itemMap, ok := item.(map[string]any); ok {
				events = append(events, RunEvent{
					EventType: fmt.Sprint(itemMap["event_type"]),
					Payload:   itemMap["payload"],
				})
			}
		}
		return events
	default:
		return nil
	}
}

func normalizeAgentError(err error) *AgentError {
	if err == nil {
		return nil
	}
	return &AgentError{Code: "AGENT_NODE_ERROR", Message: err.Error()}
}

func maxDurationMS(startedAt time.Time) int64 {
	ms := time.Since(startedAt).Milliseconds()
	if ms < 1 {
		return 1
	}
	return ms
}

func (n *Node) logf(format string, args ...any) {
	if n.Logger != nil {
		n.Logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
