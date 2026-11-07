package agentnode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const drainTestNodeID = "11111111-1111-4111-8111-111111111111"

func postPublicA2A(baseURL, id string) (int, error) {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	body := `{"jsonrpc":"2.0","id":"` + id + `","method":"SendMessage","params":{"message":{"messageId":"` + id + `"}}}`
	response, err := client.Post(baseURL+"/", "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

// helperRecordingAdapter exposes the run-scoped helper session of the Run.
type helperRecordingAdapter struct {
	*drainTestAdapter
	helper atomic.Pointer[HelperInfo]
}

func (adapter *helperRecordingAdapter) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	adapter.helper.Store(runCtx.Helper)
	return adapter.drainTestAdapter.Run(ctx, input, runCtx)
}

// The public A2A listener forwards new requests to Core. Once a drain fences
// admission it must not accept new ingress, while a request already in flight
// finishes and the localhost run helper keeps serving the in-flight Run.
func TestServeDrainStopsPublicA2AIngressButFinishesInFlightRequests(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	adapter := &helperRecordingAdapter{drainTestAdapter: newDrainTestAdapter()}
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	node.NodeID = drainTestNodeID
	node.PublicA2A = &PublicA2AServer{Host: "127.0.0.1", Slug: "drain-agent"}
	node.Helper = &LocalHelperServer{Host: "127.0.0.1"}
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	waitForDrainTestRun(t, adapter.drainTestAdapter, drainTestRunA.RunID)
	baseURL := node.PublicA2A.BaseURL()
	helper := adapter.helper.Load()
	if helper == nil {
		t.Fatal("Run has no localhost helper session")
	}
	if status, err := postPublicA2A(baseURL, "before-drain"); err != nil || status != http.StatusOK {
		t.Fatalf("positive control ingress = %d %v", status, err)
	}
	slowDone := make(chan error, 1)
	go func() {
		status, err := postPublicA2A(baseURL, "slow-in-flight")
		if err == nil && status != http.StatusOK {
			err = errors.New("in-flight public A2A request was not completed")
		}
		slowDone <- err
	}()
	eventuallyForTest(t, 5*time.Second, func() bool { return len(core.ingressIDs()) == 2 }, "in-flight public A2A request")

	signals <- os.Interrupt
	select {
	case <-core.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not drain the Runtime Session")
	}
	// Concurrent ingress after the drain began must never reach Core.
	statuses := make(chan int, 8)
	for index := 0; index < cap(statuses); index++ {
		go func() {
			status, err := postPublicA2A(baseURL, "during-drain")
			if err != nil {
				status = 0 // refused: the listener no longer accepts connections
			}
			statuses <- status
		}()
	}
	for index := 0; index < cap(statuses); index++ {
		if status := <-statuses; status == http.StatusOK {
			t.Fatal("public A2A accepted a new request during drain")
		}
	}
	if ids := core.ingressIDs(); len(ids) != 2 {
		t.Fatalf("Core received public A2A ingress during drain: %v", ids)
	}
	// The run-scoped localhost helper still serves the in-flight Run.
	event, err := http.NewRequest(http.MethodPost, helper.Endpoints.Events,
		strings.NewReader(`{"event_type":"run.progress","payload":{"step":"during-drain"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range helper.Headers {
		event.Header.Set(key, value)
	}
	event.Header.Set("Content-Type", "application/json")
	helperResponse, err := (&http.Client{Timeout: 5 * time.Second}).Do(event)
	if err != nil {
		t.Fatalf("localhost helper stopped during drain: %v", err)
	}
	_ = helperResponse.Body.Close()
	if helperResponse.StatusCode != http.StatusOK {
		t.Fatalf("localhost helper event during drain = %d", helperResponse.StatusCode)
	}

	core.releaseSlowRequests()
	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight public A2A request did not finish")
	}
	close(adapter.release)
	if err := waitForServe(t, served, DefaultDrainTimeout); err != nil {
		t.Fatalf("graceful shutdown returned %v; logs:\n%s", err, output.String())
	}
	if snapshot := core.snapshot(); len(snapshot.results) != 1 || len(snapshot.unexpected) != 0 {
		t.Fatalf("results=%d unexpected=%v", len(snapshot.results), snapshot.unexpected)
	}
	assertDrainTestStoreClosed(t, node)
}

// A signal while the Worker is still starting stops it without a drain, but
// the public A2A listener still closes gracefully: an active request finishes
// before Serve returns, so the process exit cannot cut it off.
func TestServeNotReadyWorkerStillWaitsForActivePublicA2ARequest(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	core.failSessions = true // session creation keeps retrying: never ready
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	node.NodeID = drainTestNodeID
	node.PublicA2A = &PublicA2AServer{Host: "127.0.0.1", Slug: "drain-agent"}
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	eventuallyForTest(t, 10*time.Second, func() bool {
		return core.snapshot().sessionAttempts >= 2 && node.PublicA2A.BaseURL() != ""
	}, "public A2A listener while the Worker retries session creation")
	baseURL := node.PublicA2A.BaseURL()
	slowDone := make(chan error, 1)
	go func() {
		status, err := postPublicA2A(baseURL, "slow-not-ready")
		if err == nil && status != http.StatusOK {
			err = errors.New("active public A2A request was not completed")
		}
		slowDone <- err
	}()
	eventuallyForTest(t, 5*time.Second, func() bool { return len(core.ingressIDs()) == 1 }, "active public A2A request")

	signals <- os.Interrupt
	// The listener stops accepting connections without any request reaching Core.
	address := strings.TrimPrefix(baseURL, "http://")
	eventuallyForTest(t, 5*time.Second, func() bool {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return true
		}
		_ = connection.Close()
		return false
	}, "public A2A listener to stop accepting connections")
	if status, err := postPublicA2A(baseURL, "after-signal"); err == nil && status == http.StatusOK {
		t.Fatal("public A2A accepted a request after the signal")
	}
	// The active request keeps Serve from returning.
	select {
	case err := <-served:
		t.Fatalf("Serve returned while a public A2A request was still active: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	core.releaseSlowRequests()
	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active public A2A request did not finish")
	}
	if err := waitForServe(t, served, DefaultShutdownTimeout); err != nil {
		t.Fatalf("not-ready shutdown returned %v; logs:\n%s", err, output.String())
	}
	snapshot := core.snapshot()
	if ids := core.ingressIDs(); len(ids) != 1 {
		t.Fatalf("Core public A2A ingress = %v, want only the active request", ids)
	}
	if len(snapshot.drains) != 0 || len(snapshot.claimed) != 0 {
		t.Fatalf("not-ready Worker drained or claimed: drains=%d claimed=%v", len(snapshot.drains), snapshot.claimed)
	}
	assertDrainTestStoreClosed(t, node)
}

// When the drain expires with the handler still running, the SDK Worker's own
// shutdown grace still applies: the handler is not canceled, the store closes
// and the process path returns nil within the outer budget.
func TestServeDrainTimeoutStillGrantsSDKGrace(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	core.stickyInflight = true // Core never reports the Session empty.
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	node.drainTimeout = 300 * time.Millisecond
	budget := node.drainTimeout + openlinker.RuntimeWorkerDefaultShutdownTimeout + processShutdownMargin
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.serve(signals, budget) }()

	waitForDrainTestRun(t, adapter, drainTestRunA.RunID)
	signaled := time.Now()
	signals <- os.Interrupt
	eventuallyForTest(t, 5*time.Second, func() bool {
		return strings.Contains(output.String(), "drain did not complete")
	}, "drain expiry")
	// Past the drain timeout, inside the SDK grace: the handler keeps running.
	time.Sleep(500 * time.Millisecond)
	if adapter.canceled.Load() {
		t.Fatal("handler was canceled when the drain expired instead of after the SDK grace")
	}
	select {
	case err := <-served:
		t.Fatalf("Serve returned before the handler finished: %v", err)
	default:
	}
	close(adapter.release)
	if err := waitForServe(t, served, budget); err != nil {
		t.Fatalf("shutdown after drain expiry returned %v; logs:\n%s", err, output.String())
	}
	if elapsed := time.Since(signaled); elapsed >= budget {
		t.Fatalf("shutdown took %s, budget %s", elapsed, budget)
	}
	if adapter.canceled.Load() || len(adapter.executed()) != 1 {
		t.Fatalf("handler canceled=%v executions=%v", adapter.canceled.Load(), adapter.executed())
	}
	assertDrainTestStoreClosed(t, node)
	// The completed Result is never lost: Core received it, or it stays in the
	// durable spool for the next start, or both when shutdown closed the
	// transport before the ACK was recorded (a re-send then carries the same
	// stable ResultID, which Core applies once).
	store, err := openlinker.OpenFileRuntimeStore(node.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	pending, pendingErr := store.PendingResult(drainTestRunA.AttemptID)
	_ = store.Close()
	if pendingErr != nil && !errors.Is(pendingErr, openlinker.ErrSpoolRecordNotFound) {
		t.Fatal(pendingErr)
	}
	spooled := pendingErr == nil
	results := core.snapshot().results
	if len(results) == 0 && !spooled {
		t.Fatal("the completed Result was neither delivered nor spooled")
	}
	for _, result := range results {
		if result.ResultID != results[0].ResultID || (spooled && pending.ResultID != result.ResultID) {
			t.Fatalf("Result IDs differ between delivery and spool: delivered=%v spooled=%v", results, pending.ResultID)
		}
	}
}

// stuckDrain stands in for an SDK Drain blocked on a store mutex that a spool
// write holds across a stalled fsync: it ignores its context until released.
type stuckDrain struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	enter   sync.Once
}

func newStuckDrain(t *testing.T) *stuckDrain {
	drain := &stuckDrain{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(drain.unblock)
	return drain
}

func (drain *stuckDrain) unblock() { drain.once.Do(func() { close(drain.release) }) }

func (drain *stuckDrain) run(context.Context, *openlinker.RuntimeWorker, openlinker.RuntimeWorkerDrainOptions) error {
	drain.enter.Do(func() { close(drain.entered) })
	<-drain.release
	return errors.New("store write stalled")
}

func waitForStuckDrain(t *testing.T, drain *stuckDrain) {
	t.Helper()
	select {
	case <-drain.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not reach the drain")
	}
}

func waitForDrainTestNodeStopped(t *testing.T, node *Node) {
	t.Helper()
	eventuallyForTest(t, 20*time.Second, func() bool {
		node.mu.Lock()
		defer node.mu.Unlock()
		return !node.started
	}, "background Node shutdown")
}

// With the drain blocked indefinitely, Serve still returns at the outer
// budget through the existing timeout path.
func TestServeReturnsWithinBudgetWhenDrainIsBlocked(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	drain := newStuckDrain(t)
	node.drainWorker = drain.run
	budget := 500 * time.Millisecond
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.serve(signals, budget) }()

	waitForDrainTestRun(t, adapter, drainTestRunA.RunID)
	signaled := time.Now()
	signals <- os.Interrupt
	waitForStuckDrain(t, drain)
	err := waitForServe(t, served, 10*time.Second)
	var timeout *ShutdownTimeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked drain returned %v, want the shutdown timeout", err)
	}
	if elapsed := time.Since(signaled); elapsed > budget+time.Second {
		t.Fatalf("Serve took %s with a %s budget while the drain was blocked", elapsed, budget)
	}
	// Let the stuck shutdown finish so the test leaves nothing running.
	drain.unblock()
	close(adapter.release)
	waitForDrainTestNodeStopped(t, node)
	assertDrainTestStoreClosed(t, node)
}

// A second signal stops the Worker even while the drain is blocked, and Serve
// returns without waiting for the stuck drain or the outer budget.
func TestServeSecondSignalStopsWhileDrainIsBlocked(t *testing.T) {
	core := newDrainTestCore(t, drainTestRunA)
	adapter := newDrainTestAdapter()
	output := &drainTestLog{}
	node := newDrainTestNode(t, core, adapter, output)
	drain := newStuckDrain(t)
	node.drainWorker = drain.run
	budget := 20 * time.Second
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.serve(signals, budget) }()

	waitForDrainTestRun(t, adapter, drainTestRunA.RunID)
	signals <- os.Interrupt
	waitForStuckDrain(t, drain)
	signaled := time.Now()
	signals <- os.Interrupt
	eventuallyForTest(t, 5*time.Second, func() bool {
		return strings.Contains(output.String(), "second signal")
	}, "second signal handling")
	close(adapter.release)
	if err := waitForServe(t, served, budget); err != nil {
		t.Fatalf("forced stop with a blocked drain returned %v; logs:\n%s", err, output.String())
	}
	if elapsed := time.Since(signaled); elapsed > 5*time.Second {
		t.Fatalf("second signal took %s to stop while the drain was blocked", elapsed)
	}
	select {
	case <-drain.release:
		t.Fatal("Serve waited for the stuck drain")
	default:
	}
	assertDrainTestStoreClosed(t, node)
}

// A real SIGTERM to the built binary drains the in-flight Run and exits 0.
func TestCompiledNodeDrainsOnSIGTERM(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("SIGTERM delivery to a child process is tested on macOS and Linux")
	}
	if testing.Short() {
		t.Skip("builds the Agent Node binary")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "openlinker-agent-node")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/openlinker-agent-node")
	build.Dir = repositoryRoot
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Agent Node: %v\n%s", err, output)
	}

	core := newDrainTestCore(t, drainTestRunA, drainTestRunB)
	var backendCalls atomic.Int32
	backendStarted := make(chan struct{}, 1)
	releaseBackend := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBackend) }) }
	var backendCanceled atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the request before blocking so the client is never stuck
		// writing and Close can always finish.
		_, _ = io.Copy(io.Discard, r.Body)
		backendCalls.Add(1)
		select {
		case backendStarted <- struct{}{}:
		default:
		}
		select {
		case <-releaseBackend:
		case <-r.Context().Done():
			backendCanceled.Store(true)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"ok": true}})
	}))
	// Cleanups run last-in first-out: the child (registered below) is killed
	// and joined first, then the backend is released and closed, even when an
	// assertion fails early.
	t.Cleanup(func() {
		release()
		backend.Close()
	})

	home := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "runtime")
	command := exec.Command(binary)
	command.Env = []string{
		"HOME=" + home, "PATH=" + os.Getenv("PATH"),
		"OPENLINKER_URL=" + core.server.URL,
		"OPENLINKER_AGENT_ID=" + drainTestAgentID,
		"OPENLINKER_AGENT_TOKEN=synthetic-sigterm-test-token",
		"OPENLINKER_AGENT_NODE_DATA_DIR=" + dataDir,
		"OPENLINKER_AGENT_NODE_TRANSPORT=pull",
		"OPENLINKER_AGENT_NODE_ADAPTER=http",
		"OPENLINKER_AGENT_NODE_HTTP_URL=" + backend.URL,
		"OPENLINKER_AGENT_NODE_HELPER=false",
		"OPENLINKER_AGENT_NODE_CLAIM_WAIT_SECONDS=1",
		"OPENLINKER_AGENT_NODE_COMMAND_WAIT_SECONDS=1",
		"OPENLINKER_AGENT_NODE_RETRY_MIN_MS=10",
		"OPENLINKER_AGENT_NODE_RETRY_MAX_MS=50",
	}
	output := &drainTestLog{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// One goroutine owns Wait; everything else observes waitDone/waitErr.
	waitDone := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = command.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		select {
		case <-waitDone:
		default:
			_ = command.Process.Kill()
			<-waitDone
		}
	})

	select {
	case <-backendStarted:
	case <-waitDone:
		t.Fatalf("Agent Node exited before running: %v\n%s", waitErr, output.String())
	case <-time.After(15 * time.Second):
		t.Fatalf("Agent Node did not start the Run\n%s", output.String())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-core.drained:
	case <-waitDone:
		t.Fatalf("SIGTERM exited before draining: %v\n%s", waitErr, output.String())
	case <-time.After(5 * time.Second):
		t.Fatalf("SIGTERM did not drain the Runtime Session\n%s", output.String())
	}
	select {
	case <-waitDone:
		t.Fatalf("Agent Node exited while the Run was still in flight: %v\n%s", waitErr, output.String())
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case <-waitDone:
		if waitErr != nil {
			t.Fatalf("Agent Node exit = %v, want 0\n%s", waitErr, output.String())
		}
	case <-time.After(ProcessShutdownTimeout + 5*time.Second):
		t.Fatalf("Agent Node did not exit after SIGTERM\n%s", output.String())
	}
	logs := output.String()
	if strings.Contains(logs, "timed out") || strings.Contains(logs, "drain did not complete") {
		t.Fatalf("SIGTERM shutdown reported a failure:\n%s", logs)
	}
	snapshot := core.snapshot()
	if len(snapshot.results) != 1 || snapshot.results[0].AttemptIdentity.RunID != drainTestRunA.RunID ||
		len(snapshot.claimed) != 1 || backendCalls.Load() != 1 || backendCanceled.Load() {
		t.Fatalf("results=%d claimed=%v backend calls=%d canceled=%v\n%s",
			len(snapshot.results), snapshot.claimed, backendCalls.Load(), backendCanceled.Load(), logs)
	}
	if len(snapshot.closes) == 0 || snapshot.closes[len(snapshot.closes)-1].Status != "closed" {
		t.Fatalf("Runtime Session was not closed: %#v", snapshot.closes)
	}
	store, err := openlinker.OpenFileRuntimeStore(dataDir)
	if err != nil {
		t.Fatalf("SDK store was not closed by the exiting process: %v", err)
	}
	_ = store.Close()
}
