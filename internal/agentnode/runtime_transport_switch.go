package agentnode

import (
	"context"
	"errors"
	"sync"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type RuntimeTransportMode string

const (
	RuntimeTransportAuto      RuntimeTransportMode = "auto"
	RuntimeTransportWebSocket RuntimeTransportMode = "ws"
	RuntimeTransportPull      RuntimeTransportMode = "pull"
)

type RuntimeTransportState string

const (
	RuntimeTransportDisconnected    RuntimeTransportState = "disconnected"
	RuntimeTransportConnectingWS    RuntimeTransportState = "connecting_ws"
	RuntimeTransportWebSocketActive RuntimeTransportState = "ws_active"
	RuntimeTransportSwitchingPull   RuntimeTransportState = "switching_to_pull"
	RuntimeTransportPullActive      RuntimeTransportState = "pull_active"
	RuntimeTransportProbingWS       RuntimeTransportState = "probing_ws"
	RuntimeTransportSwitchingWS     RuntimeTransportState = "switching_to_ws"
	RuntimeTransportStopped         RuntimeTransportState = "stopped"
)

var ErrRuntimeTransportSwitching = errors.New("runtime transport is switching")

type RuntimeV2DuplexClient interface {
	RuntimeV2Client
	Done() <-chan struct{}
	Err() error
}

type RuntimeV2TransportDialer interface {
	DialRuntimeV2WebSocket(context.Context, openlinker.RuntimeV2HelloPayload) (RuntimeV2DuplexClient, error)
	ProbeRuntimeV2WebSocket(context.Context) error
}

type sdkRuntimeV2TransportDialer struct {
	runtime *openlinker.Runtime
}

func (dialer sdkRuntimeV2TransportDialer) DialRuntimeV2WebSocket(
	ctx context.Context,
	hello openlinker.RuntimeV2HelloPayload,
) (RuntimeV2DuplexClient, error) {
	return dialer.runtime.DialRuntimeV2WebSocket(ctx, hello)
}

func (dialer sdkRuntimeV2TransportDialer) ProbeRuntimeV2WebSocket(ctx context.Context) error {
	return dialer.runtime.ProbeRuntimeV2WebSocket(ctx)
}

// switchingRuntimeV2Client is the transport gate shared by every runtime
// loop. A transition first removes the active client and cancels its generation
// context, then waits for every in-flight operation to exit. A new client is
// published only after attach and durable resume have succeeded.
type switchingRuntimeV2Client struct {
	mu         sync.Mutex
	cond       *sync.Cond
	active     RuntimeV2Client
	kind       RuntimeTransportMode
	state      RuntimeTransportState
	generation context.Context
	cancel     context.CancelFunc
	operations int
	callClient RuntimeV2Client
}

func newSwitchingRuntimeV2Client(callClient RuntimeV2Client) *switchingRuntimeV2Client {
	client := &switchingRuntimeV2Client{
		state:      RuntimeTransportDisconnected,
		callClient: callClient,
	}
	client.cond = sync.NewCond(&client.mu)
	return client
}

func (client *switchingRuntimeV2Client) setState(state RuntimeTransportState) {
	client.mu.Lock()
	client.state = state
	client.mu.Unlock()
}

func (client *switchingRuntimeV2Client) snapshot() (RuntimeTransportMode, RuntimeTransportState, RuntimeV2Client) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.kind, client.state, client.active
}

func (client *switchingRuntimeV2Client) activate(kind RuntimeTransportMode, active RuntimeV2Client) {
	client.mu.Lock()
	if client.cancel != nil {
		client.cancel()
	}
	client.generation, client.cancel = context.WithCancel(context.Background())
	client.active = active
	client.kind = kind
	if kind == RuntimeTransportWebSocket {
		client.state = RuntimeTransportWebSocketActive
	} else {
		client.state = RuntimeTransportPullActive
	}
	client.mu.Unlock()
}

func (client *switchingRuntimeV2Client) beginTransition(state RuntimeTransportState) (RuntimeTransportMode, RuntimeV2Client) {
	client.mu.Lock()
	client.state = state
	kind, active := client.kind, client.active
	client.active = nil
	client.kind = ""
	if client.cancel != nil {
		client.cancel()
	}
	for client.operations > 0 {
		client.cond.Wait()
	}
	client.mu.Unlock()
	return kind, active
}

func (client *switchingRuntimeV2Client) stop() (RuntimeTransportMode, RuntimeV2Client) {
	return client.beginTransition(RuntimeTransportStopped)
}

func (client *switchingRuntimeV2Client) begin(
	parent context.Context,
) (RuntimeV2Client, context.Context, func(), error) {
	if parent == nil {
		parent = context.Background()
	}
	client.mu.Lock()
	active := client.active
	generation := client.generation
	if active == nil || generation == nil {
		client.mu.Unlock()
		return nil, nil, nil, ErrRuntimeTransportSwitching
	}
	client.operations++
	client.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	stopGeneration := context.AfterFunc(generation, cancel)
	done := func() {
		stopGeneration()
		cancel()
		client.mu.Lock()
		client.operations--
		if client.operations == 0 {
			client.cond.Broadcast()
		}
		client.mu.Unlock()
	}
	return active, ctx, done, nil
}

func (client *switchingRuntimeV2Client) CreateRuntimeV2Session(ctx context.Context, request openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.CreateRuntimeV2Session(callCtx, request)
}

func (client *switchingRuntimeV2Client) HeartbeatRuntimeV2Session(ctx context.Context, request openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.HeartbeatRuntimeV2Session(callCtx, request)
}

func (client *switchingRuntimeV2Client) CloseRuntimeV2Session(ctx context.Context, request openlinker.RuntimeV2SessionCloseRequest) error {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return active.CloseRuntimeV2Session(callCtx, request)
}

func (client *switchingRuntimeV2Client) ClaimRuntimeV2Run(ctx context.Context, wait int, request openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.ClaimRuntimeV2Run(callCtx, wait, request)
}

func (client *switchingRuntimeV2Client) AckRuntimeV2Assignment(ctx context.Context, request openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.AckRuntimeV2Assignment(callCtx, request)
}

func (client *switchingRuntimeV2Client) RejectRuntimeV2Assignment(ctx context.Context, request openlinker.RuntimeV2AssignmentRejectPayload) (*openlinker.RuntimeV2AssignmentRejectedPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.RejectRuntimeV2Assignment(callCtx, request)
}

func (client *switchingRuntimeV2Client) RenewRuntimeV2Lease(ctx context.Context, request openlinker.RuntimeV2LeaseRenewPayload) (*openlinker.RuntimeV2LeaseRenewedPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.RenewRuntimeV2Lease(callCtx, request)
}

func (client *switchingRuntimeV2Client) AppendRuntimeV2Event(ctx context.Context, request openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.AppendRuntimeV2Event(callCtx, request)
}

func (client *switchingRuntimeV2Client) FinalizeRuntimeV2Result(ctx context.Context, request openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.FinalizeRuntimeV2Result(callCtx, request)
}

func (client *switchingRuntimeV2Client) ResumeRuntimeV2Runs(ctx context.Context, request openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.ResumeRuntimeV2Runs(callCtx, request)
}

func (client *switchingRuntimeV2Client) PollRuntimeV2Commands(ctx context.Context, sessionID string, wait int) (*openlinker.RuntimeV2CommandsResponse, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.PollRuntimeV2Commands(callCtx, sessionID, wait)
}

func (client *switchingRuntimeV2Client) AckRuntimeV2Cancel(ctx context.Context, request openlinker.RuntimeV2RunCancelAckPayload) (*openlinker.RuntimeV2RunCancellationState, error) {
	active, callCtx, done, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return active.AckRuntimeV2Cancel(callCtx, request)
}

func (client *switchingRuntimeV2Client) CallRuntimeV2Agent(ctx context.Context, authorization openlinker.RuntimeV2CallAgentAuthorization, request openlinker.RuntimeV2CallAgentRequest) (*openlinker.RuntimeV2RunSummary, error) {
	if client.callClient == nil {
		return nil, ErrRuntimeTransportSwitching
	}
	return client.callClient.CallRuntimeV2Agent(ctx, authorization, request)
}

var _ RuntimeV2Client = (*switchingRuntimeV2Client)(nil)
