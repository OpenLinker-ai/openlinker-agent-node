package agentnode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// A real SIGTERM to the built binary stops the Node through the SDK Worker's
// Stop: the run in flight finishes inside the SDK grace, the store is closed
// and the process exits 0.
func TestCompiledNodeStopsCleanlyOnSIGTERM(t *testing.T) {
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

	core := newShutdownTestCore(t, shutdownTestRunA, shutdownTestRunB)
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
		"OPENLINKER_AGENT_ID=" + shutdownTestAgentID,
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
	output := &shutdownTestLog{}
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
	// The SDK Worker's Stop keeps the run in flight inside its shutdown grace.
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
	if strings.Contains(logs, "timed out") {
		t.Fatalf("SIGTERM shutdown reported a failure:\n%s", logs)
	}
	snapshot := core.snapshot()
	if len(snapshot.drains) != 0 || len(snapshot.claimed) != 1 || backendCalls.Load() != 1 || backendCanceled.Load() {
		t.Fatalf("drains=%d claimed=%v backend calls=%d canceled=%v\n%s",
			len(snapshot.drains), snapshot.claimed, backendCalls.Load(), backendCanceled.Load(), logs)
	}
	if len(snapshot.closes) == 0 || snapshot.closes[len(snapshot.closes)-1].Status != "closed" {
		t.Fatalf("Runtime Session was not closed: %#v", snapshot.closes)
	}
	store, err := openlinker.OpenFileRuntimeStore(dataDir)
	if err != nil {
		t.Fatalf("SDK store was not closed by the exiting process: %v", err)
	}
	// The completed Result reached Core or stays in the durable spool for the
	// next start (both when the exit raced its ACK); it is never lost.
	pending, pendingErr := store.PendingResult(shutdownTestRunA.AttemptID)
	_ = store.Close()
	if pendingErr != nil && !errors.Is(pendingErr, openlinker.ErrSpoolRecordNotFound) {
		t.Fatal(pendingErr)
	}
	spooled := pendingErr == nil
	if len(snapshot.results) == 0 && !spooled {
		t.Fatal("the completed Result was neither delivered nor spooled")
	}
	for _, result := range snapshot.results {
		if result.AttemptIdentity.RunID != shutdownTestRunA.RunID || (spooled && pending.ResultID != result.ResultID) {
			t.Fatalf("unexpected Result delivery: delivered=%v spooled=%v", snapshot.results, pending.ResultID)
		}
	}
}
