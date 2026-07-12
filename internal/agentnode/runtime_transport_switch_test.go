package agentnode

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestRuntimeTransportAutoConfirmsBeforeExecuteAndSwitchesWSPullWS(t *testing.T) {
	tracker := &runtimeClaimTracker{}
	ackEntered := make(chan struct{})
	allowConfirmation := make(chan struct{})
	pullResumed := make(chan struct{}, 1)
	secondWSResumed := make(chan struct{}, 1)

	firstWSClient := newFakeRuntimeV2Client()
	configureSwitchClient(firstWSClient, testCoreInstanceID, tracker, nil)
	firstWSClient.ackFn = func(ctx context.Context, request openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error) {
		select {
		case <-ackEntered:
		default:
			close(ackEntered)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-allowConfirmation:
		}
		return confirmedAssignment(request.AttemptIdentity), nil
	}
	firstWS := newFakeRuntimeV2Duplex(firstWSClient)

	pull := newFakeRuntimeV2Client()
	configureSwitchClient(pull, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", tracker, pullResumed)
	secondWSClient := newFakeRuntimeV2Client()
	configureSwitchClient(secondWSClient, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", tracker, secondWSResumed)
	secondWS := newFakeRuntimeV2Duplex(secondWSClient)
	dialer := &fakeRuntimeV2TransportDialer{connections: []RuntimeV2DuplexClient{firstWS, secondWS}}

	adapter := newBlockingSwitchAdapter()
	node := newRuntimeNodeForTest(t.TempDir(), pull, adapter)
	node.Transport = string(RuntimeTransportAuto)
	node.RuntimeDialer = dialer
	node.HeartbeatInterval = time.Hour
	node.RetryMinimum = 5 * time.Millisecond
	node.RetryMaximum = 20 * time.Millisecond

	runDone := make(chan error, 1)
	go func() { runDone <- node.Start(context.Background()) }()

	select {
	case <-ackEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket assignment was not ACKed")
	}
	select {
	case <-adapter.started:
		t.Fatal("adapter started before run.assignment.confirmed")
	case <-time.After(75 * time.Millisecond):
	}
	close(allowConfirmation)
	select {
	case <-adapter.started:
	case <-time.After(3 * time.Second):
		t.Fatal("adapter did not start after assignment confirmation")
	}

	firstWS.disconnect(errors.New("core A disconnected"))
	select {
	case <-pullResumed:
	case <-time.After(3 * time.Second):
		t.Fatal("durable state was not resumed on HTTP pull")
	}
	waitForRuntimeTransport(t, node, RuntimeTransportPullActive)
	if adapter.count.Load() != 1 {
		t.Fatalf("adapter executions after WS to pull = %d", adapter.count.Load())
	}

	dialer.allowProbe.Store(true)
	select {
	case <-secondWSResumed:
	case <-time.After(3 * time.Second):
		t.Fatal("durable state was not resumed on replacement WebSocket")
	}
	waitForRuntimeTransport(t, node, RuntimeTransportWebSocketActive)
	if adapter.count.Load() != 1 {
		t.Fatalf("adapter executions after WS to pull to WS = %d", adapter.count.Load())
	}
	node.stateMu.RLock()
	ready := node.ready
	node.stateMu.RUnlock()
	if ready == nil || ready.CoreInstanceID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("replacement Core ready = %#v", ready)
	}
	if tracker.maximum.Load() > 1 {
		t.Fatalf("concurrent claims across transports = %d", tracker.maximum.Load())
	}

	close(adapter.release)
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := node.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if adapter.count.Load() != 1 {
		t.Fatalf("final adapter executions = %d", adapter.count.Load())
	}
}

func TestRuntimeTransportAutoFallsBackWhenInitialWebSocketIsUnavailable(t *testing.T) {
	pull := newFakeRuntimeV2Client()
	dialer := &fakeRuntimeV2TransportDialer{}
	node := newRuntimeNodeForTest(t.TempDir(), pull, AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		return JSONMap{"unused": true}, nil
	}))
	node.Transport = string(RuntimeTransportAuto)
	node.RuntimeDialer = dialer
	node.RetryMinimum = 5 * time.Millisecond
	node.RetryMaximum = 20 * time.Millisecond
	runDone := make(chan error, 1)
	go func() { runDone <- node.Start(context.Background()) }()
	select {
	case <-pull.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("Pull session was not attached after initial WS failure")
	}
	waitForRuntimeTransport(t, node, RuntimeTransportPullActive)
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := node.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}

func TestSwitchingRuntimeClientCancelsOldGenerationBeforePublishingNew(t *testing.T) {
	oldClient := newFakeRuntimeV2Client()
	entered := make(chan struct{})
	exited := make(chan struct{})
	oldClient.claimFn = func(ctx context.Context, _ int, _ openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}
	gate := newSwitchingRuntimeV2Client(oldClient)
	gate.activate(RuntimeTransportPull, oldClient)
	callDone := make(chan error, 1)
	go func() {
		_, err := gate.ClaimRuntimeV2Run(context.Background(), 25, openlinker.RuntimeV2ClaimRequest{
			RuntimeSessionID: testRunID, Capacity: 1,
		})
		callDone <- err
	}()
	<-entered
	transitionDone := make(chan struct{})
	go func() {
		gate.beginTransition(RuntimeTransportSwitchingWS)
		close(transitionDone)
	}()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("old generation was not canceled")
	}
	select {
	case <-transitionDone:
	case <-time.After(time.Second):
		t.Fatal("transition did not wait for the old call")
	}
	if !errors.Is(<-callDone, context.Canceled) {
		t.Fatal("old call did not return context cancellation")
	}
	if _, err := gate.ClaimRuntimeV2Run(context.Background(), 0, openlinker.RuntimeV2ClaimRequest{}); !errors.Is(err, ErrRuntimeTransportSwitching) {
		t.Fatalf("claim during transition = %v", err)
	}
}

type fakeRuntimeV2Duplex struct {
	*fakeRuntimeV2Client
	done chan struct{}
	once sync.Once
	err  atomic.Value
}

func newFakeRuntimeV2Duplex(client *fakeRuntimeV2Client) *fakeRuntimeV2Duplex {
	return &fakeRuntimeV2Duplex{fakeRuntimeV2Client: client, done: make(chan struct{})}
}

func (client *fakeRuntimeV2Duplex) Done() <-chan struct{} { return client.done }

func (client *fakeRuntimeV2Duplex) Err() error {
	value := client.err.Load()
	if value == nil {
		return nil
	}
	return value.(error)
}

func (client *fakeRuntimeV2Duplex) disconnect(err error) {
	if err != nil {
		client.err.Store(err)
	}
	client.once.Do(func() { close(client.done) })
}

type fakeRuntimeV2TransportDialer struct {
	mu          sync.Mutex
	connections []RuntimeV2DuplexClient
	allowProbe  atomic.Bool
}

func (dialer *fakeRuntimeV2TransportDialer) DialRuntimeV2WebSocket(
	_ context.Context,
	_ openlinker.RuntimeV2HelloPayload,
) (RuntimeV2DuplexClient, error) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	if len(dialer.connections) == 0 {
		return nil, errors.New("no WebSocket connection is available")
	}
	connection := dialer.connections[0]
	dialer.connections = dialer.connections[1:]
	return connection, nil
}

func (dialer *fakeRuntimeV2TransportDialer) ProbeRuntimeV2WebSocket(context.Context) error {
	if !dialer.allowProbe.Load() {
		return errors.New("WebSocket is still unavailable")
	}
	return nil
}

type runtimeClaimTracker struct {
	current atomic.Int64
	maximum atomic.Int64
}

func (tracker *runtimeClaimTracker) enter() func() {
	current := tracker.current.Add(1)
	for {
		maximum := tracker.maximum.Load()
		if current <= maximum || tracker.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	return func() { tracker.current.Add(-1) }
}

func configureSwitchClient(
	client *fakeRuntimeV2Client,
	coreID string,
	tracker *runtimeClaimTracker,
	resumed chan<- struct{},
) {
	client.createFn = func(_ context.Context, request openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
		ready := client.readyPayload()
		ready.CoreInstanceID = coreID
		return ready, nil
	}
	var claimOnce sync.Once
	client.claimFn = func(ctx context.Context, _ int, _ openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
		leave := tracker.enter()
		defer leave()
		var assignment *openlinker.RuntimeV2RunAssignedPayload
		claimOnce.Do(func() {
			hello := client.helloSnapshot()
			assignment = assignedRunForHello(hello)
		})
		if assignment != nil {
			return assignment, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	client.ackFn = func(_ context.Context, request openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error) {
		return confirmedAssignment(request.AttemptIdentity), nil
	}
	client.resumeFn = func(_ context.Context, request openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error) {
		if resumed != nil {
			select {
			case resumed <- struct{}{}:
			default:
			}
		}
		decisions := make([]openlinker.RuntimeV2ResumeAcceptedPayload, len(request.Attempts))
		for index, attempt := range request.Attempts {
			expires := time.Now().Add(time.Minute).UTC()
			decisions[index] = openlinker.RuntimeV2ResumeAcceptedPayload{
				AttemptIdentity: attempt.AttemptIdentity,
				Decision:        openlinker.RuntimeV2ResumeContinue,
				LeaseExpiresAt:  &expires,
				AllowedActions: []openlinker.RuntimeV2ResumeAction{
					openlinker.RuntimeV2ActionContinueExecution,
					openlinker.RuntimeV2ActionUploadEvents,
					openlinker.RuntimeV2ActionUploadResult,
				},
			}
		}
		return &openlinker.RuntimeV2ResumeResponse{Decisions: decisions}, nil
	}
}

func confirmedAssignment(identity openlinker.RuntimeV2AttemptIdentity) *openlinker.RuntimeV2AssignmentConfirmedPayload {
	return &openlinker.RuntimeV2AssignmentConfirmedPayload{
		AttemptIdentity: identity,
		AttemptNo:       1,
		LeaseExpiresAt:  time.Now().Add(time.Minute).UTC(),
	}
}

type blockingSwitchAdapter struct {
	count   atomic.Int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingSwitchAdapter() *blockingSwitchAdapter {
	return &blockingSwitchAdapter{started: make(chan struct{}), release: make(chan struct{})}
}

func (adapter *blockingSwitchAdapter) Run(ctx context.Context, _ any, _ RunContext) (any, error) {
	adapter.count.Add(1)
	adapter.once.Do(func() { close(adapter.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-adapter.release:
		return JSONMap{"ok": true}, nil
	}
}

func waitForRuntimeTransport(t *testing.T, node *Node, expected RuntimeTransportState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, state, _ := node.transport.snapshot()
		if state == expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, state, _ := node.transport.snapshot()
	t.Fatalf("runtime transport state = %s, want %s", state, expected)
}
