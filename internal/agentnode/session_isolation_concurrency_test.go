package agentnode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// One configured Node and its real Runtime handler share one cached adapter.
// No per-session Node process or fake sandbox backend is involved. Core's
// transport/scheduling is outside this test; the clients are protocol peers.
func TestNativeIsolationOneNodeConcurrentConversations(t *testing.T) {
	runtimeBin := os.Getenv("OPENLINKER_TEST_NATIVE_SANDBOX_BIN")
	if runtimeBin == "" {
		t.Skip("set OPENLINKER_TEST_NATIVE_SANDBOX_BIN for real OS acceptance")
	}
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			values := isolationValues(t, name)
			bin := filepath.Join(t.TempDir(), name)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			build := exec.CommandContext(ctx, "go", "build", "-o", bin, "../../pkg/adapters/testdata/session-client")
			build.Env = append(os.Environ(), "GOWORK=off")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build protocol peer: %v %s", err, out)
			}
			values["OPENLINKER_AGENT_NODE_"+strings.ToUpper(name)+"_BIN"] = bin
			values["OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN"] = runtimeBin
			values["OPENLINKER_AGENT_NODE_CAPACITY"] = "2"
			node, err := NewFromLookup(func(k string) string { return values[k] })
			if err != nil {
				t.Fatal(err)
			}
			if node.Capacity != 2 {
				t.Fatal("configured concurrency lost")
			}
			env := []string{"PATH=" + os.Getenv("PATH"), "CODEX_API_KEY=fixture-key", "ANTHROPIC_API_KEY=fixture-key"}
			switch adapter := node.Adapter.(type) {
			case *CodexAdapter:
				adapter.Env = env
			case *NativeAdapter:
				adapter.Config.Env = env
			default:
				t.Fatalf("unexpected adapter %T", adapter)
			}
			handler := runtimeAdapterHandler{node: node}
			assignment := func(session, run string, spec JSONMap) openlinker.RuntimeContext {
				raw, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				return openlinker.RuntimeContext{
					AgentID: "agent-1", RunID: run,
					Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "principal-1"},
					Input:     "fixture_spec=" + base64.StdEncoding.EncodeToString(raw),
					Metadata: openlinker.RuntimeJSONMap{"conversation": map[string]any{
						"source": "core", "session_key": session, "current_run_id": run,
					}},
				}
			}
			invoke := func(session, run string, spec JSONMap) openlinker.RuntimeResult {
				result, err := handler.Handle(ctx, assignment(session, run, spec))
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			report := func(result openlinker.RuntimeResult) JSONMap {
				if result.Status != "success" || result.Error != nil {
					t.Fatalf("Run failed: %#v", result)
				}
				output, ok := result.Output.(map[string]any)
				if !ok {
					if value, isMap := result.Output.(JSONMap); isMap {
						output = map[string]any(value)
					}
				}
				if output["session_isolation"] != "native" {
					t.Fatal("missing sandbox execution evidence")
				}
				var decoded JSONMap
				summary, _ := output["summary"].(string)
				if err := json.Unmarshal([]byte(summary), &decoded); err != nil {
					t.Fatal(err)
				}
				return decoded
			}
			a := report(invoke("a", "a-1", JSONMap{"Memory": "private-A"}))
			b := report(invoke("b", "b-1", JSONMap{"Memory": "private-B"}))
			if a["home"] == b["home"] || a["id"] == b["id"] || a["previous"] != "" || b["previous"] != "" {
				t.Fatal("new sessions shared workspace or native history")
			}
			workspace := func(r JSONMap) string {
				return filepath.Join(filepath.Dir(r["home"].(string)), "workspace")
			}
			adenied := []string{filepath.Join(workspace(b), "memory"), filepath.Join(filepath.Dir(b["home"].(string)), name, "native-id")}
			bdenied := []string{filepath.Join(workspace(a), "memory"), filepath.Join(filepath.Dir(a["home"].(string)), name, "native-id")}
			actx, cancelA := context.WithCancel(ctx)
			bctx, cancelB := context.WithCancel(ctx)
			var wg sync.WaitGroup
			defer func() { cancelA(); cancelB(); wg.Wait() }()
			type outcome struct {
				result openlinker.RuntimeResult
				err    error
			}
			start := func(runCtx context.Context, run openlinker.RuntimeContext) <-chan outcome {
				ch := make(chan outcome, 1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := handler.Handle(runCtx, run)
					ch <- outcome{result, err}
				}()
				return ch
			}
			adone := start(actx, assignment("a", "a-2", JSONMap{"Wait": true, "Denied": adenied}))
			bdone := start(bctx, assignment("b", "b-2", JSONMap{"Wait": true, "Denied": bdenied}))
			deadline := time.Now().Add(20 * time.Second)
			for {
				_, aerr := os.Stat(filepath.Join(workspace(a), "ready"))
				_, berr := os.Stat(filepath.Join(workspace(b), "ready"))
				if aerr == nil && berr == nil {
					break
				}
				select {
				case result := <-adone:
					t.Fatalf("A ended before both sandboxes were running: %#v", result)
				case result := <-bdone:
					t.Fatalf("B ended before both sandboxes were running: %#v", result)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("same Node did not run both session sandboxes concurrently")
				}
				time.Sleep(20 * time.Millisecond)
			}
			busy := invoke("a", "a-overlap", JSONMap{})
			if busy.Status != "failed" || busy.Error == nil || !strings.Contains(busy.Error.Message, "native session is busy") {
				t.Fatalf("overlapping write to the same session was accepted: %#v", busy)
			}
			cancelA()
			stopped := <-adone
			if stopped.err != nil || stopped.result.Error == nil || stopped.result.Error.Code != "ADAPTER_CANCELED" {
				t.Fatalf("A cancellation lost: %#v", stopped)
			}
			select {
			case result := <-bdone:
				t.Fatalf("canceling A also ended B: %#v", result)
			default:
			}
			if err := os.WriteFile(filepath.Join(workspace(b), "release"), []byte("continue"), 0o600); err != nil {
				t.Fatal(err)
			}
			finished := <-bdone
			if finished.err != nil {
				t.Fatal(finished.err)
			}
			report(finished.result)
			for _, session := range []struct {
				key, memory string
				first       JSONMap
				denied      []string
			}{{"a", "private-A", a, adenied}, {"b", "private-B", b, bdenied}} {
				next := report(invoke(session.key, session.key+"-3", JSONMap{"Denied": session.denied}))
				if next["id"] != session.first["id"] || next["previous"] != session.memory {
					t.Fatal("same Node lost session identity or crossed file histories")
				}
				for _, path := range session.denied {
					if next["blocked"].(map[string]any)[path] != true {
						t.Fatalf("session %s read another session's data", session.key)
					}
				}
			}
		})
	}
}
