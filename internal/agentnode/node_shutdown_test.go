package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// The fake Core below speaks the token-only HTTP pull Runtime protocol through
// the exported SDK payload types. It exists only in tests: Agent Node itself
// never implements Runtime wire paths.
const (
	drainTestAgentID      = "22222222-2222-4222-8222-222222222222"
	drainTestCoreID       = "33333333-3333-4333-8333-333333333333"
	drainTestAttachmentID = "99999999-9999-4999-8999-999999999998"
	drainTestRuntimePath  = "/api/v1/agent-runtime"
)

type drainTestRun struct {
	RunID, AttemptID, LeaseID string
}

var (
	drainTestRunA = drainTestRun{
		RunID:     "44444444-4444-4444-8444-444444444441",
		AttemptID: "55555555-5555-4555-8555-555555555551",
		LeaseID:   "66666666-6666-4666-8666-666666666661",
	}
	drainTestRunB = drainTestRun{
		RunID:     "44444444-4444-4444-8444-444444444442",
		AttemptID: "55555555-5555-4555-8555-555555555552",
		LeaseID:   "66666666-6666-4666-8666-666666666662",
	}
)

type drainTestCore struct {
	server *httptest.Server

	mu              sync.Mutex
	hello           openlinker.RuntimeHelloPayload
	queue           []drainTestRun
	claimed         []string
	inflight        int64
	stickyInflight  bool
	failSessions    bool
	sessionAttempts int
	drains          []openlinker.RuntimeDrainPayload
	results         []openlinker.RuntimeRunResultPayload
	closes          []openlinker.RuntimeSessionCloseRequest
	unexpected      []string
	// ingress records public A2A requests the SDK proxy forwarded to Core;
	// a request whose body contains "slow" waits for releaseSlow.
	ingress     []string
	releaseSlow chan struct{}
	releaseOnce sync.Once

	drained     chan struct{}
	drainedOnce sync.Once
}

func newDrainTestCore(t *testing.T, runs ...drainTestRun) *drainTestCore {
	t.Helper()
	core := &drainTestCore{
		queue: append([]drainTestRun(nil), runs...), drained: make(chan struct{}), releaseSlow: make(chan struct{}),
	}
	core.server = httptest.NewServer(core)
	t.Cleanup(core.server.Close)
	// Cleanups run last-in first-out: blocked handlers are released before the
	// server Close waits for them, even when a test fails early.
	t.Cleanup(core.releaseSlowRequests)
	return core
}

func (core *drainTestCore) ready() openlinker.RuntimeReadyPayload {
	return openlinker.RuntimeReadyPayload{
		CoreInstanceID: drainTestCoreID, AttachmentID: drainTestAttachmentID,
		Features: openlinker.RuntimeRequiredFeatures(), OfferTTLSeconds: 30, LeaseTTLSeconds: 60,
		DatabaseTime: time.Now().UTC(),
	}
}

func (core *drainTestCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	runtimePath := strings.TrimPrefix(path, drainTestRuntimePath)
	post := r.Method == http.MethodPost
	switch {
	case post && strings.HasPrefix(runtimePath, "/a2a-proxy/agents/"):
		core.serveA2AIngress(w, r)
	case r.Method == http.MethodGet && path == "/.well-known/openlinker.json":
		writeJSON(w, http.StatusOK, map[string]any{
			"base_urls": map[string]any{"runtime": "http://" + r.Host},
			"runtime": map[string]any{
				"enabled": true, "mtls_required": false,
				"transports": []string{"pull"}, "default_transport": "pull",
			},
		})
	case post && path == drainTestRuntimePath+"/sessions":
		var hello openlinker.RuntimeHelloPayload
		if !core.decode(w, r, &hello) {
			return
		}
		core.mu.Lock()
		core.sessionAttempts++
		fail := core.failSessions
		if !fail {
			core.hello = hello
		}
		core.mu.Unlock()
		if fail {
			http.Error(w, "Core is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, core.ready())
	case post && strings.HasPrefix(runtimePath, "/sessions/") && strings.HasSuffix(path, "/heartbeat"):
		writeJSON(w, http.StatusOK, core.ready())
	case post && strings.HasPrefix(runtimePath, "/sessions/") && strings.HasSuffix(path, "/drain"):
		var request openlinker.RuntimeDrainPayload
		if !core.decode(w, r, &request) {
			return
		}
		core.mu.Lock()
		core.drains = append(core.drains, request)
		inflight := core.inflight
		if core.stickyInflight && inflight < 1 {
			inflight = 1
		}
		core.mu.Unlock()
		core.drainedOnce.Do(func() { close(core.drained) })
		writeJSON(w, http.StatusOK, openlinker.RuntimeDrainPayload{
			DeadlineAt: request.DeadlineAt, ReasonCode: request.ReasonCode, Capacity: 0, Inflight: inflight,
		})
	case post && strings.HasPrefix(runtimePath, "/sessions/") && strings.HasSuffix(path, "/close"):
		var request openlinker.RuntimeSessionCloseRequest
		if !core.decode(w, r, &request) {
			return
		}
		core.mu.Lock()
		core.closes = append(core.closes, request)
		core.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case post && runtimePath == "/runs/claim":
		core.serveClaim(w, r)
	case post && runtimePath == "/runs/resume":
		var request openlinker.RuntimeResumePayload
		if !core.decode(w, r, &request) {
			return
		}
		decisions := make([]openlinker.RuntimeResumeAcceptedPayload, len(request.Attempts))
		for index, attempt := range request.Attempts {
			expires := time.Now().Add(time.Minute).UTC()
			decisions[index] = openlinker.RuntimeResumeAcceptedPayload{
				AttemptIdentity: attempt.AttemptIdentity, Decision: openlinker.RuntimeResumeContinue, LeaseExpiresAt: &expires,
				AllowedActions: []openlinker.RuntimeResumeAction{
					openlinker.RuntimeActionContinueExecution, openlinker.RuntimeActionUploadEvents, openlinker.RuntimeActionUploadResult,
				},
			}
		}
		writeJSON(w, http.StatusOK, openlinker.RuntimeResumeResponse{Decisions: decisions})
	case post && strings.HasPrefix(runtimePath, "/runs/") && strings.HasSuffix(path, "/assignment-ack"):
		var request openlinker.RuntimeAssignmentAckPayload
		if !core.decode(w, r, &request) {
			return
		}
		writeJSON(w, http.StatusOK, openlinker.RuntimeAssignmentConfirmedPayload{
			AttemptIdentity: request.AttemptIdentity, AttemptNo: 1, LeaseExpiresAt: time.Now().Add(time.Minute).UTC(),
		})
	case post && strings.HasPrefix(runtimePath, "/runs/") && strings.HasSuffix(path, "/assignment-reject"):
		var request openlinker.RuntimeAssignmentRejectPayload
		if !core.decode(w, r, &request) {
			return
		}
		core.mu.Lock()
		core.inflight--
		core.mu.Unlock()
		writeJSON(w, http.StatusOK, openlinker.RuntimeAssignmentRejectedPayload{
			AttemptIdentity: request.AttemptIdentity, Outcome: openlinker.RuntimeOfferRejected, DispatchState: openlinker.RuntimeDispatchPending,
		})
	case post && strings.HasPrefix(runtimePath, "/runs/") && strings.HasSuffix(path, "/lease-renew"):
		var request openlinker.RuntimeLeaseRenewPayload
		if !core.decode(w, r, &request) {
			return
		}
		writeJSON(w, http.StatusOK, openlinker.RuntimeLeaseRenewedPayload{
			AttemptIdentity: request.AttemptIdentity, LeaseExpiresAt: time.Now().Add(time.Minute).UTC(),
		})
	case post && strings.HasPrefix(runtimePath, "/runs/") && strings.HasSuffix(path, "/events"):
		var request openlinker.RuntimeRunEventPayload
		if !core.decode(w, r, &request) {
			return
		}
		writeJSON(w, http.StatusOK, openlinker.RuntimeRunEventAckPayload{
			ClientEventID: request.ClientEventID, ClientEventSeq: request.ClientEventSeq, Sequence: request.ClientEventSeq,
		})
	case post && strings.HasPrefix(runtimePath, "/runs/") && strings.HasSuffix(path, "/result"):
		var request openlinker.RuntimeRunResultPayload
		if !core.decode(w, r, &request) {
			return
		}
		core.mu.Lock()
		core.results = append(core.results, request)
		core.inflight--
		core.mu.Unlock()
		ack := openlinker.RuntimeRunResultAckPayload{
			ResultID: request.ResultID, Classification: openlinker.RuntimeResultSuccess,
			RunStatus: openlinker.RuntimeRunSuccess, DispatchState: openlinker.RuntimeDispatchTerminal,
		}
		if request.Status != "success" {
			ack.Classification, ack.RunStatus = openlinker.RuntimeResultNonRetryableFailure, openlinker.RuntimeRunFailed
		}
		writeJSON(w, http.StatusOK, ack)
	case r.Method == http.MethodGet && runtimePath == "/commands":
		core.idle(r)
		writeJSON(w, http.StatusOK, openlinker.RuntimeCommandsResponse{
			Commands: []openlinker.RuntimePendingCommand{}, DatabaseTime: time.Now().UTC(),
		})
	default:
		core.mu.Lock()
		core.unexpected = append(core.unexpected, r.Method+" "+path)
		core.mu.Unlock()
		http.Error(w, "unexpected fake Core request", http.StatusNotFound)
	}
}

func (core *drainTestCore) serveA2AIngress(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if !core.decode(w, r, &request) {
		return
	}
	core.mu.Lock()
	core.ingress = append(core.ingress, request.ID)
	core.mu.Unlock()
	if strings.Contains(request.ID, "slow") {
		select {
		case <-core.releaseSlow:
		case <-r.Context().Done():
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"kind": "message"}})
}

func (core *drainTestCore) releaseSlowRequests() {
	core.releaseOnce.Do(func() { close(core.releaseSlow) })
}

func (core *drainTestCore) ingressIDs() []string {
	core.mu.Lock()
	defer core.mu.Unlock()
	return append([]string(nil), core.ingress...)
}

func (core *drainTestCore) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		core.mu.Lock()
		core.unexpected = append(core.unexpected, "undecodable "+r.URL.Path)
		core.mu.Unlock()
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func (core *drainTestCore) idle(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(50 * time.Millisecond):
	}
}

func (core *drainTestCore) serveClaim(w http.ResponseWriter, r *http.Request) {
	core.mu.Lock()
	if len(core.queue) == 0 || core.hello.RuntimeSessionID == "" {
		core.mu.Unlock()
		core.idle(r)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	run := core.queue[0]
	core.queue = core.queue[1:]
	core.claimed = append(core.claimed, run.RunID)
	core.inflight++
	hello := core.hello
	core.mu.Unlock()
	now := time.Now().UTC()
	writeJSON(w, http.StatusOK, openlinker.RuntimeRunAssignedPayload{
		AttemptIdentity: openlinker.RuntimeAttemptIdentity{
			RunID: run.RunID, AttemptID: run.AttemptID, LeaseID: run.LeaseID, FencingToken: 1,
			NodeID: hello.NodeID, AgentID: hello.AgentID, WorkerID: hello.WorkerID, RuntimeSessionID: hello.RuntimeSessionID,
		},
		OfferNo: 1, OfferExpiresAt: now.Add(time.Minute), AttemptDeadlineAt: now.Add(2 * time.Minute),
		RunDeadlineAt: now.Add(3 * time.Minute),
		Input:         map[string]any{"text": "drain test"}, Metadata: map[string]any{"source": "drain-test"},
		NodeEnvelope: "ol_ctx_v2.header.payload.signature", AgentInvocationToken: "ol_inv_v2.header.payload.signature",
	})
}

type drainTestSnapshot struct {
	hello           openlinker.RuntimeHelloPayload
	claimed         []string
	queued          int
	sessionAttempts int
	drains          []openlinker.RuntimeDrainPayload
	results         []openlinker.RuntimeRunResultPayload
	closes          []openlinker.RuntimeSessionCloseRequest
	unexpected      []string
}

func (core *drainTestCore) snapshot() drainTestSnapshot {
	core.mu.Lock()
	defer core.mu.Unlock()
	return drainTestSnapshot{
		hello:           core.hello,
		claimed:         append([]string(nil), core.claimed...),
		queued:          len(core.queue),
		sessionAttempts: core.sessionAttempts,
		drains:          append([]openlinker.RuntimeDrainPayload(nil), core.drains...),
		results:         append([]openlinker.RuntimeRunResultPayload(nil), core.results...),
		closes:          append([]openlinker.RuntimeSessionCloseRequest(nil), core.closes...),
		unexpected:      append([]string(nil), core.unexpected...),
	}
}

type drainTestAdapter struct {
	mu       sync.Mutex
	runs     []string
	started  chan string
	release  chan struct{}
	canceled atomic.Bool
}

func newDrainTestAdapter() *drainTestAdapter {
	return &drainTestAdapter{started: make(chan string, 4), release: make(chan struct{})}
}

func (adapter *drainTestAdapter) Run(ctx context.Context, _ any, runCtx RunContext) (any, error) {
	adapter.mu.Lock()
	adapter.runs = append(adapter.runs, runCtx.RunID)
	adapter.mu.Unlock()
	select {
	case adapter.started <- runCtx.RunID:
	default:
	}
	select {
	case <-adapter.release:
		return map[string]any{"ok": true}, nil
	case <-ctx.Done():
		adapter.canceled.Store(true)
		return nil, ctx.Err()
	}
}

func (adapter *drainTestAdapter) executed() []string {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return append([]string(nil), adapter.runs...)
}

type drainTestLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *drainTestLog) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(data)
}

func (output *drainTestLog) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func newDrainTestNode(t *testing.T, core *drainTestCore, adapter Adapter, output *drainTestLog) *Node {
	t.Helper()
	return &Node{
		OpenLinkerURL: core.server.URL,
		Transport:     string(openlinker.RuntimeTransportPull),
		AgentID:       drainTestAgentID,
		AgentToken:    "synthetic-drain-test-token",
		DataDir:       filepath.Join(t.TempDir(), "runtime"),
		Capacity:      1,
		ClaimWait:     time.Second,
		CommandWait:   time.Second,
		RetryMinimum:  10 * time.Millisecond,
		RetryMaximum:  50 * time.Millisecond,
		Adapter:       adapter,
		Logger:        log.New(output, "", 0),
	}
}

func waitForDrainTestRun(t *testing.T, adapter *drainTestAdapter, runID string) {
	t.Helper()
	select {
	case started := <-adapter.started:
		if started != runID {
			t.Fatalf("adapter started run %s, want %s", started, runID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("adapter did not start the first run")
	}
}

func waitForServe(t *testing.T, served <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-served:
		return err
	case <-time.After(timeout):
		t.Fatalf("Serve did not return within %s", timeout)
		return nil
	}
}

func assertDrainTestStoreClosed(t *testing.T, node *Node) {
	t.Helper()
	node.mu.Lock()
	started, worker := node.started, node.worker
	node.mu.Unlock()
	if started || worker != nil {
		t.Fatalf("Node did not finish Start: started=%v worker=%v", started, worker != nil)
	}
	store, err := openlinker.OpenFileRuntimeStore(node.DataDir)
	if err != nil {
		t.Fatalf("SDK store was not closed by shutdown: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessShutdownBudgetExceedsDrainAndSDKGrace(t *testing.T) {
	// The SDK waits up to 2s after its grace for canceled handlers to return.
	minimum := DefaultDrainTimeout + openlinker.RuntimeWorkerDefaultShutdownTimeout + 2*time.Second
	if ProcessShutdownTimeout <= minimum {
		t.Fatalf("process shutdown budget %s must exceed drain + SDK grace + force wait (%s)", ProcessShutdownTimeout, minimum)
	}
	if DefaultDrainTimeout < 10*time.Second {
		t.Fatalf("drain timeout %s grants in-flight work less than the previous 10s", DefaultDrainTimeout)
	}
}

func TestServeDrainsInFlightRunBeforeStopping(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA, drainTestRunB)
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	waitForDrainTestRun(t, adapter, drainTestRunA.RunID)
	signals <- os.Interrupt
	select {
	case <-core.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not drain the Runtime Session")
	}
	// A non-draining Worker would keep the old hard-stop behaviour or claim the
	// queued run; give either a chance to happen before releasing the handler.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-served:
		t.Fatalf("Serve returned before the in-flight run finished: %v", err)
	default:
	}
	if adapter.canceled.Load() {
		t.Fatal("drain canceled the in-flight adapter")
	}
	released := time.Now()
	close(adapter.release)
	if err := waitForServe(t, served, DefaultDrainTimeout); err != nil {
		t.Fatalf("graceful shutdown returned %v; logs:\n%s", err, output.String())
	}
	if elapsed := time.Since(released); elapsed >= DefaultDrainTimeout {
		t.Fatalf("drain waited %s after the run finished", elapsed)
	}

	snapshot := core.snapshot()
	if len(snapshot.drains) == 0 || snapshot.drains[0].Capacity != 0 {
		t.Fatalf("Core drain requests = %#v", snapshot.drains)
	}
	if len(snapshot.results) != 1 || snapshot.results[0].AttemptIdentity.RunID != drainTestRunA.RunID ||
		snapshot.results[0].Status != "success" {
		t.Fatalf("in-flight result did not reach Core: %#v", snapshot.results)
	}
	if len(snapshot.claimed) != 1 || snapshot.queued != 1 {
		t.Fatalf("Worker claimed new work during drain: claimed=%v queued=%d", snapshot.claimed, snapshot.queued)
	}
	if executed := adapter.executed(); len(executed) != 1 || executed[0] != drainTestRunA.RunID || adapter.canceled.Load() {
		t.Fatalf("adapter executions = %v canceled=%v", executed, adapter.canceled.Load())
	}
	if len(snapshot.closes) == 0 || snapshot.closes[len(snapshot.closes)-1].Status != "closed" {
		t.Fatalf("Runtime Session was not closed: %#v", snapshot.closes)
	}
	if len(snapshot.unexpected) != 0 {
		t.Fatalf("unexpected fake Core requests: %v", snapshot.unexpected)
	}
	if logs := output.String(); strings.Contains(logs, "timed out") || strings.Contains(logs, "drain did not complete") {
		t.Fatalf("graceful shutdown logged a failure:\n%s", logs)
	}
	assertDrainTestStoreClosed(t, node)
}

func TestServeStopsWorkerThatIsNotReadyWithoutDrain(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	core.failSessions = true
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	eventuallyForTest(t, 10*time.Second, func() bool {
		return core.snapshot().sessionAttempts >= 2
	}, "Worker retrying session creation")
	signaled := time.Now()
	signals <- os.Interrupt
	if err := waitForServe(t, served, DefaultDrainTimeout); err != nil {
		t.Fatalf("stopping a starting Worker returned %v", err)
	}
	if elapsed := time.Since(signaled); elapsed > 3*time.Second {
		t.Fatalf("stopping a Worker that was not ready took %s", elapsed)
	}
	snapshot := core.snapshot()
	if len(snapshot.drains) != 0 || len(snapshot.claimed) != 0 || len(adapter.executed()) != 0 {
		t.Fatalf("not-ready Worker drained or executed: drains=%d claimed=%v", len(snapshot.drains), snapshot.claimed)
	}
	assertDrainTestStoreClosed(t, node)
}

func TestServeSecondSignalAbandonsDrain(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	// Core never reports the Session as empty, so only the second signal can
	// end the drain before its own timeout.
	core.stickyInflight = true
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	waitForDrainTestRun(t, adapter, drainTestRunA.RunID)
	signaled := time.Now()
	signals <- os.Interrupt
	select {
	case <-core.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("first signal did not drain the Runtime Session")
	}
	signals <- os.Interrupt
	eventuallyForTest(t, 5*time.Second, func() bool {
		return strings.Contains(output.String(), "second signal")
	}, "second signal handling")
	close(adapter.release)
	if err := waitForServe(t, served, DefaultDrainTimeout); err != nil {
		t.Fatalf("second-signal stop returned %v; logs:\n%s", err, output.String())
	}
	if elapsed := time.Since(signaled); elapsed >= DefaultDrainTimeout {
		t.Fatalf("second signal still waited for the drain timeout (%s)", elapsed)
	}
	if logs := output.String(); strings.Contains(logs, "drain did not complete") {
		t.Fatalf("an operator-abandoned drain was reported as a failure:\n%s", logs)
	}
	assertDrainTestStoreClosed(t, node)
}

func TestServeReportsTimeoutWhenShutdownExceedsBudget(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	core.stickyInflight = true
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.serve(signals, 300*time.Millisecond) }()

	waitForDrainTestRun(t, adapter, drainTestRunA.RunID)
	signals <- os.Interrupt
	err := waitForServe(t, served, 5*time.Second)
	var timeout *ShutdownTimeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exceeded shutdown budget returned %v", err)
	}
	// The SDK Worker keeps its own grace; let the handler finish so the
	// background shutdown completes before the temporary directory is removed.
	close(adapter.release)
	eventuallyForTest(t, 15*time.Second, func() bool {
		node.mu.Lock()
		defer node.mu.Unlock()
		return !node.started
	}, "background Worker shutdown")
	assertDrainTestStoreClosed(t, node)
}

func TestShutdownWithoutStartIsNoop(t *testing.T) {
	node := &Node{}
	if err := node.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
