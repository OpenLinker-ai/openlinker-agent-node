package agentnode

import (
	"context"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const (
	testNodeID         = "11111111-1111-4111-8111-111111111111"
	testAgentID        = "22222222-2222-4222-8222-222222222222"
	testCoreInstanceID = "33333333-3333-4333-8333-333333333333"
	testRunID          = "44444444-4444-4444-8444-444444444444"
	testAttemptID      = "55555555-5555-4555-8555-555555555555"
	testLeaseID        = "66666666-6666-4666-8666-666666666666"
	testTargetAgentID  = "77777777-7777-4777-8777-777777777777"
	testCancellationID = "88888888-8888-4888-8888-888888888888"
)

type fakeRuntimeV2Client struct {
	mu sync.Mutex

	hello      openlinker.RuntimeV2HelloPayload
	heartbeats []openlinker.RuntimeV2HelloPayload
	closes     []openlinker.RuntimeV2SessionCloseRequest
	readyOnce  sync.Once
	ready      chan struct{}

	createFn    func(context.Context, openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error)
	heartbeatFn func(context.Context, openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error)
	closeFn     func(context.Context, openlinker.RuntimeV2SessionCloseRequest) error
	claimFn     func(context.Context, int, openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error)
	ackFn       func(context.Context, openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error)
	rejectFn    func(context.Context, openlinker.RuntimeV2AssignmentRejectPayload) (*openlinker.RuntimeV2AssignmentRejectedPayload, error)
	renewFn     func(context.Context, openlinker.RuntimeV2LeaseRenewPayload) (*openlinker.RuntimeV2LeaseRenewedPayload, error)
	eventFn     func(context.Context, openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error)
	resultFn    func(context.Context, openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error)
	resumeFn    func(context.Context, openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error)
	commandsFn  func(context.Context, string, int) (*openlinker.RuntimeV2CommandsResponse, error)
	cancelAckFn func(context.Context, openlinker.RuntimeV2RunCancelAckPayload) (*openlinker.RuntimeV2RunCancellationState, error)
	callAgentFn func(context.Context, openlinker.RuntimeV2CallAgentAuthorization, openlinker.RuntimeV2CallAgentRequest) (*openlinker.RuntimeV2RunSummary, error)
}

func newFakeRuntimeV2Client() *fakeRuntimeV2Client {
	return &fakeRuntimeV2Client{ready: make(chan struct{})}
}

func (client *fakeRuntimeV2Client) readyPayload() *openlinker.RuntimeV2ReadyPayload {
	return &openlinker.RuntimeV2ReadyPayload{
		CoreInstanceID:  testCoreInstanceID,
		Features:        openlinker.RuntimeRequiredFeatures(),
		OfferTTLSeconds: 30,
		LeaseTTLSeconds: 60,
		DatabaseTime:    time.Now().UTC(),
	}
}

func (client *fakeRuntimeV2Client) CreateRuntimeV2Session(ctx context.Context, request openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
	client.mu.Lock()
	client.hello = request
	client.mu.Unlock()
	client.readyOnce.Do(func() { close(client.ready) })
	if client.createFn != nil {
		return client.createFn(ctx, request)
	}
	return client.readyPayload(), nil
}

func (client *fakeRuntimeV2Client) HeartbeatRuntimeV2Session(ctx context.Context, request openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
	client.mu.Lock()
	client.heartbeats = append(client.heartbeats, request)
	client.mu.Unlock()
	if client.heartbeatFn != nil {
		return client.heartbeatFn(ctx, request)
	}
	return client.readyPayload(), nil
}

func (client *fakeRuntimeV2Client) CloseRuntimeV2Session(ctx context.Context, request openlinker.RuntimeV2SessionCloseRequest) error {
	client.mu.Lock()
	client.closes = append(client.closes, request)
	client.mu.Unlock()
	if client.closeFn != nil {
		return client.closeFn(ctx, request)
	}
	return nil
}

func (client *fakeRuntimeV2Client) ClaimRuntimeV2Run(ctx context.Context, wait int, request openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
	if client.claimFn != nil {
		return client.claimFn(ctx, wait, request)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (client *fakeRuntimeV2Client) AckRuntimeV2Assignment(ctx context.Context, request openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error) {
	if client.ackFn != nil {
		return client.ackFn(ctx, request)
	}
	return &openlinker.RuntimeV2AssignmentConfirmedPayload{
		AttemptIdentity: request.AttemptIdentity,
		AttemptNo:       1,
		LeaseExpiresAt:  time.Now().Add(time.Minute).UTC(),
	}, nil
}

func (client *fakeRuntimeV2Client) RejectRuntimeV2Assignment(ctx context.Context, request openlinker.RuntimeV2AssignmentRejectPayload) (*openlinker.RuntimeV2AssignmentRejectedPayload, error) {
	if client.rejectFn != nil {
		return client.rejectFn(ctx, request)
	}
	return &openlinker.RuntimeV2AssignmentRejectedPayload{
		AttemptIdentity: request.AttemptIdentity,
		Outcome:         openlinker.RuntimeV2OfferRejected,
		DispatchState:   openlinker.RuntimeV2DispatchPending,
	}, nil
}

func (client *fakeRuntimeV2Client) RenewRuntimeV2Lease(ctx context.Context, request openlinker.RuntimeV2LeaseRenewPayload) (*openlinker.RuntimeV2LeaseRenewedPayload, error) {
	if client.renewFn != nil {
		return client.renewFn(ctx, request)
	}
	return &openlinker.RuntimeV2LeaseRenewedPayload{
		AttemptIdentity: request.AttemptIdentity,
		LeaseExpiresAt:  time.Now().Add(time.Minute).UTC(),
	}, nil
}

func (client *fakeRuntimeV2Client) AppendRuntimeV2Event(ctx context.Context, request openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error) {
	if client.eventFn != nil {
		return client.eventFn(ctx, request)
	}
	return &openlinker.RuntimeV2RunEventAckPayload{
		ClientEventID:  request.ClientEventID,
		ClientEventSeq: request.ClientEventSeq,
		Sequence:       request.ClientEventSeq,
	}, nil
}

func (client *fakeRuntimeV2Client) FinalizeRuntimeV2Result(ctx context.Context, request openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error) {
	if client.resultFn != nil {
		return client.resultFn(ctx, request)
	}
	return successfulResultACK(request.ResultID), nil
}

func (client *fakeRuntimeV2Client) ResumeRuntimeV2Runs(ctx context.Context, request openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error) {
	if client.resumeFn != nil {
		return client.resumeFn(ctx, request)
	}
	return &openlinker.RuntimeV2ResumeResponse{Decisions: []openlinker.RuntimeV2ResumeAcceptedPayload{}}, nil
}

func (client *fakeRuntimeV2Client) PollRuntimeV2Commands(ctx context.Context, runtimeSessionID string, wait int) (*openlinker.RuntimeV2CommandsResponse, error) {
	if client.commandsFn != nil {
		return client.commandsFn(ctx, runtimeSessionID, wait)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (client *fakeRuntimeV2Client) AckRuntimeV2Cancel(ctx context.Context, request openlinker.RuntimeV2RunCancelAckPayload) (*openlinker.RuntimeV2RunCancellationState, error) {
	if client.cancelAckFn != nil {
		return client.cancelAckFn(ctx, request)
	}
	return &openlinker.RuntimeV2RunCancellationState{
		CancellationID: request.CancellationID,
		CancelState:    request.CancelState,
		UpdatedAt:      time.Now().UTC(),
		ErrorCode:      request.ErrorCode,
	}, nil
}

func (client *fakeRuntimeV2Client) CallRuntimeV2Agent(ctx context.Context, authorization openlinker.RuntimeV2CallAgentAuthorization, request openlinker.RuntimeV2CallAgentRequest) (*openlinker.RuntimeV2RunSummary, error) {
	if client.callAgentFn != nil {
		return client.callAgentFn(ctx, authorization, request)
	}
	return &openlinker.RuntimeV2RunSummary{
		RunID:         "99999999-9999-4999-8999-999999999999",
		Status:        openlinker.RuntimeV2RunRunning,
		DispatchState: openlinker.RuntimeV2DispatchPending,
	}, nil
}

func (client *fakeRuntimeV2Client) helloSnapshot() openlinker.RuntimeV2HelloPayload {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.hello
}

func successfulResultACK(resultID string) *openlinker.RuntimeV2RunResultAckPayload {
	return &openlinker.RuntimeV2RunResultAckPayload{
		ResultID:       resultID,
		Classification: openlinker.RuntimeV2ResultSuccess,
		RunStatus:      openlinker.RuntimeV2RunSuccess,
		DispatchState:  openlinker.RuntimeV2DispatchTerminal,
	}
}

func assignedRunForHello(hello openlinker.RuntimeV2HelloPayload) *openlinker.RuntimeV2RunAssignedPayload {
	return &openlinker.RuntimeV2RunAssignedPayload{
		AttemptIdentity: openlinker.RuntimeV2AttemptIdentity{
			RunID:            testRunID,
			AttemptID:        testAttemptID,
			LeaseID:          testLeaseID,
			FencingToken:     1,
			NodeID:           hello.NodeID,
			AgentID:          hello.AgentID,
			WorkerID:         hello.WorkerID,
			RuntimeSessionID: hello.RuntimeSessionID,
		},
		OfferNo:              1,
		OfferExpiresAt:       time.Now().Add(time.Minute).UTC(),
		AttemptDeadlineAt:    time.Now().Add(2 * time.Minute).UTC(),
		RunDeadlineAt:        time.Now().Add(3 * time.Minute).UTC(),
		Input:                map[string]any{"task": "test reliable runtime"},
		Metadata:             map[string]any{"source": "test"},
		NodeEnvelope:         "ol_ctx_v2.header.payload.signature",
		AgentInvocationToken: "ol_inv_v2.header.payload.signature",
	}
}

func newRuntimeNodeForTest(dataDir string, client RuntimeV2Client, adapter Adapter) *Node {
	return &Node{
		RuntimeURL:        "https://core.example.test",
		NodeID:            testNodeID,
		AgentID:           testAgentID,
		DataDir:           dataDir,
		Capacity:          1,
		ClaimWait:         time.Second,
		CommandWait:       time.Second,
		HeartbeatInterval: time.Hour,
		RetryMinimum:      5 * time.Millisecond,
		RetryMaximum:      20 * time.Millisecond,
		Adapter:           adapter,
		RuntimeClient:     client,
		jitter:            func(value time.Duration) time.Duration { return value },
	}
}
