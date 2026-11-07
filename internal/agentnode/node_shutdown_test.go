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
	shutdownTestAgentID      = "22222222-2222-4222-8222-222222222222"
	shutdownTestCoreID       = "33333333-3333-4333-8333-333333333333"
	shutdownTestAttachmentID = "99999999-9999-4999-8999-999999999998"
	shutdownTestRuntimePath  = "/api/v1/agent-runtime"
)

type shutdownTestRun struct {
	RunID, AttemptID, LeaseID string
}

var (
	shutdownTestRunA = shutdownTestRun{
		RunID:     "44444444-4444-4444-8444-444444444441",
		AttemptID: "55555555-5555-4555-8555-555555555551",
		LeaseID:   "66666666-6666-4666-8666-666666666661",
	}
	shutdownTestRunB = shutdownTestRun{
		RunID:     "44444444-4444-4444-8444-444444444442",
		AttemptID: "55555555-5555-4555-8555-555555555552",
		LeaseID:   "66666666-6666-4666-8666-666666666662",
	}
)

type shutdownTestCore struct {
	server *httptest.Server

	mu              sync.Mutex
	hello           openlinker.RuntimeHelloPayload
	queue           []shutdownTestRun
	claimed         []string
	inflight        int64
	stickyInflight  bool
	failSessions    bool
	sessionAttempts int
	drains          []openlinker.RuntimeDrainPayload
	results         []openlinker.RuntimeRunResultPayload
	closes          []openlinker.RuntimeSessionCloseRequest
	unexpected      []string

	drained     chan struct{}
	drainedOnce sync.Once
}

func newShutdownTestCore(t *testing.T, runs ...shutdownTestRun) *shutdownTestCore {
	t.Helper()
	core := &shutdownTestCore{queue: append([]shutdownTestRun(nil), runs...), drained: make(chan struct{})}
	core.server = httptest.NewServer(core)
	t.Cleanup(core.server.Close)
	return core
}

func (core *shutdownTestCore) ready() openlinker.RuntimeReadyPayload {
	return openlinker.RuntimeReadyPayload{
		CoreInstanceID: shutdownTestCoreID, AttachmentID: shutdownTestAttachmentID,
		Features: openlinker.RuntimeRequiredFeatures(), OfferTTLSeconds: 30, LeaseTTLSeconds: 60,
		DatabaseTime: time.Now().UTC(),
	}
}

func (core *shutdownTestCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	runtimePath := strings.TrimPrefix(path, shutdownTestRuntimePath)
	post := r.Method == http.MethodPost
	switch {
	case r.Method == http.MethodGet && path == "/.well-known/openlinker.json":
		writeJSON(w, http.StatusOK, map[string]any{
			"base_urls": map[string]any{"runtime": "http://" + r.Host},
			"runtime": map[string]any{
				"enabled": true, "mtls_required": false,
				"transports": []string{"pull"}, "default_transport": "pull",
			},
		})
	case post && path == shutdownTestRuntimePath+"/sessions":
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

func (core *shutdownTestCore) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		core.mu.Lock()
		core.unexpected = append(core.unexpected, "undecodable "+r.URL.Path)
		core.mu.Unlock()
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func (core *shutdownTestCore) idle(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(50 * time.Millisecond):
	}
}

func (core *shutdownTestCore) serveClaim(w http.ResponseWriter, r *http.Request) {
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
		Input:         map[string]any{"text": "shutdown test"}, Metadata: map[string]any{"source": "shutdown-test"},
		NodeEnvelope: "ol_ctx_v2.header.payload.signature", AgentInvocationToken: "ol_inv_v2.header.payload.signature",
	})
}

type shutdownTestSnapshot struct {
	hello           openlinker.RuntimeHelloPayload
	claimed         []string
	queued          int
	sessionAttempts int
	drains          []openlinker.RuntimeDrainPayload
	results         []openlinker.RuntimeRunResultPayload
	closes          []openlinker.RuntimeSessionCloseRequest
	unexpected      []string
}

func (core *shutdownTestCore) snapshot() shutdownTestSnapshot {
	core.mu.Lock()
	defer core.mu.Unlock()
	return shutdownTestSnapshot{
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

type shutdownTestAdapter struct {
	mu       sync.Mutex
	runs     []string
	started  chan string
	release  chan struct{}
	canceled atomic.Bool
}

func newShutdownTestAdapter() *shutdownTestAdapter {
	return &shutdownTestAdapter{started: make(chan string, 4), release: make(chan struct{})}
}

func (adapter *shutdownTestAdapter) Run(ctx context.Context, _ any, runCtx RunContext) (any, error) {
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

func (adapter *shutdownTestAdapter) executed() []string {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return append([]string(nil), adapter.runs...)
}

type shutdownTestLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *shutdownTestLog) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(data)
}

func (output *shutdownTestLog) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func newShutdownTestNode(t *testing.T, core *shutdownTestCore, adapter Adapter, output *shutdownTestLog) *Node {
	t.Helper()
	return &Node{
		OpenLinkerURL: core.server.URL,
		Transport:     string(openlinker.RuntimeTransportPull),
		AgentID:       shutdownTestAgentID,
		AgentToken:    "synthetic-shutdown-test-token",
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

func waitForShutdownTestRun(t *testing.T, adapter *shutdownTestAdapter, runID string) {
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

func assertShutdownTestStoreClosed(t *testing.T, node *Node) {
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

func TestProcessShutdownBudgetExceedsSDKGrace(t *testing.T) {
	// After its grace the SDK waits up to 2s for canceled handlers to return.
	minimum := openlinker.RuntimeWorkerDefaultShutdownTimeout + 2*time.Second
	if ProcessShutdownTimeout <= minimum {
		t.Fatalf("process shutdown budget %s must exceed the SDK grace and force wait (%s)", ProcessShutdownTimeout, minimum)
	}
}

func countStops(node *Node) *atomic.Int32 {
	var stops atomic.Int32
	node.stopHook = func() { stops.Add(1) }
	return &stops
}

func waitForShutdownTestNodeStopped(t *testing.T, node *Node) {
	t.Helper()
	eventuallyForTest(t, 20*time.Second, func() bool {
		node.mu.Lock()
		defer node.mu.Unlock()
		return !node.started
	}, "background Node shutdown")
}

// A signal stops the Node once through the SDK Worker's Stop; the run in
// flight finishes inside the SDK grace, the store is closed and Serve returns
// nil (exit 0) instead of racing an equal outer timer. There is no drain.
func TestServeStopsOnSignalAndClosesStore(t *testing.T) {
	core := newShutdownTestCore(t, shutdownTestRunA)
	adapter := newShutdownTestAdapter()
	output := &shutdownTestLog{}
	node := newShutdownTestNode(t, core, adapter, output)
	stops := countStops(node)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	waitForShutdownTestRun(t, adapter, shutdownTestRunA.RunID)
	signals <- os.Interrupt
	eventuallyForTest(t, 5*time.Second, func() bool { return stops.Load() == 1 }, "Stop after the signal")
	close(adapter.release)
	if err := waitForServe(t, served, ProcessShutdownTimeout); err != nil {
		t.Fatalf("shutdown returned %v; logs:\n%s", err, output.String())
	}
	snapshot := core.snapshot()
	if stops.Load() != 1 || len(snapshot.drains) != 0 || adapter.canceled.Load() {
		t.Fatalf("stops=%d drains=%d canceled=%v", stops.Load(), len(snapshot.drains), adapter.canceled.Load())
	}
	if len(snapshot.closes) == 0 || snapshot.closes[len(snapshot.closes)-1].Status != "closed" {
		t.Fatalf("Runtime Session was not closed: %#v", snapshot.closes)
	}
	if len(snapshot.unexpected) != 0 {
		t.Fatalf("unexpected fake Core requests: %v", snapshot.unexpected)
	}
	assertShutdownTestStoreClosed(t, node)
}

func TestServeStopsWorkerThatIsNotReady(t *testing.T) {
	core := newShutdownTestCore(t, shutdownTestRunA)
	core.failSessions = true
	adapter := newShutdownTestAdapter()
	output := &shutdownTestLog{}
	node := newShutdownTestNode(t, core, adapter, output)
	stops := countStops(node)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	eventuallyForTest(t, 10*time.Second, func() bool {
		return core.snapshot().sessionAttempts >= 2
	}, "Worker retrying session creation")
	signaled := time.Now()
	signals <- os.Interrupt
	if err := waitForServe(t, served, ProcessShutdownTimeout); err != nil {
		t.Fatalf("stopping a starting Worker returned %v", err)
	}
	if elapsed := time.Since(signaled); elapsed > 3*time.Second {
		t.Fatalf("stopping a Worker that was not ready took %s", elapsed)
	}
	if snapshot := core.snapshot(); stops.Load() != 1 || len(snapshot.drains) != 0 || len(snapshot.claimed) != 0 {
		t.Fatalf("stops=%d drains=%d claimed=%v", stops.Load(), len(snapshot.drains), snapshot.claimed)
	}
	assertShutdownTestStoreClosed(t, node)
}

// Stop blocked past the budget (a run that outlives it keeps the SDK grace
// busy) returns the timeout error within the budget.
func TestServeReportsTimeoutWhenStopExceedsBudget(t *testing.T) {
	core := newShutdownTestCore(t, shutdownTestRunA)
	adapter := newShutdownTestAdapter()
	output := &shutdownTestLog{}
	node := newShutdownTestNode(t, core, adapter, output)
	stops := countStops(node)
	budget := 500 * time.Millisecond
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.serve(signals, budget) }()

	waitForShutdownTestRun(t, adapter, shutdownTestRunA.RunID)
	signaled := time.Now()
	signals <- os.Interrupt
	err := waitForServe(t, served, 5*time.Second)
	var timeout *ShutdownTimeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exceeded shutdown budget returned %v", err)
	}
	if elapsed := time.Since(signaled); elapsed > budget+time.Second {
		t.Fatalf("Serve took %s with a %s budget", elapsed, budget)
	}
	if stops.Load() != 1 {
		t.Fatalf("Stop calls = %d, want 1", stops.Load())
	}
	// The SDK Worker keeps its own grace; let the run finish so the background
	// shutdown completes before the temporary directory is removed.
	close(adapter.release)
	waitForShutdownTestNodeStopped(t, node)
	if stops.Load() != 1 {
		t.Fatalf("Stop calls = %d, want 1", stops.Load())
	}
	assertShutdownTestStoreClosed(t, node)
}

// A second signal while Stop is still blocked returns promptly through the
// timeout path and never starts a second Stop.
func TestServeSecondSignalDuringBlockedStopReturnsPromptly(t *testing.T) {
	core := newShutdownTestCore(t, shutdownTestRunA)
	adapter := newShutdownTestAdapter()
	output := &shutdownTestLog{}
	node := newShutdownTestNode(t, core, adapter, output)
	stops := countStops(node)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	waitForShutdownTestRun(t, adapter, shutdownTestRunA.RunID)
	signals <- os.Interrupt
	eventuallyForTest(t, 5*time.Second, func() bool { return stops.Load() == 1 }, "Stop after the first signal")
	signaled := time.Now()
	signals <- os.Interrupt
	err := waitForServe(t, served, 5*time.Second)
	var timeout *ShutdownTimeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, errSecondSignal) {
		t.Fatalf("second signal returned %v, want the timeout path", err)
	}
	if elapsed := time.Since(signaled); elapsed > time.Second {
		t.Fatalf("second signal took %s to return", elapsed)
	}
	time.Sleep(200 * time.Millisecond)
	if stops.Load() != 1 {
		t.Fatalf("Stop calls = %d, want exactly 1", stops.Load())
	}
	close(adapter.release)
	waitForShutdownTestNodeStopped(t, node)
	if stops.Load() != 1 {
		t.Fatalf("Stop calls = %d, want exactly 1", stops.Load())
	}
	assertShutdownTestStoreClosed(t, node)
}
