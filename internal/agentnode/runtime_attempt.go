package agentnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type activeRuntimeAttempt struct {
	identity  AttemptIdentity
	payload   DurableAssignmentPayload
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	renewStop chan struct{}
	renewDone chan struct{}

	canceled atomic.Bool
	finished atomic.Bool

	leaseMu        sync.RWMutex
	leaseExpiresAt time.Time
}

var runtimeEventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func (attempt *activeRuntimeAttempt) setLeaseExpiry(value time.Time) {
	attempt.leaseMu.Lock()
	attempt.leaseExpiresAt = value
	attempt.leaseMu.Unlock()
}

func (attempt *activeRuntimeAttempt) leaseExpiry() time.Time {
	attempt.leaseMu.RLock()
	defer attempt.leaseMu.RUnlock()
	return attempt.leaseExpiresAt
}

func (node *Node) startConfirmedAttempt(record AssignmentJournalRecord, payload DurableAssignmentPayload, leaseExpiry time.Time) error {
	if record.State != AssignmentStateConfirmed || record.Identity != payload.Identity {
		return ErrAssignmentTransition
	}
	node.stateMu.Lock()
	if node.draining {
		node.stateMu.Unlock()
		return nil
	}
	if existing := node.active[record.Identity.AttemptID]; existing != nil {
		node.stateMu.Unlock()
		return nil
	}
	attemptCtx, cancel := runtimeAttemptContext(node.runtimeCtx, payload)
	attempt := &activeRuntimeAttempt{
		identity:       record.Identity,
		payload:        cloneAssignmentPayload(payload),
		ctx:            attemptCtx,
		cancel:         cancel,
		done:           make(chan struct{}),
		renewStop:      make(chan struct{}),
		renewDone:      make(chan struct{}),
		leaseExpiresAt: leaseExpiry,
	}
	node.active[record.Identity.AttemptID] = attempt
	// shutdown sets draining under the same lock before waiting. Adding here
	// guarantees no execution can be added after shutdown begins its Wait.
	node.executions.Add(1)
	node.stateMu.Unlock()

	if _, err := node.store.AdvanceAssignment(record.Identity.AssignmentMessageID, AssignmentStateStarted); err != nil {
		node.removeActiveAttempt(attempt)
		cancel()
		node.executions.Done()
		return err
	}
	go node.executeAttempt(attempt)
	return nil
}

func runtimeAttemptContext(parent context.Context, payload DurableAssignmentPayload) (context.Context, context.CancelFunc) {
	deadline := payload.AttemptDeadlineAt
	if deadline.IsZero() || (!payload.RunDeadlineAt.IsZero() && payload.RunDeadlineAt.Before(deadline)) {
		deadline = payload.RunDeadlineAt
	}
	if deadline.IsZero() {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, deadline)
}

func (node *Node) executeAttempt(attempt *activeRuntimeAttempt) {
	defer node.executions.Done()
	defer close(attempt.done)
	defer node.removeActiveAttempt(attempt)
	go node.renewAttemptLease(attempt)
	defer func() {
		close(attempt.renewStop)
		<-attempt.renewDone
	}()

	input := JSONMap{}
	if err := json.Unmarshal(attempt.payload.Input, &input); err != nil {
		node.persistAttemptFailure(attempt, time.Now(), "ASSIGNMENT_INPUT_INVALID", "assignment input could not be decoded")
		return
	}
	metadata := JSONMap{}
	if string(attempt.payload.Metadata) != "null" {
		if err := json.Unmarshal(attempt.payload.Metadata, &metadata); err != nil {
			node.persistAttemptFailure(attempt, time.Now(), "ASSIGNMENT_METADATA_INVALID", "assignment metadata could not be decoded")
			return
		}
	}
	startedAt := time.Now()
	if errors.Is(attempt.ctx.Err(), context.DeadlineExceeded) {
		node.persistAttemptFailure(attempt, startedAt, "ATTEMPT_DEADLINE_EXCEEDED", "Attempt deadline elapsed before adapter execution")
		return
	}
	runCtx := RunContext{
		RunID:    attempt.identity.RunID,
		AgentID:  attempt.identity.AgentID,
		Input:    input,
		Metadata: metadata,
		Source:   "agent_runtime",
	}
	runCtx.emitChecked = func(eventType string, payload any) error {
		if attempt.finished.Load() || attempt.canceled.Load() {
			return context.Canceled
		}
		if err := node.persistRuntimeEvent(attempt, eventType, payload); err != nil {
			if durableRuntimeErrorIsFatal(err) {
				node.reportFatal(err)
			}
			return err
		}
		node.signalSpool()
		return nil
	}
	runCtx.Emit = func(eventType string, payload any) {
		if err := runCtx.emitChecked(eventType, payload); err != nil {
			node.logf("runtime Event was not persisted: %v", scrubRuntimeError(err))
		}
	}
	runCtx.CallAgent = func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error) {
		return node.callAgentForAttempt(ctx, attempt, targetAgentID, input, options)
	}

	var helperSession *LocalHelperSession
	if node.Helper != nil {
		helperSession = node.Helper.CreateSession(attempt.identity.RunID, &runCtx)
		runCtx.Helper = helperSession.Info
		defer helperSession.Close()
	}

	result := RunResult{Status: "success"}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result.Status = "failed"
				result.Error = &AgentError{Code: "ADAPTER_PANIC", Message: "adapter panicked"}
			}
		}()
		raw, err := node.Adapter.Run(attempt.ctx, input, runCtx)
		if err != nil {
			result.Status = "failed"
			result.Error = normalizeAgentError(err)
			return
		}
		normalized := normalizeAdapterResult(raw)
		result.Status = normalized.Status
		result.Output = normalized.Output
		result.Error = normalized.Error
		result.Events = normalized.Events
	}()

	attempt.finished.Store(true)
	if attempt.canceled.Load() {
		return
	}
	if errors.Is(attempt.ctx.Err(), context.DeadlineExceeded) {
		result = RunResult{
			Status: "failed",
			Error:  &AgentError{Code: "ATTEMPT_DEADLINE_EXCEEDED", Message: "Attempt deadline elapsed during adapter execution"},
		}
	}
	for _, event := range result.Events {
		if err := node.persistRuntimeEvent(attempt, event.EventType, event.Payload); err != nil {
			node.logf("runtime final Event was not persisted: %v", scrubRuntimeError(err))
			if durableRuntimeErrorIsFatal(err) {
				node.reportFatal(err)
				return
			}
			node.persistAttemptFailure(attempt, startedAt, "ADAPTER_EVENT_INVALID", "adapter returned an invalid final Event")
			return
		}
	}
	result.DurationMS = maxDurationMS(startedAt)
	if err := node.persistRunResult(attempt, result); err != nil {
		node.logf("runtime Result was not persisted: %v", scrubRuntimeError(err))
		node.reportFatal(err)
		return
	}
	node.signalSpool()
}

func (node *Node) persistRuntimeEvent(attempt *activeRuntimeAttempt, eventType string, payload any) error {
	if !runtimeEventTypePattern.MatchString(eventType) || eventType == "run.completed" || eventType == "run.failed" || eventType == "run.canceled" || eventType == "run.stream.gap" {
		return errors.New("invalid or Core-owned runtime Event type")
	}
	payloadMap, err := runtimeObject(payload)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payloadMap)
	if err != nil {
		return err
	}
	journal, err := node.store.Assignment(attempt.identity.AssignmentMessageID)
	if err != nil {
		return err
	}
	probe := openlinker.RuntimeV2RunEventPayload{
		AttemptIdentity: sdkAttemptIdentity(attempt.identity),
		ClientEventID:   "00000000-0000-4000-8000-000000000001",
		ClientEventSeq:  journal.LastClientEventSeq + 1,
		EventType:       eventType,
		Payload:         payloadMap,
	}
	if err := enforceRuntimeMessageLimit(probe); err != nil {
		return err
	}
	_, err = node.store.AppendEvent(attempt.identity, eventType, raw)
	return err
}

func (node *Node) persistRunResult(attempt *activeRuntimeAttempt, result RunResult) error {
	journal, err := node.store.Assignment(attempt.identity.AssignmentMessageID)
	if err != nil {
		return err
	}
	resultID, err := newRuntimeUUID()
	if err != nil {
		return err
	}
	payload, err := runtimeResultPayload(attempt.identity, result)
	if err != nil {
		payload = runtimeFailurePayload(
			attempt.identity,
			"RESULT_INVALID",
			"adapter result was not JSON encodable",
			result.DurationMS,
		)
	}
	payload.ResultID = resultID
	payload.FinalClientEventSeq = journal.LastClientEventSeq
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if int64(len(raw)) > openlinker.RuntimeV2MaxMessageBytes {
		payload = runtimeFailurePayload(
			attempt.identity,
			"RESULT_TOO_LARGE",
			"adapter result exceeded the 4 MiB runtime limit",
			result.DurationMS,
		)
		payload.ResultID = resultID
		payload.FinalClientEventSeq = journal.LastClientEventSeq
		raw, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	return node.store.StoreResult(ResultSpoolRecord{
		Identity:            attempt.identity,
		ResultID:            resultID,
		FinalClientEventSeq: journal.LastClientEventSeq,
		Status:              payload.Status,
		Payload:             raw,
	})
}

func (node *Node) persistAttemptFailure(attempt *activeRuntimeAttempt, startedAt time.Time, code, message string) {
	attempt.finished.Store(true)
	if attempt.canceled.Load() {
		return
	}
	if err := node.persistRunResult(attempt, RunResult{
		Status:     "failed",
		DurationMS: maxDurationMS(startedAt),
		Error:      &AgentError{Code: code, Message: message},
	}); err != nil {
		node.reportFatal(err)
		return
	}
	node.signalSpool()
}

func (node *Node) callAgentForAttempt(
	caller context.Context,
	attempt *activeRuntimeAttempt,
	targetAgentID string,
	input any,
	options CallAgentOptions,
) (any, error) {
	inputMap, err := runtimeObject(input)
	if err != nil {
		return nil, err
	}
	metadata, err := runtimeOptionalObject(options.Metadata)
	if err != nil {
		return nil, err
	}
	request := openlinker.RuntimeV2CallAgentRequest{
		TargetAgentID: targetAgentID,
		Input:         inputMap,
		Metadata:      metadata,
		Reason:        options.Reason,
	}
	idempotencyKey := options.IdempotencyKey
	if err := validateDelegatedIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(attempt.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	summary, err := node.RuntimeClient.CallRuntimeV2Agent(callCtx, openlinker.RuntimeV2CallAgentAuthorization{
		NodeEnvelope:         attempt.payload.NodeEnvelope,
		AgentInvocationToken: attempt.payload.AgentInvocationToken,
		IdempotencyKey:       idempotencyKey,
	}, request)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		err := fmt.Errorf("%w: delegated Agent response", ErrRuntimeProtocolMismatch)
		node.reportFatal(err)
		return nil, err
	}
	return JSONMap{
		"run_id":         summary.RunID,
		"status":         summary.Status,
		"dispatch_state": summary.DispatchState,
	}, nil
}

func validateDelegatedIdempotencyKey(key string) error {
	if len(key) == 0 {
		return errors.New("idempotency key is required for delegated Agent calls")
	}
	if len(key) > 255 || key[0] == ' ' || key[len(key)-1] == ' ' {
		return errors.New("idempotency key must contain 1 to 255 printable ASCII bytes without surrounding spaces")
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] > 0x7e {
			return errors.New("idempotency key must contain 1 to 255 printable ASCII bytes without surrounding spaces")
		}
	}
	return nil
}

func (node *Node) removeActiveAttempt(attempt *activeRuntimeAttempt) {
	node.stateMu.Lock()
	if node.active[attempt.identity.AttemptID] == attempt {
		delete(node.active, attempt.identity.AttemptID)
	}
	node.stateMu.Unlock()
}

func (node *Node) cancelAllActive() {
	node.stateMu.RLock()
	active := make([]*activeRuntimeAttempt, 0, len(node.active))
	for _, attempt := range node.active {
		active = append(active, attempt)
	}
	node.stateMu.RUnlock()
	for _, attempt := range active {
		attempt.canceled.Store(true)
		attempt.cancel()
	}
}

func (node *Node) activeAttempt(attemptID string) *activeRuntimeAttempt {
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	return node.active[attemptID]
}

func runtimeResultPayload(identity AttemptIdentity, result RunResult) (openlinker.RuntimeV2RunResultPayload, error) {
	if result.Status != "success" && result.Status != "failed" {
		return runtimeFailurePayload(identity, "RESULT_STATUS_INVALID", "adapter returned an invalid result status", result.DurationMS), nil
	}
	if result.Status == "failed" || result.Error != nil {
		agentErr := result.Error
		if agentErr == nil {
			agentErr = &AgentError{Code: "AGENT_NODE_ERROR", Message: "adapter returned a failed result"}
		}
		return runtimeFailurePayload(identity, agentErr.Code, agentErr.Message, result.DurationMS), nil
	}
	output, err := runtimeObject(result.Output)
	if err != nil {
		return openlinker.RuntimeV2RunResultPayload{}, err
	}
	return openlinker.RuntimeV2RunResultPayload{
		AttemptIdentity: sdkAttemptIdentity(identity),
		Status:          "success",
		Output:          output,
		DurationMS:      result.DurationMS,
	}, nil
}

func runtimeFailurePayload(identity AttemptIdentity, code, message string, durationMS int64) openlinker.RuntimeV2RunResultPayload {
	return openlinker.RuntimeV2RunResultPayload{
		AttemptIdentity: sdkAttemptIdentity(identity),
		Status:          "failed",
		Error: &openlinker.RuntimeV2RunErrorPayload{
			ErrorCode:     boundedRuntimeText(code, 120, "AGENT_NODE_ERROR"),
			Message:       boundedRuntimeText(message, 500, "adapter failed"),
			RetryableHint: false,
		},
		DurationMS: durationMS,
	}
}

func runtimeObject(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, nil
	case JSONMap:
		return map[string]any(typed), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("runtime value is not JSON encodable")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		return object, nil
	}
	return map[string]any{"value": value}, nil
}

func runtimeOptionalObject(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	return runtimeObject(value)
}

func boundedRuntimeText(value string, maximum int, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
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
		copyValue := JSONMap{}
		for key, raw := range value {
			if key != "status" && key != "output" && key != "events" && key != "error" {
				copyValue[key] = raw
			}
		}
		result.Output = copyValue
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
	if errors.Is(err, context.Canceled) {
		return &AgentError{Code: "ADAPTER_CANCELED", Message: "adapter execution was canceled"}
	}
	return &AgentError{Code: "AGENT_NODE_ERROR", Message: boundedRuntimeText(err.Error(), 500, "adapter failed")}
}

func maxDurationMS(startedAt time.Time) int64 {
	milliseconds := time.Since(startedAt).Milliseconds()
	if milliseconds < 1 {
		return 1
	}
	if milliseconds > int64(1<<31-1) {
		return int64(1<<31 - 1)
	}
	return milliseconds
}

func scrubRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var runtimeErr *openlinker.Error
	if errors.As(err, &runtimeErr) {
		return fmt.Errorf("%s (HTTP %d)", runtimeErr.Code, runtimeErr.StatusCode)
	}
	return err
}
