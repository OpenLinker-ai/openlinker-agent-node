package agentnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestHTTPAdapterEnvelopeIncludesHelper(t *testing.T) {
	var received AdapterEnvelope
	server := testJSONServer(t, func(r testRequest) (int, any) {
		if err := json.Unmarshal(r.Body, &received); err != nil {
			t.Fatal(err)
		}
		return httpOK, JSONMap{"output": JSONMap{"ok": true, "run_id": received.RunID}}
	})
	defer server.Close()

	adapter := HTTPAdapter{URL: server.URL + "/run"}
	output, err := adapter.Run(context.Background(), JSONMap{"q": "http"}, RunContext{
		RunID:    "run-http",
		Metadata: JSONMap{"source": "test"},
		A2A:      JSONMap{"current_run_id": "run-http"},
		Helper:   testHelperInfo(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.AgentNode == nil || received.AgentNode.Helper.Token != "olh_test" {
		t.Fatalf("helper not passed: %#v", received.AgentNode)
	}
	out := output.(map[string]any)
	if out["run_id"] != "run-http" {
		t.Fatalf("output = %#v", out)
	}
}

func TestCommandAdapterPassesHelper(t *testing.T) {
	if os.Getenv("AGENTNODE_HELPER_PROCESS") == "1" {
		var envelope AdapterEnvelope
		if err := json.NewDecoder(os.Stdin).Decode(&envelope); err != nil {
			panic(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(JSONMap{"output": JSONMap{
			"run_id":                envelope.RunID,
			"helper_url":            os.Getenv("OPENLINKER_AGENT_NODE_HELPER_URL"),
			"helper_token":          os.Getenv("OPENLINKER_AGENT_NODE_HELPER_TOKEN"),
			"call_agent_url":        os.Getenv("OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL"),
			"envelope_helper_token": envelope.AgentNode.Helper.Token,
		}})
		os.Exit(0)
	}

	adapter := CommandAdapter{
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestCommandAdapterPassesHelper"},
		Env:          []string{"AGENTNODE_HELPER_PROCESS=1"},
		EnvAllowlist: []string{"AGENTNODE_HELPER_PROCESS"},
		Timeout:      testTimeout,
	}
	output, err := adapter.Run(context.Background(), JSONMap{"q": "cli"}, RunContext{
		RunID:  "run-cli",
		Helper: testHelperInfo(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := output.(map[string]any)
	if out["helper_token"] != "olh_test" || out["envelope_helper_token"] != "olh_test" {
		t.Fatalf("helper not passed: %#v", out)
	}
	if out["call_agent_url"] != "http://127.0.0.1:19090/a2a/call" {
		t.Fatalf("call_agent_url = %v", out["call_agent_url"])
	}
}

func TestCommandAdapterRejectsOversizedOutput(t *testing.T) {
	if os.Getenv("AGENTNODE_OVERSIZED_OUTPUT_PROCESS") == "1" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", maxAdapterOutputBytes+1))
		os.Exit(0)
	}

	_, err := (CommandAdapter{
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestCommandAdapterRejectsOversizedOutput"},
		Env:          []string{"AGENTNODE_OVERSIZED_OUTPUT_PROCESS=1"},
		EnvAllowlist: []string{"AGENTNODE_OVERSIZED_OUTPUT_PROCESS"},
		Timeout:      testTimeout,
	}).Run(context.Background(), JSONMap{"q": "oversized"}, RunContext{
		RunID: "run-cli-oversized",
	})
	if err == nil || !strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("oversized output error = %v", err)
	}
}

func TestAdapterErrorBranches(t *testing.T) {
	if _, err := (HTTPAdapter{}).Run(context.Background(), JSONMap{"q": "missing"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "HTTP_URL") {
		t.Fatalf("missing HTTP url error = %v", err)
	}
	if _, err := (HTTPAdapter{URL: "://bad"}).Run(context.Background(), JSONMap{"q": "bad-url"}, RunContext{}); err == nil {
		t.Fatal("expected invalid HTTP URL error")
	}
	if _, err := (HTTPAdapter{
		URL: "https://adapter.example/run",
		HTTPClient: adapterHTTPClient(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}).Run(context.Background(), JSONMap{"q": "dial"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("HTTP client error = %v", err)
	}
	if _, err := (HTTPAdapter{
		URL: "https://adapter.example/run",
		HTTPClient: adapterHTTPClient(func(*http.Request) (*http.Response, error) {
			return adapterHTTPResponse(http.StatusBadGateway, `{"error":"bad gateway"}`), nil
		}),
	}).Run(context.Background(), JSONMap{"q": "bad-status"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("HTTP status error = %v", err)
	}
	output, err := (HTTPAdapter{
		URL: "https://adapter.example/run",
		HTTPClient: adapterHTTPClient(func(*http.Request) (*http.Response, error) {
			return adapterHTTPResponse(http.StatusOK, `{"answer":"ok"}`), nil
		}),
	}).Run(context.Background(), JSONMap{"q": "raw-json"}, RunContext{})
	if err != nil || output.(map[string]any)["answer"] != "ok" {
		t.Fatalf("HTTP raw output = %#v, %v", output, err)
	}

	if _, err := (CommandAdapter{}).Run(context.Background(), JSONMap{"q": "missing"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "COMMAND") {
		t.Fatalf("missing command error = %v", err)
	}
	env := sanitizedEnv([]string{
		"NO_EQUALS",
		"OPENLINKER_AGENT_TOKEN=secret",
		"OPENLINKER_PUBLIC_HOST=example.test",
		"OPENAI_API_KEY=secret",
		"GITHUB_TOKEN=secret",
		"AWS_SECRET_ACCESS_KEY=secret",
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"NORMAL=value",
	}, []string{"NORMAL"})
	if strings.Join(env, ",") != "PATH=/usr/bin:/bin,LC_ALL=C,NORMAL=value" {
		t.Fatalf("sanitized env = %#v", env)
	}

	if _, err := (A2AAdapter{}).Run(context.Background(), JSONMap{"q": "missing"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "A2A_BASE_URL") {
		t.Fatalf("missing A2A url error = %v", err)
	}
	if _, err := (A2AAdapter{
		BaseURL: "://bad",
	}).Run(context.Background(), JSONMap{"q": "bad-url"}, RunContext{}); err == nil {
		t.Fatal("expected invalid A2A URL error")
	}
	if _, err := (A2AAdapter{
		BaseURL: "https://a2a.example/",
		HTTPClient: adapterHTTPClient(func(*http.Request) (*http.Response, error) {
			return adapterHTTPResponse(http.StatusBadGateway, `{"error":"bad gateway"}`), nil
		}),
	}).Run(context.Background(), JSONMap{"q": "bad-status"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "HTTP_502") {
		t.Fatalf("A2A status error = %v", err)
	}
	if _, err := (A2AAdapter{
		BaseURL: "https://a2a.example/",
		HTTPClient: adapterHTTPClient(func(*http.Request) (*http.Response, error) {
			return adapterHTTPResponse(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":-32603,"message":"boom"}}`), nil
		}),
	}).Run(context.Background(), JSONMap{"q": "rpc-error"}, RunContext{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("A2A JSON-RPC error = %v", err)
	}
}

func TestA2AAdapterMessageSend(t *testing.T) {
	var received JSONMap
	adapter := A2AAdapter{
		BaseURL: "https://a2a.example/",
		Token:   "local-agent",
		Headers: map[string]string{
			"x-a2a-agent": "node",
		},
		HTTPClient: adapterHTTPClient(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("authorization") != "Bearer local-agent" || req.Header.Get("x-a2a-agent") != "node" {
				t.Fatalf("headers = %#v", req.Header)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &received); err != nil {
				t.Fatal(err)
			}
			return adapterHTTPResponse(http.StatusOK, `{
				"jsonrpc":"2.0",
				"id":"msg-run-a2a",
				"result":{
					"task":{
						"id":"task-a2a",
						"status":{"state":"TASK_STATE_COMPLETED"},
						"artifacts":[{"parts":[{"text":"done from a2a"}]}]
					}
				}
			}`), nil
		}),
	}

	raw, err := adapter.Run(context.Background(), JSONMap{"query": "hello a2a"}, RunContext{
		RunID:  "run-a2a",
		Source: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if received["method"] != "SendMessage" {
		t.Fatalf("received rpc = %#v", received)
	}
	params := received["params"].(map[string]any)
	message := params["message"].(map[string]any)
	if _, ok := message["kind"]; ok {
		t.Fatalf("current message should not include kind = %#v", message)
	}
	if message["role"] != "ROLE_USER" {
		t.Fatalf("current message role = %#v", message)
	}
	parts := message["parts"].([]any)
	part := parts[0].(map[string]any)
	if part["text"] != "hello a2a" {
		t.Fatalf("message part = %#v", part)
	}
	if _, ok := part["kind"]; ok {
		t.Fatalf("current part should not include kind = %#v", part)
	}
	config := params["configuration"].(map[string]any)
	if config["returnImmediately"] != false {
		t.Fatalf("current config = %#v", config)
	}
	if _, ok := config["blocking"]; ok {
		t.Fatalf("current config should not include blocking = %#v", config)
	}

	result := raw.(AdapterResult)
	if result.Status != "success" || len(result.Events) != 1 {
		t.Fatalf("result = %#v", result)
	}
	output := result.Output.(JSONMap)
	if output["text"] != "done from a2a" {
		t.Fatalf("output = %#v", output)
	}
}

func TestA2AAdapterLegacyDialectMessageSend(t *testing.T) {
	var received JSONMap
	adapter := A2AAdapter{
		BaseURL: "https://a2a.example/",
		Dialect: openlinker.A2ADialectLegacy,
		HTTPClient: adapterHTTPClient(func(req *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			return adapterHTTPResponse(http.StatusOK, `{
				"jsonrpc":"2.0",
				"id":"msg-run-a2a",
				"result":{"id":"task-a2a","status":{"state":"completed"}}
			}`), nil
		}),
	}

	if _, err := adapter.Run(context.Background(), JSONMap{"query": "hello legacy"}, RunContext{RunID: "run-a2a"}); err != nil {
		t.Fatal(err)
	}
	if received["method"] != "message/send" {
		t.Fatalf("legacy rpc = %#v", received)
	}
	params := received["params"].(map[string]any)
	message := params["message"].(map[string]any)
	if message["kind"] != "message" {
		t.Fatalf("legacy message = %#v", message)
	}
	part := message["parts"].([]any)[0].(map[string]any)
	if part["kind"] != "text" || part["text"] != "hello legacy" {
		t.Fatalf("legacy part = %#v", part)
	}
	config := params["configuration"].(map[string]any)
	if config["blocking"] != true {
		t.Fatalf("legacy config = %#v", config)
	}
	if _, ok := config["returnImmediately"]; ok {
		t.Fatalf("legacy config should not include returnImmediately = %#v", config)
	}
}

func TestA2AAdapterExplicitParamsAndFailedStatus(t *testing.T) {
	adapter := A2AAdapter{
		BaseURL: "https://a2a.example/",
		HTTPClient: adapterHTTPClient(func(req *http.Request) (*http.Response, error) {
			var received JSONMap
			if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			params := received["params"].(map[string]any)
			if params["custom"] != "value" {
				t.Fatalf("params = %#v", params)
			}
			return adapterHTTPResponse(http.StatusOK, `{
				"jsonrpc":"2.0",
				"result":{"kind":"task","status":{"state":"TASK_STATE_FAILED","message":{"parts":[{"kind":"text","text":"failed badly"}]}}}
			}`), nil
		}),
	}
	raw, err := adapter.Run(context.Background(), JSONMap{"a2a_params": JSONMap{"custom": "value"}}, RunContext{})
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(AdapterResult)
	if result.Status != "failed" || result.Error == nil || !strings.Contains(result.Error.Message, "failed badly") {
		t.Fatalf("failed result = %#v", result)
	}
}

func TestCodexAdapterMockAndPromptHelper(t *testing.T) {
	var events []RunEvent
	runCtx := RunContext{
		RunID:    "run-codex",
		Metadata: JSONMap{},
		A2A:      JSONMap{"current_run_id": "run-codex"},
		Helper:   testHelperInfo(),
		Emit: func(eventType string, payload any) {
			events = append(events, RunEvent{EventType: eventType, Payload: payload})
		},
	}
	adapter := CodexAdapter{MockResponse: "mocked codex result"}
	output, err := adapter.Run(context.Background(), JSONMap{"task": "explain"}, runCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != "run.message.delta" {
		t.Fatalf("events = %#v", events)
	}
	out := normalizeAdapterResult(output).Output.(JSONMap)
	if out["summary"] != "mocked codex result" {
		t.Fatalf("output = %#v", output)
	}
	runCtx.Conversation = &ConversationContext{
		ID:           "conv-1",
		SessionKey:   "conv-1",
		CurrentRunID: "run-codex",
		Source:       "core",
		HistoryBeforeCurrent: []ConversationMessage{{
			RunID:   "run-prev",
			Role:    "user",
			Content: "previous question",
		}},
	}
	prompt := BuildCodexPrompt(JSONMap{"task": "delegate"}, runCtx)
	for _, want := range []string{
		"agent_node.helper.endpoints.call_agent",
		"agent_node.helper.headers.authorization",
		"http://127.0.0.1:19090/a2a/call",
		"olh_test",
		"conversation.history_before_current",
		"previous question",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCodexAdapterExecutesCLIAndReadsSummary(t *testing.T) {
	workspace := t.TempDir()
	fakeCodex := writeFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
stdin="$(cat)"
printf "%s" "summary from file" > "$out"
printf "%s" "$stdin" > "${out}.stdin"
printf "%s" "stdout ignored"
`)

	output, err := (CodexAdapter{
		CodexBin:  fakeCodex,
		Workspace: workspace,
		Sandbox:   "workspace-write",
		Approval:  "never",
		Model:     "gpt-5",
		Timeout:   codexTestTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), JSONMap{"task": "use fake codex"}, RunContext{
		RunID: "run-cli-codex",
		Emit:  func(string, any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := normalizeAdapterResult(output).Output.(JSONMap)
	if out["summary"] != "summary from file" || out["codex_sandbox"] != "workspace-write" || out["codex_model"] != "gpt-5" {
		t.Fatalf("codex output = %#v", out)
	}
	outputPath := codexOutputFilePath("run-cli-codex")
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("codex output file was not cleaned up: %v", err)
	}
	stdinPath := outputPath + ".stdin"
	t.Cleanup(func() { _ = os.Remove(stdinPath) })
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), "OpenLinker run context") {
		t.Fatalf("codex stdin missing prompt context:\n%s", stdin)
	}
}

func TestCodexOutputFilePathIsSafe(t *testing.T) {
	path := codexOutputFilePath("../../run/with\nodd chars")
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(os.TempDir()) {
		t.Fatalf("codex output path escaped temp dir: %s", path)
	}
	if strings.Contains(filepath.Base(path), "..") || strings.Contains(filepath.Base(path), "/") || strings.Contains(filepath.Base(path), "\n") {
		t.Fatalf("codex output path is not sanitized: %s", path)
	}
}

func TestCodexAdapterStdoutFallbackAndFailure(t *testing.T) {
	workspace := t.TempDir()
	stdoutCodex := writeFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
cat >/dev/null
printf "%s" "summary from stdout"
`)
	output, err := (CodexAdapter{
		CodexBin:  stdoutCodex,
		Workspace: workspace,
		Timeout:   codexTestTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), "stdout fallback", RunContext{
		RunID: "run-cli-stdout",
		Emit:  func(string, any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalizeAdapterResult(output).Output.(JSONMap)["summary"] != "summary from stdout" {
		t.Fatalf("stdout fallback output = %#v", output)
	}

	failingCodex := writeFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
echo "fake failure" >&2
exit 7
`)
	_, err = (CodexAdapter{
		CodexBin:  failingCodex,
		Workspace: workspace,
		Timeout:   codexTestTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), "fail", RunContext{
		RunID: "run-cli-fail",
		Emit:  func(string, any) {},
	})
	if err == nil || !strings.Contains(err.Error(), "fake failure") {
		t.Fatalf("failure error = %v", err)
	}
}

func TestCodexAdapterSessionReuseUsesConversationKey(t *testing.T) {
	workspace := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	fakeCodex := writeFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
original="$*"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
cat >/dev/null
printf "%s" "summary from reusable session" > "$out"
printf "%s" "$original" > "${out}.args"
printf '{"type":"session.created","session_id":"codex-session-1"}\n'
`)
	adapter := CodexAdapter{
		CodexBin:     fakeCodex,
		Workspace:    workspace,
		Sandbox:      "workspace-write",
		Approval:     "never",
		Timeout:      codexTestTimeout,
		SessionReuse: true,
		SessionStore: storePath,
		Env:          []string{"PATH=/usr/bin:/bin"},
	}
	runCtx := RunContext{
		RunID: "run-session-1",
		Conversation: &ConversationContext{
			ID:           "conv-session",
			SessionKey:   "conv-session",
			CurrentRunID: "run-session-1",
			Source:       "core",
		},
		Emit: func(string, any) {},
	}
	first, err := adapter.Run(context.Background(), JSONMap{"task": "first"}, runCtx)
	if err != nil {
		t.Fatal(err)
	}
	firstOut := normalizeAdapterResult(first).Output.(JSONMap)
	if firstOut["codex_session_reuse"] != true || firstOut["codex_session_resumed"] == true {
		t.Fatalf("first output = %#v", firstOut)
	}
	firstArgsPath := codexOutputFilePath("run-session-1") + ".args"
	t.Cleanup(func() { _ = os.Remove(firstArgsPath) })
	firstArgs, err := os.ReadFile(firstArgsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstArgs), "--json") || strings.Contains(string(firstArgs), "--ephemeral") || strings.Contains(string(firstArgs), " resume ") {
		t.Fatalf("first args did not start persistent json session: %s", firstArgs)
	}
	if got := loadCodexSessionID(storePath, workspace, "conv-session"); got != "codex-session-1" {
		t.Fatalf("stored session id = %q", got)
	}

	runCtx.RunID = "run-session-2"
	runCtx.Conversation.CurrentRunID = "run-session-2"
	second, err := adapter.Run(context.Background(), JSONMap{"task": "second"}, runCtx)
	if err != nil {
		t.Fatal(err)
	}
	secondOut := normalizeAdapterResult(second).Output.(JSONMap)
	if secondOut["codex_session_resumed"] != true {
		t.Fatalf("second output = %#v", secondOut)
	}
	secondArgsPath := codexOutputFilePath("run-session-2") + ".args"
	t.Cleanup(func() { _ = os.Remove(secondArgsPath) })
	secondArgs, err := os.ReadFile(secondArgsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exec resume", "codex-session-1", "--output-last-message"} {
		if !strings.Contains(string(secondArgs), want) {
			t.Fatalf("second args missing %q: %s", want, secondArgs)
		}
	}
}

func TestCodexAdapterRejectsOversizedFinalMessage(t *testing.T) {
	workspace := t.TempDir()
	oversizedCodex := writeFakeCodex(t, fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
cat >/dev/null
dd if=/dev/zero of="$out" bs=%d count=1 2>/dev/null
`, maxAdapterOutputBytes+1))

	_, err := (CodexAdapter{
		CodexBin:  oversizedCodex,
		Workspace: workspace,
		Timeout:   codexTestTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), "oversized", RunContext{
		RunID: "run-cli-codex-oversized",
		Emit:  func(string, any) {},
	})
	if err == nil || !strings.Contains(err.Error(), "final message exceeded") {
		t.Fatalf("oversized codex output error = %v", err)
	}
}

func writeFakeCodex(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type adapterRoundTripper func(*http.Request) (*http.Response, error)

func (f adapterRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func adapterHTTPClient(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: adapterRoundTripper(fn)}
}

func adapterHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testHelperInfo() *HelperInfo {
	return &HelperInfo{
		BaseURL: "http://127.0.0.1:19090",
		Token:   "olh_test",
		Headers: map[string]string{
			"authorization": "Bearer olh_test",
		},
		Endpoints: HelperEndpoints{
			CallAgent: "http://127.0.0.1:19090/a2a/call",
			Events:    "http://127.0.0.1:19090/events",
		},
	}
}
