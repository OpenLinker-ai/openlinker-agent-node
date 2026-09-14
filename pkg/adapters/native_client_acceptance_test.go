package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Actual installed clients execute deterministic tool calls from a loopback
// fixture. No live model, personal account or real credential is used.
func TestNativeHostAuthInstalledClients(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			bin := os.Getenv("OPENLINKER_TEST_NATIVE_" + strings.ToUpper(provider) + "_BIN")
			if bin == "" {
				t.Skip("set both official client paths for real tool-boundary acceptance")
			}
			c := isolationConfig(t, provider)
			if provider == "claude" {
				// Direct-path file-tool checks are an explicit opt-in. The
				// default Bash-only policy has separate link-matrix coverage.
				c.AllowedTools = []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"}
			}
			c.Bin = bin
			diagnostics := filepath.Join(t.TempDir(), "client-stderr")
			wrapper := filepath.Join(t.TempDir(), provider)
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\n"+jsonString(bin)+" \"$@\" 2>"+jsonString(diagnostics)+" | tee "+jsonString(diagnostics+".rpc")+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			c.Bin = wrapper
			if provider == "claude" {
				c.Bin = bin
			}
			c.Timeout = 30 * time.Second
			c.Model = "gpt-5.4"
			if provider == "claude" {
				c.Model = "claude-sonnet-4-6"
			}
			home := t.TempDir()
			conf := filepath.Join(home, "."+provider)
			if err := os.Mkdir(conf, 0700); err != nil {
				t.Fatal(err)
			}
			canary := "synthetic-cached-auth-canary"
			parentCanary := "synthetic-parent-env-canary"
			notifyMarker := filepath.Join(home, "unexpected-notify")
			other := filepath.Join(t.TempDir(), "other-session-secret")
			mustWrite := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mustWrite(other, canary)
			keychainProbe := syntheticKeychainProbe(t)
			var mu sync.Mutex
			calls, authed := 0, 0
			var bodies []string
			var tools []string
			activeWorkspace := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if r.Method != "POST" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"models":[]}`)
					return
				}
				if strings.Contains(r.URL.Path, "count_tokens") {
					fmt.Fprint(w, `{"input_tokens":20}`)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				calls++
				bodies = append(bodies, string(raw))
				auth := r.Header.Get("Authorization") + r.Header.Get("X-Api-Key")
				if strings.Contains(auth, canary) || strings.Contains(auth, parentCanary) {
					authed++
				}
				var request map[string]any
				_ = json.Unmarshal(raw, &request)
				if list, ok := request["tools"].([]any); ok {
					for _, item := range list {
						if m, ok := item.(map[string]any); ok {
							if n, ok := m["name"].(string); ok {
								tools = appendUniqueString(tools, n)
							}
						}
					}
				}
				// Exactly one Bash/exec tool per turn; the provider sends its result back.
				toolDone := strings.Contains(string(raw), "fixture_exec_")
				// A resumed turn contains the previous request too. The final answer is
				// sufficient for A-B-A routing; only the first turn needs the OS probes.
				id := fmt.Sprintf("fixture_exec_%d", calls)
				command := "/bin/bash ./probe.sh"
				if provider == "claude" {
					content := map[string]any{"type": "text", "text": "fixture complete"}
					if !toolDone {
						content = map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command, "description": "Synthetic tool boundary probe"}}
					}
					if toolDone && !strings.Contains(string(raw), "fixture_read") {
						content = map[string]any{"type": "tool_use", "id": "fixture_read", "name": "Read", "input": map[string]any{"file_path": filepath.Join(conf, ".credentials.json")}}
					} else if toolDone && !strings.Contains(string(raw), "fixture_write") {
						content = map[string]any{"type": "tool_use", "id": "fixture_write", "name": "Write", "input": map[string]any{"file_path": filepath.Join(activeWorkspace, "file-tool.txt"), "content": "file-tool-write"}}
					} else if toolDone && !strings.Contains(string(raw), "fixture_edit") {
						content = map[string]any{"type": "tool_use", "id": "fixture_edit", "name": "Edit", "input": map[string]any{"file_path": filepath.Join(activeWorkspace, "file-tool.txt"), "old_string": "file-tool-write", "new_string": "file-tool-edited"}}
					} else if toolDone && !strings.Contains(string(raw), "fixture_outside_write") {
						content = map[string]any{"type": "tool_use", "id": "fixture_outside_write", "name": "Write", "input": map[string]any{"file_path": filepath.Join(conf, "tool-write"), "content": "forbidden"}}
					}
					claudeFixtureEvents(w, calls, content, content["type"] == "tool_use")
				} else {
					content := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "fixture complete", "annotations": []any{}}}}
					if !toolDone {
						content = map[string]any{"id": id, "type": "function_call", "call_id": id, "name": "exec_command", "arguments": jsonObject(map[string]any{"cmd": command, "yield_time_ms": 1000, "max_output_tokens": 2000})}
					}
					w.Header().Set("Content-Type", "text/event-stream")
					emit := func(kind string, v any) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, jsonObject(v)) }
					emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("response_", calls)}})
					emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": content})
					emit("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("response_", calls), "status": "completed", "output": []any{content}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 10, "total_tokens": 30}}})
				}
			}))
			defer server.Close()
			c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "NO_PROXY=127.0.0.1,localhost,::1", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "OPENLINKER_AGENT_TOKEN=never-forward-this", "NODE_OPTIONS=never-forward-this"}
			if provider == "codex" {
				c.Env = append(c.Env, "CODEX_HOME="+conf)
				mustWrite(filepath.Join(conf, "auth.json"), jsonObject(map[string]any{"OPENAI_API_KEY": canary}))
				mustWrite(filepath.Join(conf, "config.toml"), "model_provider=\"fixture\"\nsandbox_mode=\"danger-full-access\"\nnotify=[\"/usr/bin/touch\","+jsonString(notifyMarker)+"]\n[features]\nview_image=true\n[model_providers.fixture]\nname=\"fixture\"\nbase_url="+jsonString(server.URL)+"\nwire_api=\"responses\"\nrequires_openai_auth=true\nsupports_websockets=false\n")
			} else {
				c.Env = append(c.Env, "CLAUDE_CONFIG_DIR="+conf, "ANTHROPIC_BASE_URL="+server.URL)
				mustWrite(filepath.Join(conf, ".credentials.json"), jsonObject(map[string]any{"claudeAiOauth": map[string]any{"accessToken": canary, "refreshToken": "synthetic-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}, "subscriptionType": "pro", "rateLimitTier": "default_claude_pro"}}))
				mustWrite(filepath.Join(home, ".claude.json"), `{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"00000000-0000-4000-8000-000000000001","organizationUuid":"00000000-0000-4000-8000-000000000002","emailAddress":"fixture@example.invalid"}}`)
			}
			lockPath, err := hostAuthLockPath(c)
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(lockPath, "")
			lockInfo, err := os.Stat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			stateCanary := filepath.Join(filepath.Dir(lockPath), "state-canary")
			stateWrite := filepath.Join(filepath.Dir(lockPath), "unexpected-tool-write")
			mustWrite(stateCanary, canary)
			// Prepare through the production scope allocator, then release the lock so
			// the production Provider.Run takes exactly the same session directories.
			workspaces := map[string]string{}
			for _, key := range []string{"a", "b"} {
				prepared, close, err := prepareIsolatedSession(context.Background(), c, isolationRun(key))
				if err != nil {
					t.Fatal(err)
				}
				workspaces[key] = prepared.Workspace
				// Execute an opaque script: client permission heuristics cannot satisfy
				// this test merely by refusing an absolute path before OS execution.
				script := "printf allowed > own.txt\nif printf forbidden > " + jsonString(filepath.Join(conf, "tool-write")) + "; then echo AUTH_WRITE_SUCCEEDED; fi\ncat escape\ncat " + jsonString(other) + "\ncat " + jsonString(filepath.Join(conf, "auth.json")) + "\ncat " + jsonString(filepath.Join(conf, ".credentials.json")) + "\n/bin/ps eww -p $PPID\nfor e in /proc/[0-9]*/environ; do grep -aoh synthetic-parent-env-canary \"$e\" 2>/dev/null; done\nenv\n/usr/bin/curl -sSf --max-time 2 " + jsonString(server.URL+"/tool-egress") + " >/dev/null && echo NETWORK_ESCAPE\n"
				script += "cat " + jsonString(stateCanary) + "\nrm -f " + jsonString(lockPath) + "\nprintf forbidden > " + jsonString(stateWrite) + "\n"
				mustWrite(filepath.Join(prepared.Workspace, "probe.sh"), script+keychainProbe)
				if err := os.Symlink(other, filepath.Join(prepared.Workspace, "escape")); err != nil {
					t.Fatal(err)
				}
				mustWrite(filepath.Join(prepared.Workspace, "private-memory"), canary)
				if err := close(); err != nil {
					t.Fatal(err)
				}
			}
			for key, workspace := range workspaces {
				peer := "a"
				if key == "a" {
					peer = "b"
				}
				path := filepath.Join(workspace, "probe.sh")
				raw, _ := os.ReadFile(path)
				mustWrite(path, string(raw)+"cat "+jsonString(filepath.Join(workspaces[peer], "private-memory"))+"\n")
			}
			p, err := NewProvider(c)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			results := []map[string]any{}
			for i, key := range []string{"a", "b", "a"} {
				mu.Lock()
				activeWorkspace = workspaces[key]
				mu.Unlock()
				if i == 1 {
					keyName := "CODEX_API_KEY"
					if provider == "claude" {
						keyName = "ANTHROPIC_API_KEY"
					}
					c.Env = append(c.Env, keyName+"="+parentCanary)
					p, err = NewProvider(c)
					if err != nil {
						t.Fatal(err)
					}
				}
				run := isolationRun(key)
				run.RunID = fmt.Sprint("run-", i)
				run.Conversation.CurrentRunID = run.RunID
				run.Input = "Use the supplied fixture tool call."
				result, err := p.Run(ctx, run)
				if err != nil {
					raw, _ := os.ReadFile(diagnostics)
					trace, _ := os.ReadFile(diagnostics + ".rpc")
					if len(trace) > 5000 {
						trace = trace[len(trace)-5000:]
					}
					raw = append(raw, trace...)
					t.Fatalf("run %s: %v; exposed tool names: %v; calls=%d; diagnostic=%s", key, err, tools, calls, raw)
				}
				after, statErr := os.Stat(lockPath)
				if statErr != nil || !os.SameFile(lockInfo, after) {
					t.Fatal("tool removed or replaced coordination lock", statErr)
				}
				if _, err := os.Stat(stateWrite); !os.IsNotExist(err) {
					t.Fatal("tool wrote host coordination state", err)
				}
				if raw, err := os.ReadFile(stateCanary); err != nil || string(raw) != canary {
					t.Fatal("tool changed coordination canary", err)
				}
				output := result.Output.(map[string]any)
				results = append(results, output)
			}
			// Linux may allow creating the same pathname in an empty private
			// tmpfs overlay. Only a mutation visible to the host is an escape.
			if _, err := os.Stat(filepath.Join(conf, "tool-write")); !os.IsNotExist(err) {
				t.Fatal("tool modified the real host authentication directory", err)
			}
			for _, key := range []string{"a", "b"} {
				if provider == "claude" {
					raw, err := os.ReadFile(filepath.Join(workspaces[key], "file-tool.txt"))
					if err != nil || string(raw) != "file-tool-edited" {
						t.Fatalf("scoped file tools failed for %s: %v %q", key, err, raw)
					}
				}
				raw, err := os.ReadFile(filepath.Join(workspaces[key], "own.txt"))
				if err != nil || string(raw) != "allowed" {
					detail := ""
					for _, body := range bodies {
						if i := strings.Index(body, `"type":"function_call_output"`); i >= 0 {
							detail = body[max(0, i-500):min(len(body), i+2200)]
						}
					}
					if provider == "claude" {
						for _, body := range bodies {
							if i := strings.Index(body, `"type":"tool_result"`); i >= 0 {
								detail = body[max(0, i-100):min(len(body), i+2500)]
							}
						}
					}
					raw, _ := os.ReadFile(diagnostics)
					t.Fatalf("%s did not execute its tool successfully (%v); tools=%v; result=%s; stderr=%s", key, err, tools, detail, raw)
				}
			}
			if results[0][provider+"_session_resumed"] != false || results[1][provider+"_session_resumed"] != false || results[2][provider+"_session_resumed"] != true {
				t.Fatal("A-B-A did not resume exactly A")
			}
			mu.Lock()
			defer mu.Unlock()
			if authed == 0 || authed != calls {
				t.Fatalf("cached authentication not reused: %d/%d", authed, calls)
			}
			if provider == "codex" {
				for _, tool := range tools {
					if tool == "view_image" {
						t.Fatal("host-configured view_image remained model-visible")
					}
				}
				if _, err := os.Stat(notifyMarker); !os.IsNotExist(err) {
					t.Fatal("host notifier executed", err)
				}
			}
			for _, body := range bodies {
				for _, secret := range []string{canary, parentCanary, "never-forward-this", "NETWORK_ESCAPE", "synthetic-keychain-canary"} {
					if strings.Contains(body, secret) {
						i := strings.Index(body, secret)
						a := max(0, i-300)
						b := min(len(body), i+len(secret)+150)
						t.Fatalf("tool output leaked a synthetic secret (%s): %s", secret, body[a:b])
					}
				}
			}
			t.Logf("%s: cached auth reused in %d fixture requests, A-B-A resumed, tools wrote only allowed files and returned no synthetic credential", provider, calls)
		})
	}
}

func claudeFixtureEvents(w http.ResponseWriter, n int, b map[string]any, tool bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(kind string, v any) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, jsonObject(v)) }
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprint("msg_fixture_", n), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 1}}})
	start := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": b["text"]}
	stop := "end_turn"
	if tool {
		start = map[string]any{"type": "tool_use", "id": b["id"], "name": b["name"], "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": jsonObject(b["input"])}
		stop = "tool_use"
	}
	emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": start})
	emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
	emit("message_stop", map[string]any{"type": "message_stop"})
}
