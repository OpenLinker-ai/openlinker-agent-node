package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestRuntimeV2ReliableFlowReplaysStableEventAndResult(t *testing.T) {
	dataDir := t.TempDir()
	client := newFakeRuntimeV2Client()
	var claimCalls atomic.Int32
	client.claimFn = func(ctx context.Context, _ int, _ openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
		if claimCalls.Add(1) == 1 {
			return assignedRunForHello(client.helloSnapshot()), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	var eventMu sync.Mutex
	var eventRequests []openlinker.RuntimeV2RunEventPayload
	client.eventFn = func(_ context.Context, request openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error) {
		eventMu.Lock()
		eventRequests = append(eventRequests, request)
		attempt := len(eventRequests)
		eventMu.Unlock()
		if attempt == 1 {
			return nil, errors.New("simulated Event ACK loss")
		}
		return &openlinker.RuntimeV2RunEventAckPayload{
			ClientEventID:  request.ClientEventID,
			ClientEventSeq: request.ClientEventSeq,
			Sequence:       71,
			Replayed:       true,
		}, nil
	}

	resultDone := make(chan struct{})
	var resultOnce sync.Once
	var resultMu sync.Mutex
	var resultRequests []openlinker.RuntimeV2RunResultPayload
	client.resultFn = func(_ context.Context, request openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error) {
		resultMu.Lock()
		resultRequests = append(resultRequests, request)
		attempt := len(resultRequests)
		resultMu.Unlock()
		if attempt == 1 {
			return nil, errors.New("simulated Result ACK loss")
		}
		resultOnce.Do(func() { close(resultDone) })
		ack := successfulResultACK(request.ResultID)
		ack.Replayed = true
		return ack, nil
	}

	var delegatedMu sync.Mutex
	var delegatedAuth []openlinker.RuntimeV2CallAgentAuthorization
	client.callAgentFn = func(_ context.Context, authorization openlinker.RuntimeV2CallAgentAuthorization, request openlinker.RuntimeV2CallAgentRequest) (*openlinker.RuntimeV2RunSummary, error) {
		delegatedMu.Lock()
		delegatedAuth = append(delegatedAuth, authorization)
		delegatedMu.Unlock()
		if request.TargetAgentID != testTargetAgentID || request.Input["question"] != "status" {
			return nil, errors.New("delegated request mismatch")
		}
		return &openlinker.RuntimeV2RunSummary{
			RunID:         "99999999-9999-4999-8999-999999999999",
			Status:        openlinker.RuntimeV2RunRunning,
			DispatchState: openlinker.RuntimeV2DispatchPending,
		}, nil
	}

	var adapterCalls atomic.Int32
	adapter := AdapterFunc(func(ctx context.Context, _ any, runCtx RunContext) (any, error) {
		adapterCalls.Add(1)
		if _, err := runCtx.CallAgent(ctx, testTargetAgentID, JSONMap{"question": "status"}, CallAgentOptions{
			IdempotencyKey: "child-intent-status-1",
			Reason:         "collect status",
		}); err != nil {
			return nil, err
		}
		runCtx.Emit("run.message.delta", JSONMap{"text": "working"})
		return JSONMap{"answer": 42}, nil
	})
	node := newRuntimeNodeForTest(dataDir, client, adapter)
	errCh := startRuntimeNodeForTest(node)

	waitForTestSignal(t, resultDone, 7*time.Second, "typed Result ACK")
	stopRuntimeNodeForTest(t, node, errCh)

	if adapterCalls.Load() != 1 {
		t.Fatalf("adapter calls = %d, want exactly 1", adapterCalls.Load())
	}
	eventMu.Lock()
	if len(eventRequests) != 2 || eventRequests[0].ClientEventID != eventRequests[1].ClientEventID || eventRequests[0].ClientEventSeq != eventRequests[1].ClientEventSeq {
		t.Fatalf("Event replay did not preserve identity: %#v", eventRequests)
	}
	stableEventID := eventRequests[0].ClientEventID
	eventMu.Unlock()
	if stableEventID == "" {
		t.Fatal("Event replay used an empty client_event_id")
	}
	resultMu.Lock()
	if len(resultRequests) != 2 || resultRequests[0].ResultID != resultRequests[1].ResultID || resultRequests[0].FinalClientEventSeq != 1 || resultRequests[1].FinalClientEventSeq != 1 {
		t.Fatalf("Result replay did not preserve identity/final sequence: %#v", resultRequests)
	}
	resultMu.Unlock()
	delegatedMu.Lock()
	if len(delegatedAuth) != 1 || delegatedAuth[0].NodeEnvelope != "ol_ctx_v2.header.payload.signature" ||
		delegatedAuth[0].AgentInvocationToken != "ol_inv_v2.header.payload.signature" || delegatedAuth[0].IdempotencyKey != "child-intent-status-1" {
		t.Fatalf("delegated authorization = %#v", delegatedAuth)
	}
	delegatedMu.Unlock()

	client.mu.Lock()
	if len(client.closes) != 1 || client.closes[0].RuntimeSessionID != client.hello.RuntimeSessionID {
		t.Fatalf("session closes = %#v hello=%#v", client.closes, client.hello)
	}
	foundDrainingHeartbeat := false
	for _, heartbeat := range client.heartbeats {
		foundDrainingHeartbeat = foundDrainingHeartbeat || heartbeat.Capacity == 0
	}
	client.mu.Unlock()
	if !foundDrainingHeartbeat {
		t.Fatal("graceful shutdown did not advertise capacity=0")
	}

	store := openRuntimeStoreForTest(t, dataDir)
	records, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("fully acknowledged Attempt remained durable: %#v", records)
	}
}

func TestRuntimeV2ResumeAfterAssignmentACKResponseLossExecutesOnce(t *testing.T) {
	dataDir := t.TempDir()
	firstClient := newFakeRuntimeV2Client()
	var firstClaim atomic.Bool
	firstClient.claimFn = func(ctx context.Context, _ int, _ openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
		if firstClaim.CompareAndSwap(false, true) {
			return assignedRunForHello(firstClient.helloSnapshot()), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ackEntered := make(chan struct{})
	var ackOnce sync.Once
	firstClient.ackFn = func(ctx context.Context, _ openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error) {
		ackOnce.Do(func() { close(ackEntered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var firstAdapterCalls atomic.Int32
	firstNode := newRuntimeNodeForTest(dataDir, firstClient, AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		firstAdapterCalls.Add(1)
		return JSONMap{"unsafe": true}, nil
	}))
	firstErrCh := startRuntimeNodeForTest(firstNode)
	waitForTestSignal(t, ackEntered, 3*time.Second, "assignment ACK request")
	stopRuntimeNodeForTest(t, firstNode, firstErrCh)
	if firstAdapterCalls.Load() != 0 {
		t.Fatalf("adapter ran before assignment confirmation: %d", firstAdapterCalls.Load())
	}
	firstHello := firstClient.helloSnapshot()

	secondClient := newFakeRuntimeV2Client()
	var resumeRequest openlinker.RuntimeV2ResumePayload
	secondClient.resumeFn = func(_ context.Context, request openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error) {
		resumeRequest = request
		decisions := make([]openlinker.RuntimeV2ResumeAcceptedPayload, len(request.Attempts))
		for index, attempt := range request.Attempts {
			leaseExpiry := time.Now().Add(time.Minute).UTC()
			decisions[index] = openlinker.RuntimeV2ResumeAcceptedPayload{
				AttemptIdentity: attempt.AttemptIdentity,
				Decision:        openlinker.RuntimeV2ResumeContinue,
				LeaseExpiresAt:  &leaseExpiry,
				AllowedActions: []openlinker.RuntimeV2ResumeAction{
					openlinker.RuntimeV2ActionContinueExecution,
					openlinker.RuntimeV2ActionUploadEvents,
					openlinker.RuntimeV2ActionUploadResult,
				},
			}
		}
		return &openlinker.RuntimeV2ResumeResponse{Decisions: decisions}, nil
	}
	resultDone := make(chan struct{})
	var resultOnce sync.Once
	secondClient.resultFn = func(_ context.Context, request openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error) {
		resultOnce.Do(func() { close(resultDone) })
		return successfulResultACK(request.ResultID), nil
	}
	var secondAdapterCalls atomic.Int32
	secondNode := newRuntimeNodeForTest(dataDir, secondClient, AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		secondAdapterCalls.Add(1)
		return JSONMap{"resumed": true}, nil
	}))
	secondErrCh := startRuntimeNodeForTest(secondNode)
	waitForTestSignal(t, resultDone, 4*time.Second, "resumed Result")
	stopRuntimeNodeForTest(t, secondNode, secondErrCh)

	if secondAdapterCalls.Load() != 1 {
		t.Fatalf("resumed adapter calls = %d, want 1", secondAdapterCalls.Load())
	}
	secondHello := secondClient.helloSnapshot()
	if len(resumeRequest.Attempts) != 1 || resumeRequest.Attempts[0].AttemptIdentity.RuntimeSessionID != firstHello.RuntimeSessionID {
		t.Fatalf("resume did not carry the durable Attempt identity: %#v", resumeRequest)
	}
	if resumeRequest.RuntimeSessionID != secondHello.RuntimeSessionID || resumeRequest.RuntimeSessionID == firstHello.RuntimeSessionID {
		t.Fatalf("resume session rotation mismatch: first=%s second=%s request=%s", firstHello.RuntimeSessionID, secondHello.RuntimeSessionID, resumeRequest.RuntimeSessionID)
	}
}

func TestRuntimeV2ReplacementSessionUploadsDurableSpoolWithoutRerun(t *testing.T) {
	dataDir := t.TempDir()
	store := openRuntimeStoreForTest(t, dataDir)
	identity := runtimeV2TestAttemptIdentity(store.Identity())
	if err := store.CreateAssignment(testAssignmentRecord(identity)); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreAssignmentPayload(runtimeV2TestAssignmentPayload(identity)); err != nil {
		t.Fatal(err)
	}
	for _, state := range []AssignmentState{AssignmentStateACKSent, AssignmentStateConfirmed, AssignmentStateStarted} {
		if _, err := store.AdvanceAssignment(identity.AssignmentMessageID, state); err != nil {
			t.Fatal(err)
		}
	}
	event, err := store.AppendEvent(identity, "run.message.delta", json.RawMessage(`{"text":"durable"}`))
	if err != nil {
		t.Fatal(err)
	}
	resultID, err := newRuntimeUUID()
	if err != nil {
		t.Fatal(err)
	}
	resultPayload := openlinker.RuntimeV2RunResultPayload{
		AttemptIdentity:     sdkAttemptIdentity(identity),
		ResultID:            resultID,
		Status:              "success",
		Output:              map[string]any{"answer": 42},
		DurationMS:          10,
		FinalClientEventSeq: event.ClientEventSeq,
	}
	resultRaw, err := json.Marshal(resultPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(ResultSpoolRecord{
		Identity:            identity,
		ResultID:            resultID,
		FinalClientEventSeq: event.ClientEventSeq,
		Status:              "success",
		Payload:             resultRaw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	client := newFakeRuntimeV2Client()
	client.resumeFn = func(_ context.Context, request openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error) {
		if len(request.Attempts) != 1 || request.Attempts[0].PendingResultID != resultID || len(request.Attempts[0].PendingClientEventRanges) != 1 {
			return nil, errors.New("resume inventory mismatch")
		}
		return &openlinker.RuntimeV2ResumeResponse{Decisions: []openlinker.RuntimeV2ResumeAcceptedPayload{{
			AttemptIdentity: request.Attempts[0].AttemptIdentity,
			Decision:        openlinker.RuntimeV2ResumeUploadSpool,
			AllowedActions: []openlinker.RuntimeV2ResumeAction{
				openlinker.RuntimeV2ActionUploadEvents,
				openlinker.RuntimeV2ActionUploadResult,
			},
		}}}, nil
	}
	client.eventFn = func(_ context.Context, request openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error) {
		if request.ClientEventID != event.ClientEventID || request.ClientEventSeq != event.ClientEventSeq {
			return nil, errors.New("durable Event identity changed")
		}
		return &openlinker.RuntimeV2RunEventAckPayload{
			ClientEventID: request.ClientEventID, ClientEventSeq: request.ClientEventSeq, Sequence: 1,
		}, nil
	}
	resultDone := make(chan struct{})
	var resultOnce sync.Once
	client.resultFn = func(_ context.Context, request openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error) {
		if request.ResultID != resultID || request.FinalClientEventSeq != event.ClientEventSeq {
			return nil, errors.New("durable Result identity changed")
		}
		resultOnce.Do(func() { close(resultDone) })
		return successfulResultACK(request.ResultID), nil
	}
	var adapterCalls atomic.Int32
	node := newRuntimeNodeForTest(dataDir, client, AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		adapterCalls.Add(1)
		return nil, errors.New("durable spool must not rerun adapter")
	}))
	errCh := startRuntimeNodeForTest(node)
	waitForTestSignal(t, resultDone, 3*time.Second, "replacement-session durable Result")
	stopRuntimeNodeForTest(t, node, errCh)
	if adapterCalls.Load() != 0 {
		t.Fatalf("replacement session reran adapter %d time(s)", adapterCalls.Load())
	}
}

func TestRuntimeV2RefusesToRerunStartedAttemptAfterProcessRestart(t *testing.T) {
	dataDir := t.TempDir()
	store := openRuntimeStoreForTest(t, dataDir)
	identity := runtimeV2TestAttemptIdentity(store.Identity())
	if err := store.CreateAssignment(testAssignmentRecord(identity)); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreAssignmentPayload(runtimeV2TestAssignmentPayload(identity)); err != nil {
		t.Fatal(err)
	}
	for _, state := range []AssignmentState{AssignmentStateACKSent, AssignmentStateConfirmed, AssignmentStateStarted} {
		if _, err := store.AdvanceAssignment(identity.AssignmentMessageID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	client := newFakeRuntimeV2Client()
	client.resumeFn = func(_ context.Context, request openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error) {
		leaseExpiry := time.Now().Add(time.Minute).UTC()
		return &openlinker.RuntimeV2ResumeResponse{Decisions: []openlinker.RuntimeV2ResumeAcceptedPayload{{
			AttemptIdentity: request.Attempts[0].AttemptIdentity,
			Decision:        openlinker.RuntimeV2ResumeContinue,
			LeaseExpiresAt:  &leaseExpiry,
			AllowedActions: []openlinker.RuntimeV2ResumeAction{
				openlinker.RuntimeV2ActionContinueExecution,
				openlinker.RuntimeV2ActionUploadEvents,
				openlinker.RuntimeV2ActionUploadResult,
			},
		}}}, nil
	}
	var adapterCalls atomic.Int32
	node := newRuntimeNodeForTest(dataDir, client, AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		adapterCalls.Add(1)
		return nil, nil
	}))
	err := node.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsafe resume refused") {
		t.Fatalf("Start error = %v, want unsafe resume refusal", err)
	}
	if adapterCalls.Load() != 0 {
		t.Fatalf("started Attempt was rerun %d time(s)", adapterCalls.Load())
	}
}

func TestRuntimeV2CancelACKsStoppedOnlyAfterAdapterExited(t *testing.T) {
	dataDir := t.TempDir()
	client := newFakeRuntimeV2Client()
	var claimed atomic.Bool
	client.claimFn = func(ctx context.Context, _ int, _ openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
		if claimed.CompareAndSwap(false, true) {
			return assignedRunForHello(client.helloSnapshot()), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	adapterStarted := make(chan struct{})
	adapterExited := make(chan struct{})
	adapter := AdapterFunc(func(ctx context.Context, _ any, _ RunContext) (any, error) {
		close(adapterStarted)
		<-ctx.Done()
		time.Sleep(40 * time.Millisecond)
		close(adapterExited)
		return nil, ctx.Err()
	})
	var commandDelivered atomic.Bool
	var polledSession string
	client.commandsFn = func(ctx context.Context, runtimeSessionID string, _ int) (*openlinker.RuntimeV2CommandsResponse, error) {
		polledSession = runtimeSessionID
		if commandDelivered.CompareAndSwap(false, true) {
			select {
			case <-adapterStarted:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			assigned := assignedRunForHello(client.helloSnapshot())
			payload, err := json.Marshal(openlinker.RuntimeV2RunCancelPayload{
				CancellationID:  testCancellationID,
				AttemptIdentity: assigned.AttemptIdentity,
				ReasonCode:      "USER_REQUESTED",
				DeadlineAt:      time.Now().Add(3 * time.Second).UTC(),
			})
			if err != nil {
				return nil, err
			}
			return &openlinker.RuntimeV2CommandsResponse{
				Commands:     []openlinker.RuntimeV2PendingCommand{{Type: openlinker.RuntimeV2RunCancel, Payload: payload}},
				DatabaseTime: time.Now().UTC(),
			}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	stoppedACK := make(chan struct{})
	var stoppedOnce sync.Once
	var cancelMu sync.Mutex
	var cancelStates []openlinker.RuntimeV2CancelState
	var stoppedBeforeExit bool
	client.cancelAckFn = func(_ context.Context, request openlinker.RuntimeV2RunCancelAckPayload) (*openlinker.RuntimeV2RunCancellationState, error) {
		cancelMu.Lock()
		cancelStates = append(cancelStates, request.CancelState)
		if request.CancelState == openlinker.RuntimeV2CancelStopped {
			select {
			case <-adapterExited:
			default:
				stoppedBeforeExit = true
			}
			stoppedOnce.Do(func() { close(stoppedACK) })
		}
		cancelMu.Unlock()
		return &openlinker.RuntimeV2RunCancellationState{
			CancellationID: request.CancellationID,
			CancelState:    request.CancelState,
			UpdatedAt:      time.Now().UTC(),
		}, nil
	}

	node := newRuntimeNodeForTest(dataDir, client, adapter)
	errCh := startRuntimeNodeForTest(node)
	waitForTestSignal(t, stoppedACK, 4*time.Second, "cancel stopped ACK")
	eventuallyForTest(t, 2*time.Second, func() bool {
		record, err := node.store.Assignment(deterministicRuntimeUUID("assignment", testAttemptID, testLeaseID))
		return err == nil && record.State == AssignmentStateRevoked
	}, "canceled Attempt journal to become revoked")
	stopRuntimeNodeForTest(t, node, errCh)

	cancelMu.Lock()
	if stoppedBeforeExit || len(cancelStates) < 2 || cancelStates[0] != openlinker.RuntimeV2CancelStopping || cancelStates[len(cancelStates)-1] != openlinker.RuntimeV2CancelStopped {
		t.Fatalf("cancel ACK order = %#v stoppedBeforeExit=%v", cancelStates, stoppedBeforeExit)
	}
	cancelMu.Unlock()
	if polledSession != client.helloSnapshot().RuntimeSessionID {
		t.Fatalf("command poll session = %q, want %q", polledSession, client.helloSnapshot().RuntimeSessionID)
	}
}

func TestRuntimeV2StaleLeaseCancelsExactAttempt(t *testing.T) {
	dataDir := t.TempDir()
	client := newFakeRuntimeV2Client()
	client.createFn = func(_ context.Context, _ openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
		ready := client.readyPayload()
		ready.LeaseTTLSeconds = 1
		return ready, nil
	}
	var claimed atomic.Bool
	client.claimFn = func(ctx context.Context, _ int, _ openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
		if claimed.CompareAndSwap(false, true) {
			return assignedRunForHello(client.helloSnapshot()), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	renewed := make(chan struct{})
	var renewOnce sync.Once
	client.renewFn = func(_ context.Context, request openlinker.RuntimeV2LeaseRenewPayload) (*openlinker.RuntimeV2LeaseRenewedPayload, error) {
		renewOnce.Do(func() { close(renewed) })
		if request.AttemptIdentity.AttemptID != testAttemptID {
			return nil, errors.New("wrong Attempt renewed")
		}
		return nil, &openlinker.Error{StatusCode: 409, Code: "STALE_LEASE", Message: "stale lease"}
	}
	adapterExited := make(chan struct{})
	adapter := AdapterFunc(func(ctx context.Context, _ any, _ RunContext) (any, error) {
		<-ctx.Done()
		close(adapterExited)
		return nil, ctx.Err()
	})
	node := newRuntimeNodeForTest(dataDir, client, adapter)
	errCh := startRuntimeNodeForTest(node)
	waitForTestSignal(t, renewed, 2*time.Second, "lease renewal")
	waitForTestSignal(t, adapterExited, 2*time.Second, "stale-lease cancellation")
	eventuallyForTest(t, 2*time.Second, func() bool {
		record, err := node.store.Assignment(deterministicRuntimeUUID("assignment", testAttemptID, testLeaseID))
		return err == nil && record.State == AssignmentStateRevoked
	}, "stale-lease Attempt journal to become revoked")
	stopRuntimeNodeForTest(t, node, errCh)
}

func TestDelegatedAgentCallRequiresExplicitIntentKey(t *testing.T) {
	client := newFakeRuntimeV2Client()
	var mu sync.Mutex
	var keys []string
	client.callAgentFn = func(_ context.Context, authorization openlinker.RuntimeV2CallAgentAuthorization, _ openlinker.RuntimeV2CallAgentRequest) (*openlinker.RuntimeV2RunSummary, error) {
		mu.Lock()
		keys = append(keys, authorization.IdempotencyKey)
		mu.Unlock()
		return &openlinker.RuntimeV2RunSummary{
			RunID:         "99999999-9999-4999-8999-999999999999",
			Status:        openlinker.RuntimeV2RunRunning,
			DispatchState: openlinker.RuntimeV2DispatchPending,
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempt := &activeRuntimeAttempt{
		identity: runtimeV2TestAttemptIdentity(RuntimeIdentity{
			WorkerID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			RuntimeSessionID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			SessionEpoch:     1,
		}),
		payload: DurableAssignmentPayload{
			NodeEnvelope:         "ol_ctx_v2.header.payload.signature",
			AgentInvocationToken: "ol_inv_v2.header.payload.signature",
		},
		ctx: ctx,
	}
	node := &Node{RuntimeClient: client}
	body := JSONMap{"same": "body"}
	if _, err := node.callAgentForAttempt(ctx, attempt, testTargetAgentID, body, CallAgentOptions{}); err == nil || !strings.Contains(err.Error(), "idempotency key is required") {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := node.callAgentForAttempt(ctx, attempt, testTargetAgentID, body, CallAgentOptions{IdempotencyKey: " normalized "}); err == nil || !strings.Contains(err.Error(), "without surrounding spaces") {
		t.Fatalf("normalized key error = %v", err)
	}
	for _, key := range []string{"intent-a", "intent-b", "intent-a"} {
		if _, err := node.callAgentForAttempt(ctx, attempt, testTargetAgentID, body, CallAgentOptions{IdempotencyKey: key}); err != nil {
			t.Fatalf("delegated call with key %q: %v", key, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(keys, ",") != "intent-a,intent-b,intent-a" {
		t.Fatalf("forwarded intent keys = %#v", keys)
	}
}

func TestRuntimeV2TypedACKMismatchNeverClearsSpool(t *testing.T) {
	store := openRuntimeStoreForTest(t, t.TempDir())
	identity := persistStartedAssignmentForTest(t, store, "typed-ack")
	event, err := store.AppendEvent(identity, "run.message.delta", json.RawMessage(`{"text":"ack me"}`))
	if err != nil {
		t.Fatal(err)
	}
	resultID, err := newRuntimeUUID()
	if err != nil {
		t.Fatal(err)
	}
	resultPayload := openlinker.RuntimeV2RunResultPayload{
		AttemptIdentity: sdkAttemptIdentity(identity), ResultID: resultID, Status: "success",
		Output: map[string]any{"ok": true}, DurationMS: 1, FinalClientEventSeq: 1,
	}
	resultRaw, err := json.Marshal(resultPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StoreResult(ResultSpoolRecord{
		Identity: identity, ResultID: resultID, FinalClientEventSeq: 1, Status: "success", Payload: resultRaw,
	}); err != nil {
		t.Fatal(err)
	}
	client := newFakeRuntimeV2Client()
	client.eventFn = func(_ context.Context, request openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error) {
		return &openlinker.RuntimeV2RunEventAckPayload{
			ClientEventID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", ClientEventSeq: request.ClientEventSeq, Sequence: 1,
		}, nil
	}
	node := &Node{store: store, RuntimeClient: client, runtimeCtx: context.Background()}
	record, err := store.Assignment(identity.AssignmentMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.flushAttemptSpool(record, spoolPermission{events: true, result: true}); !errors.Is(err, ErrRuntimeProtocolMismatch) {
		t.Fatalf("Event ACK mismatch error = %v", err)
	}
	pending, err := store.PendingEvents(identity.AttemptID)
	if err != nil || len(pending) != 1 || pending[0].ClientEventID != event.ClientEventID {
		t.Fatalf("Event cleared by mismatched ACK: %#v err=%v", pending, err)
	}

	client.eventFn = nil
	client.resultFn = func(_ context.Context, request openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error) {
		return successfulResultACK("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"), nil
	}
	if err := node.flushAttemptSpool(record, spoolPermission{events: true, result: true}); !errors.Is(err, ErrRuntimeProtocolMismatch) {
		t.Fatalf("Result ACK mismatch error = %v", err)
	}
	if _, err := store.PendingResult(identity.AttemptID); err != nil {
		t.Fatalf("Result cleared by mismatched ACK: %v", err)
	}
}

func TestRuntimeV2StopCancelsBlockedSessionStartup(t *testing.T) {
	client := newFakeRuntimeV2Client()
	client.createFn = func(ctx context.Context, _ openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	node := newRuntimeNodeForTest(t.TempDir(), client, AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		return JSONMap{}, nil
	}))
	errCh := startRuntimeNodeForTest(node)
	waitForTestSignal(t, client.ready, 2*time.Second, "blocked session startup")
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := node.Stop(stopCtx); err != nil {
		t.Fatalf("Stop during startup: %v", err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start error after startup cancellation = %v", err)
		}
	case <-stopCtx.Done():
		t.Fatalf("Start remained blocked after Stop: %v", stopCtx.Err())
	}
}

func TestRuntimeV2MessageBoundaryAndOversizedResultFallback(t *testing.T) {
	request := openlinker.RuntimeV2RunEventPayload{
		AttemptIdentity: openlinker.RuntimeV2AttemptIdentity{
			RunID: testRunID, AttemptID: testAttemptID, LeaseID: testLeaseID, FencingToken: 1,
			NodeID: testNodeID, AgentID: testAgentID,
			WorkerID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", RuntimeSessionID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		},
		ClientEventID:  "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		ClientEventSeq: 1,
		EventType:      "run.message.delta",
		Payload:        map[string]any{"blob": ""},
	}
	base, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	filler := int(openlinker.RuntimeV2MaxMessageBytes) - len(base)
	request.Payload["blob"] = strings.Repeat("a", filler)
	if encoded, _ := json.Marshal(request); int64(len(encoded)) != openlinker.RuntimeV2MaxMessageBytes {
		t.Fatalf("boundary fixture size = %d", len(encoded))
	}
	if err := enforceRuntimeMessageLimit(request); err != nil {
		t.Fatalf("exact 4 MiB message rejected: %v", err)
	}
	request.Payload["blob"] = strings.Repeat("a", filler+1)
	if err := enforceRuntimeMessageLimit(request); !errors.Is(err, ErrRuntimeMessageTooLarge) {
		t.Fatalf("oversized message error = %v", err)
	}

	store := openRuntimeStoreForTest(t, t.TempDir())
	identity := persistStartedAssignmentForTest(t, store, "oversized-result")
	node := &Node{store: store}
	attempt := &activeRuntimeAttempt{identity: identity}
	if err := node.persistRunResult(attempt, RunResult{
		Status: "success",
		Output: JSONMap{"blob": strings.Repeat("z", int(openlinker.RuntimeV2MaxMessageBytes))},
	}); err != nil {
		t.Fatal(err)
	}
	spooled, err := store.PendingResult(identity.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var fallback openlinker.RuntimeV2RunResultPayload
	if err := decodeStrictJSON(spooled.Payload, &fallback); err != nil {
		t.Fatal(err)
	}
	if fallback.Status != "failed" || fallback.Error == nil || fallback.Error.ErrorCode != "RESULT_TOO_LARGE" {
		t.Fatalf("oversized result fallback = %#v", fallback)
	}
}

func TestRuntimeV2ExpiredAttemptDeadlineDoesNotInvokeAdapter(t *testing.T) {
	store := openRuntimeStoreForTest(t, t.TempDir())
	identity := runtimeV2TestAttemptIdentity(store.Identity())
	if err := store.CreateAssignment(testAssignmentRecord(identity)); err != nil {
		t.Fatal(err)
	}
	payload := runtimeV2TestAssignmentPayload(identity)
	payload.AttemptDeadlineAt = time.Now().Add(-time.Second).UTC()
	payload.RunDeadlineAt = time.Now().Add(time.Minute).UTC()
	if err := store.StoreAssignmentPayload(payload); err != nil {
		t.Fatal(err)
	}
	for _, state := range []AssignmentState{AssignmentStateACKSent, AssignmentStateConfirmed} {
		if _, err := store.AdvanceAssignment(identity.AssignmentMessageID, state); err != nil {
			t.Fatal(err)
		}
	}
	record, err := store.Assignment(identity.AssignmentMessageID)
	if err != nil {
		t.Fatal(err)
	}
	var adapterCalls atomic.Int32
	node := &Node{
		Adapter: AdapterFunc(func(context.Context, any, RunContext) (any, error) {
			adapterCalls.Add(1)
			return JSONMap{}, nil
		}),
		RuntimeClient: newFakeRuntimeV2Client(),
		store:         store,
		runtimeCtx:    context.Background(),
		active:        make(map[string]*activeRuntimeAttempt),
		wakeSpool:     make(chan struct{}, 1),
	}
	if err := node.startConfirmedAttempt(record, payload, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		node.executions.Wait()
		close(done)
	}()
	waitForTestSignal(t, done, 2*time.Second, "expired Attempt failure Result")
	if adapterCalls.Load() != 0 {
		t.Fatalf("expired Attempt invoked adapter %d time(s)", adapterCalls.Load())
	}
	result, err := store.PendingResult(identity.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var payloadResult openlinker.RuntimeV2RunResultPayload
	if err := decodeStrictJSON(result.Payload, &payloadResult); err != nil {
		t.Fatal(err)
	}
	if payloadResult.Error == nil || payloadResult.Error.ErrorCode != "ATTEMPT_DEADLINE_EXCEEDED" {
		t.Fatalf("expired Attempt Result = %#v", payloadResult)
	}
}

func TestAssignmentPayloadIsEncryptedDurableAndFailsClosedOnCorruption(t *testing.T) {
	dataDir := t.TempDir()
	store := openRuntimeStoreForTest(t, dataDir)
	identity := testAttemptIdentity(store.Identity(), "assignment-payload")
	if err := store.CreateAssignment(testAssignmentRecord(identity)); err != nil {
		t.Fatal(err)
	}
	payload := runtimeV2TestAssignmentPayload(identity)
	payload.Input = json.RawMessage(`{"private_marker":"never-plaintext"}`)
	payload.AgentInvocationToken = "ol_inv_v2.private.token.signature"
	if err := store.StoreAssignmentPayload(payload); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreAssignmentPayload(payload); err != nil {
		t.Fatalf("idempotent payload replay: %v", err)
	}
	conflict := payload
	conflict.AgentInvocationToken = "ol_inv_v2.different.token.signature"
	if err := store.StoreAssignmentPayload(conflict); !errors.Is(err, ErrSpoolRecordConflict) {
		t.Fatalf("conflicting payload error = %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(dataDir, assignmentSpoolDirectory, "*"+spoolRecordExtension))
	if err != nil || len(paths) != 1 {
		t.Fatalf("assignment spool paths = %#v err=%v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("never-plaintext")) || bytes.Contains(raw, []byte("ol_inv_v2.private.token.signature")) {
		t.Fatal("encrypted assignment spool exposed input or invocation token")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openRuntimeStoreForTest(t, dataDir)
	replayed, err := store.AssignmentPayload(identity.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed.Input, payload.Input) || replayed.AgentInvocationToken != payload.AgentInvocationToken || replayed.CreatedAt.IsZero() {
		t.Fatalf("replayed assignment payload = %#v", replayed)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(paths[0], raw, 0o600); err != nil {
		t.Fatal(err)
	}
	corruptStore, err := OpenRuntimeDurableStore(dataDir)
	if corruptStore != nil {
		_ = corruptStore.Close()
	}
	if !errors.Is(err, ErrRuntimeRecordCorrupt) {
		t.Fatalf("corrupt assignment payload open error = %v", err)
	}
}

func runtimeV2TestAttemptIdentity(identity RuntimeIdentity) AttemptIdentity {
	return AttemptIdentity{
		NodeID:              testNodeID,
		AgentID:             testAgentID,
		WorkerID:            identity.WorkerID,
		RuntimeSessionID:    identity.RuntimeSessionID,
		SessionEpoch:        identity.SessionEpoch,
		AssignmentMessageID: deterministicRuntimeUUID("assignment", testAttemptID, testLeaseID),
		RunID:               testRunID,
		AttemptID:           testAttemptID,
		OfferID:             deterministicRuntimeUUID("offer", testAttemptID, testLeaseID),
		LeaseID:             testLeaseID,
		FencingToken:        1,
	}
}

func runtimeV2TestAssignmentPayload(identity AttemptIdentity) DurableAssignmentPayload {
	return DurableAssignmentPayload{
		Identity:             identity,
		Input:                json.RawMessage(`{"task":"resume"}`),
		Metadata:             json.RawMessage(`{"source":"test"}`),
		NodeEnvelope:         "ol_ctx_v2.header.payload.signature",
		AgentInvocationToken: "ol_inv_v2.header.payload.signature",
		OfferExpiresAt:       time.Now().Add(time.Minute).UTC(),
		AttemptDeadlineAt:    time.Now().Add(2 * time.Minute).UTC(),
		RunDeadlineAt:        time.Now().Add(3 * time.Minute).UTC(),
	}
}

func startRuntimeNodeForTest(node *Node) <-chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- node.Start(context.Background()) }()
	return errCh
}

func stopRuntimeNodeForTest(t *testing.T, node *Node, errCh <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := node.Stop(ctx); err != nil {
		t.Fatalf("stop runtime node: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runtime node returned: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("runtime node did not return: %v", ctx.Err())
	}
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func eventuallyForTest(t *testing.T, timeout time.Duration, condition func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}
