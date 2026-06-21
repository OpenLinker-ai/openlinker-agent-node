package agentnode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		Command: os.Args[0],
		Args:    []string{"-test.run=TestCommandAdapterPassesHelper"},
		Env:     append(os.Environ(), "AGENTNODE_HELPER_PROCESS=1"),
		Timeout: testTimeout,
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
	out := output.(JSONMap)
	if out["summary"] != "mocked codex result" {
		t.Fatalf("output = %#v", output)
	}
	prompt := BuildCodexPrompt(JSONMap{"task": "delegate"}, runCtx)
	for _, want := range []string{
		"agent_node.helper.endpoints.call_agent",
		"agent_node.helper.headers.authorization",
		"http://127.0.0.1:19090/a2a/call",
		"olh_test",
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
		Timeout:   testTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), JSONMap{"task": "use fake codex"}, RunContext{
		RunID: "run-cli-codex",
		Emit:  func(string, any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := output.(JSONMap)
	if out["summary"] != "summary from file" || out["codex_sandbox"] != "workspace-write" || out["codex_model"] != "gpt-5" {
		t.Fatalf("codex output = %#v", out)
	}
	stdinPath := filepath.Join(os.TempDir(), "openlinker-codex-run-cli-codex.txt.stdin")
	t.Cleanup(func() { _ = os.Remove(stdinPath) })
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), "OpenLinker run context") {
		t.Fatalf("codex stdin missing prompt context:\n%s", stdin)
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
		Timeout:   testTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), "stdout fallback", RunContext{
		RunID: "run-cli-stdout",
		Emit:  func(string, any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.(JSONMap)["summary"] != "summary from stdout" {
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
		Timeout:   testTimeout,
		Env:       []string{"PATH=/usr/bin:/bin"},
	}).Run(context.Background(), "fail", RunContext{
		RunID: "run-cli-fail",
		Emit:  func(string, any) {},
	})
	if err == nil || !strings.Contains(err.Error(), "fake failure") {
		t.Fatalf("failure error = %v", err)
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
