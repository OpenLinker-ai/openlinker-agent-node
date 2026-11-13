package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexturn"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type continuationRun struct {
	bin, dir string
	result   openlinker.RuntimeResult
	err      error
	statuses []string
	elapsed  time.Duration
}

// runContinuation exercises the production CodexProvider.Run entry against the
// fake app-server. sessionKey enables session reuse.
func runContinuation(t *testing.T, ctx context.Context, dir, scenario string, enabled bool, sessionKey string) continuationRun {
	t.Helper()
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, scenario)
	config := ProviderConfig{
		Bin: bin, Workspace: dir, Timeout: 10 * time.Second,
		CodexContinueOnMaxMessages: enabled,
	}
	run := RunContext{Input: "geo audit"}
	if sessionKey != "" {
		config.SessionReuse, config.SessionStore = true, filepath.Join(dir, "sessions.json")
		run.Conversation = &ConversationContext{SessionKey: sessionKey}
	}
	var mu sync.Mutex
	out := continuationRun{bin: bin, dir: dir}
	run.Emit = func(kind string, value any) error {
		if data, ok := value.(map[string]any); ok && kind == "run.status.changed" {
			mu.Lock()
			out.statuses = append(out.statuses, data["status"].(string))
			mu.Unlock()
			if data["status"] == "provider_continuing" {
				want := map[string]any{"provider": "codex", "status": "provider_continuing", "phase": "continuing",
					"provider_error_kind": "incomplete_response", "provider_error_reason": "max_messages"}
				if !reflect.DeepEqual(data, want) {
					t.Errorf("unsafe continuation event: %v", data)
				}
			}
		}
		return nil
	}
	start := time.Now()
	out.result, out.err = (CodexProvider{Config: config}).Run(ctx, sessionTestRun(run))
	out.elapsed = time.Since(start)
	return out
}

func (r continuationRun) trace(t *testing.T, suffix string) string {
	t.Helper()
	raw, err := os.ReadFile(r.bin + ".trace" + suffix)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func (r continuationRun) output(t *testing.T) map[string]any {
	t.Helper()
	output, ok := r.result.Output.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output %T", r.result.Output)
	}
	return output
}

func (r continuationRun) interrupted(t *testing.T) []string {
	t.Helper()
	var turns []string
	for _, line := range strings.Split(strings.TrimSpace(r.trace(t, ".interrupts")), "\n") {
		if line == "" {
			continue
		}
		var params codexrpc.TurnInterruptParams
		if json.Unmarshal([]byte(line), &params) != nil || params.ThreadID != fixtureThread {
			t.Fatalf("interrupt left the run thread: %s", line)
		}
		turns = append(turns, params.TurnID)
	}
	return turns
}

func isOriginalLimit(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, codexturn.ErrResponseMessageLimit) || err.Error() != "Codex failed: "+codexturn.ErrResponseMessageLimit.Error() {
		t.Fatalf("expected the original message-limit error, got %v", err)
	}
}

func TestCodexMaxMessagesContinuationSucceedsOnSameThread(t *testing.T) {
	for _, sessionKey := range []string{"", "conversation"} {
		t.Run("reuse="+map[bool]string{true: "on", false: "off"}[sessionKey != ""], func(t *testing.T) {
			r := runContinuation(t, context.Background(), t.TempDir(), "message-limit-continue-success", true, sessionKey)
			if r.err != nil {
				t.Fatal(r.err)
			}
			output := r.output(t)
			if output["summary"] != "continued answer" || output["codex_max_messages_continued"] != true {
				t.Fatalf("continuation result was not reported: %v", output)
			}
			if !reflect.DeepEqual(r.statuses, []string{"provider_continuing"}) {
				t.Fatalf("unexpected status events: %v", r.statuses)
			}
			if got := r.trace(t, ".continuation.prompt"); got != codexMessageLimitContinuation {
				t.Fatalf("continuation turn input = %q", got)
			}
			// Only the stopped first turn is interrupted; the task is not replayed
			// on a new thread, a resumed thread or a relaunched app-server.
			if got := r.interrupted(t); !reflect.DeepEqual(got, []string{fixtureTurn}) {
				t.Fatalf("interrupted turns = %v", got)
			}
			requests := r.trace(t, ".requests")
			if strings.Count(requests, " thread/start\n") != 1 || strings.Count(requests, " thread/resume\n") != 0 ||
				strings.Count(requests, " initialize\n") != 1 {
				t.Fatalf("continuation replayed the task: %s", requests)
			}
			if strings.Count(r.trace(t, ".args"), "app-server") != 1 {
				t.Fatal("app-server was relaunched")
			}
			if sessionKey == "" {
				if _, ok := output["codex_session_reuse"]; ok {
					t.Fatal("session metadata without reuse")
				}
				return
			}
			// The same thread is persisted; an explicit next Run resumes it and
			// is not itself marked as continued.
			next := runContinuation(t, context.Background(), r.dir, "message-limit-continue-success", true, sessionKey)
			if next.err != nil {
				t.Fatal(next.err)
			}
			nextOutput := next.output(t)
			if nextOutput["codex_session_resumed"] != true || nextOutput["codex_max_messages_continued"] != nil {
				t.Fatalf("follow-up did not resume the continued thread: %v", nextOutput)
			}
			if !strings.Contains(next.trace(t, ".requests"), `"threadId":"`+fixtureThread+`"`) {
				t.Fatal("follow-up resumed a different thread")
			}
		})
	}
}

func TestCodexMaxMessagesContinuationFailureReturnsOriginalError(t *testing.T) {
	for _, scenario := range []string{"message-limit-continue-limit", "message-limit-continue-failed"} {
		t.Run(scenario, func(t *testing.T) {
			r := runContinuation(t, context.Background(), t.TempDir(), scenario, true, "")
			isOriginalLimit(t, r.err)
			if strings.Contains(r.err.Error(), "private") {
				t.Fatal("raw upstream diagnostics leaked")
			}
			if !reflect.DeepEqual(r.statuses, []string{"provider_continuing", "provider_failed"}) {
				t.Fatalf("unexpected status events: %v", r.statuses)
			}
			want := []string{fixtureTurn}
			if scenario == "message-limit-continue-limit" {
				want = append(want, providertest.FixtureContinuationTurn)
			}
			if got := r.interrupted(t); !reflect.DeepEqual(got, want) {
				t.Fatalf("interrupted turns = %v, want %v", got, want)
			}
		})
	}
}

func TestCodexMaxMessagesContinuationDisabledKeepsSingleTurn(t *testing.T) {
	r := runContinuation(t, context.Background(), t.TempDir(), "message-limit-continue-success", false, "")
	isOriginalLimit(t, r.err)
	if !reflect.DeepEqual(r.statuses, []string{"provider_failed"}) {
		t.Fatalf("unexpected status events: %v", r.statuses)
	}
	if r.trace(t, ".continuation.prompt") != "" {
		t.Fatal("continuation turn started while disabled")
	}
}

func TestCodexMaxMessagesContinuationRequiresSettledTurn(t *testing.T) {
	// The fake app-server never confirms the stopped turn ended. A second turn
	// must not start on a possibly active thread.
	r := runContinuation(t, context.Background(), t.TempDir(), "message-limit-ignore", true, "")
	isOriginalLimit(t, r.err)
	if !reflect.DeepEqual(r.statuses, []string{"provider_failed"}) {
		t.Fatalf("unexpected status events: %v", r.statuses)
	}
	if r.trace(t, ".continuation.prompt") != "" {
		t.Fatal("continuation started before the stopped turn ended")
	}
	if r.elapsed > 5*time.Second {
		t.Fatalf("unsettled turn exceeded its bound: %s", r.elapsed)
	}
}

func TestCodexMaxMessagesContinuationCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		marker := filepath.Join(dir, "codex.trace.continuation.started")
		for ctx.Err() == nil {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	r := runContinuation(t, ctx, dir, "message-limit-continue-cancel", true, "")
	if !errors.Is(r.err, context.Canceled) || errors.Is(r.err, codexturn.ErrResponseMessageLimit) {
		t.Fatalf("cancellation during continuation was not reported: %v", r.err)
	}
	if got := r.interrupted(t); !reflect.DeepEqual(got, []string{fixtureTurn, providertest.FixtureContinuationTurn}) {
		t.Fatalf("cancellation did not interrupt the continuation turn: %v", got)
	}
	if !reflect.DeepEqual(r.statuses, []string{"provider_continuing"}) {
		t.Fatalf("unexpected status events: %v", r.statuses)
	}
}
