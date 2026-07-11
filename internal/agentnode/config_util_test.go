package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestNewFromEnvMapOpenClawRuntimeV2HTTP(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":               "https://example.test/api/v1",
		"OPENLINKER_NODE_ID":                   "11111111-1111-4111-8111-111111111111",
		"OPENLINKER_AGENT_ID":                  "22222222-2222-4222-8222-222222222222",
		"OPENLINKER_AGENT_TOKEN":               "ol_agent_env",
		"OPENLINKER_AGENT_NODE_DATA_DIR":       "/var/lib/openlinker-agent-node",
		"OPENLINKER_AGENT_NODE_MTLS_CERT_FILE": "/run/openlinker/client.crt",
		"OPENLINKER_AGENT_NODE_MTLS_KEY_FILE":  "/run/openlinker/client.key",
		"OPENLINKER_AGENT_NODE_MTLS_CA_FILE":   "/run/openlinker/ca.crt",
		"OPENLINKER_AGENT_NODE_ADAPTER":        "openclaw",
		"OPENLINKER_AGENT_NODE_HTTP_URL":       "http://127.0.0.1:18080/run",
		"OPENLINKER_AGENT_NODE_HTTP_HEADERS":   `{"x-openlinker-agent":"node"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.CoreURL != "https://example.test/api/v1" || node.AgentToken != "ol_agent_env" || node.DataDir != "/var/lib/openlinker-agent-node" {
		t.Fatalf("node config = %#v", node)
	}
	if node.Capacity != 1 || node.ClaimWait != 25*time.Second || node.HeartbeatInterval != 5*time.Second {
		t.Fatalf("runtime v2 timing/capacity = %#v", node)
	}
	adapter, ok := node.Adapter.(HTTPAdapter)
	if !ok {
		t.Fatalf("adapter = %T", node.Adapter)
	}
	if adapter.URL != "http://127.0.0.1:18080/run" || adapter.Headers["x-openlinker-agent"] != "node" {
		t.Fatalf("http adapter = %#v", adapter)
	}
	if node.Helper == nil {
		t.Fatal("openclaw adapter should enable helper in auto mode")
	}
}

func TestNewFromEnvMapRuntimeV2Command(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":                     "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":                     "ol_agent_v2",
		"OPENLINKER_AGENT_NODE_CLAIM_WAIT_SECONDS":   "2",
		"OPENLINKER_AGENT_NODE_COMMAND_WAIT_SECONDS": "4",
		"OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS":    "3",
		"OPENLINKER_AGENT_NODE_CAPACITY":             "4",
		"OPENLINKER_AGENT_NODE_ADAPTER":              "command",
		"OPENLINKER_AGENT_NODE_COMMAND":              "/usr/local/bin/openclaw",
		"OPENLINKER_AGENT_NODE_ARGS":                 `["run","--json"]`,
		"OPENLINKER_AGENT_NODE_CWD":                  "/tmp",
		"OPENLINKER_AGENT_NODE_ENV_ALLOWLIST":        "CUSTOM_PATH, OPENCLAW_MODE",
		"OPENLINKER_AGENT_NODE_HELPER":               "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.ClaimWait != 2*time.Second || node.CommandWait != 4*time.Second || node.HeartbeatInterval != 3*time.Second || node.Capacity != 4 {
		t.Fatalf("runtime v2 config = %#v", node)
	}
	adapter, ok := node.Adapter.(CommandAdapter)
	if !ok {
		t.Fatalf("adapter = %T", node.Adapter)
	}
	if adapter.Command != "/usr/local/bin/openclaw" || strings.Join(adapter.Args, " ") != "run --json" || adapter.CWD != "/tmp" {
		t.Fatalf("command adapter = %#v", adapter)
	}
	if strings.Join(adapter.EnvAllowlist, ",") != "CUSTOM_PATH,OPENCLAW_MODE" {
		t.Fatalf("command env allowlist = %#v", adapter.EnvAllowlist)
	}
	if node.Helper != nil {
		t.Fatalf("helper should be disabled: %#v", node.Helper)
	}
}

func TestNewFromEnvUsesProcessEnvironment(t *testing.T) {
	t.Setenv("OPENLINKER_CORE_V2_URL", "https://env.example.test")
	t.Setenv("OPENLINKER_AGENT_TOKEN", "ol_agent_env_process")
	t.Setenv("OPENLINKER_NODE_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("OPENLINKER_AGENT_ID", "22222222-2222-4222-8222-222222222222")
	t.Setenv("OPENLINKER_AGENT_NODE_DATA_DIR", t.TempDir())
	t.Setenv("OPENLINKER_AGENT_NODE_MTLS_CERT_FILE", "/tmp/client.crt")
	t.Setenv("OPENLINKER_AGENT_NODE_MTLS_KEY_FILE", "/tmp/client.key")
	t.Setenv("OPENLINKER_AGENT_NODE_MTLS_CA_FILE", "/tmp/ca.crt")
	t.Setenv("OPENLINKER_AGENT_NODE_ADAPTER", "command")
	t.Setenv("OPENLINKER_AGENT_NODE_COMMAND", "/bin/echo")
	t.Setenv("OPENLINKER_AGENT_NODE_ARGS", `["hello"]`)
	t.Setenv("OPENLINKER_AGENT_NODE_CWD", "/tmp")
	t.Setenv("OPENLINKER_AGENT_NODE_HELPER", "false")

	node, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if node.CoreURL != "https://env.example.test" || node.AgentToken != "ol_agent_env_process" {
		t.Fatalf("node from env = %#v", node)
	}
	adapter, ok := node.Adapter.(CommandAdapter)
	if !ok || adapter.Command != "/bin/echo" || strings.Join(adapter.Args, " ") != "hello" {
		t.Fatalf("adapter = %#v (%T)", node.Adapter, node.Adapter)
	}
}

func TestNewFromEnvMapCodexAndInvalidEnv(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":                    "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":                    "ol_agent_codex",
		"OPENLINKER_AGENT_NODE_CODEX_WORKSPACE":     "/workspace",
		"OPENLINKER_AGENT_NODE_CODEX_SANDBOX":       "workspace-write",
		"OPENLINKER_AGENT_NODE_CODEX_APPROVAL":      "never",
		"OPENLINKER_AGENT_NODE_CODEX_MODEL":         "gpt-5",
		"OPENLINKER_AGENT_NODE_CODEX_BIN":           "codex",
		"OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE": "ok",
		"OPENLINKER_AGENT_NODE_ENV_ALLOWLIST":       "CODEX_HOME",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := node.Adapter.(CodexAdapter)
	if !ok {
		t.Fatalf("adapter = %T", node.Adapter)
	}
	if adapter.Workspace != "/workspace" || adapter.Sandbox != "workspace-write" || adapter.Model != "gpt-5" {
		t.Fatalf("codex adapter = %#v", adapter)
	}
	if strings.Join(adapter.EnvAllowlist, ",") != "CODEX_HOME" {
		t.Fatalf("codex env allowlist = %#v", adapter.EnvAllowlist)
	}
	if node.Helper == nil {
		t.Fatal("codex adapter should enable helper in auto mode")
	}

	if _, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":        "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":        "ol_agent_bad",
		"OPENLINKER_AGENT_NODE_ADAPTER": "module",
	}); err == nil || !strings.Contains(err.Error(), "module adapter is not supported") {
		t.Fatalf("module adapter error = %v", err)
	}
	if _, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":        "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":        "ol_agent_bad",
		"OPENLINKER_AGENT_NODE_ARGS":    "not-json",
		"OPENLINKER_AGENT_NODE_COMMAND": "openclaw",
	}); err == nil || !strings.Contains(err.Error(), "JSON string array") {
		t.Fatalf("args parse error = %v", err)
	}
}

func TestNewFromEnvMapA2AAdapter(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":                          "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":                          "ol_agent_a2a",
		"OPENLINKER_AGENT_NODE_A2A_BASE_URL":              "http://127.0.0.1:9001/",
		"OPENLINKER_UPSTREAM_A2A_TOKEN":                   "a2a-token",
		"OPENLINKER_AGENT_NODE_A2A_HEADERS":               `{"x-a2a-agent":"local"}`,
		"OPENLINKER_AGENT_NODE_A2A_ACCEPTED_OUTPUT_MODES": `["application/json"]`,
		"OPENLINKER_AGENT_NODE_A2A_METHOD":                "SendMessage",
		"OPENLINKER_AGENT_NODE_A2A_PROTOCOL_VERSION":      "1.0",
		"OPENLINKER_AGENT_NODE_TIMEOUT_MS":                "120000",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := node.Adapter.(A2AAdapter)
	if !ok {
		t.Fatalf("adapter = %T", node.Adapter)
	}
	if adapter.BaseURL != "http://127.0.0.1:9001/" || adapter.Token != "a2a-token" || adapter.Headers["x-a2a-agent"] != "local" {
		t.Fatalf("a2a adapter = %#v", adapter)
	}
	if adapter.Method != "SendMessage" || adapter.Dialect != openlinker.A2ADialectCurrent || adapter.Timeout != 2*time.Minute || strings.Join(adapter.AcceptedOutputModes, ",") != "application/json" {
		t.Fatalf("a2a adapter timing/method = %#v", adapter)
	}
	if node.Helper != nil {
		t.Fatalf("a2a adapter should not enable helper by default: %#v", node.Helper)
	}
}

func TestNewFromEnvMapA2AAdapterLegacyDialect(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_CORE_V2_URL":             "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":             "ol_agent_a2a",
		"OPENLINKER_AGENT_NODE_A2A_BASE_URL": "http://127.0.0.1:9001/",
		"OPENLINKER_AGENT_NODE_A2A_DIALECT":  "legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := node.Adapter.(A2AAdapter)
	if !ok {
		t.Fatalf("adapter = %T", node.Adapter)
	}
	if adapter.Method != openlinker.A2ALegacyMethodMessageSend || adapter.Dialect != openlinker.A2ADialectLegacy {
		t.Fatalf("legacy a2a adapter = %#v", adapter)
	}
}

func TestOptionsParsersAndURLHelpers(t *testing.T) {
	if !boolOption("YES", false) || boolOption("off", true) || !boolOption("maybe", true) {
		t.Fatal("boolOption returned an unexpected value")
	}
	if got, err := numberOption("42", 0, "TEST_NUMBER"); err != nil || got != 42 {
		t.Fatalf("numberOption = %d, %v", got, err)
	}
	if _, err := numberOption("-1", 0, "TEST_NUMBER"); err == nil {
		t.Fatal("expected negative number error")
	}
	values, err := parseJSONStringArray(`["a","b"]`, "TEST_ARRAY")
	if err != nil || strings.Join(values, ",") != "a,b" {
		t.Fatalf("parseJSONStringArray = %#v, %v", values, err)
	}
	headers, err := parseJSONMap(`{"x":"y"}`, "TEST_HEADERS")
	if err != nil || headers["x"] != "y" {
		t.Fatalf("parseJSONMap = %#v, %v", headers, err)
	}
	if headers, err := parseJSONMap("", "TEST_HEADERS"); err != nil || headers != nil {
		t.Fatalf("parseJSONMap empty = %#v, %v", headers, err)
	}
	if _, err := parseJSONMap("not-json", "TEST_HEADERS"); err == nil {
		t.Fatal("expected parseJSONMap invalid JSON error")
	}
	if got := joinAPIPath("https://example.test/api/v1/", "agents"); got != "https://example.test/api/v1/agents" {
		t.Fatalf("joinAPIPath relative = %q", got)
	}
	if got := joinAPIPath("https://example.test/api/v1", "https://other.test/run"); got != "https://other.test/run" {
		t.Fatalf("joinAPIPath absolute = %q", got)
	}
	if stringFromMap(JSONMap{"answer": 123}, "answer") != "123" {
		t.Fatal("stringFromMap should stringify values")
	}
	if stringFromMap(JSONMap{"answer": nil}, "answer") != "" || stringFromMap(JSONMap{}, "missing") != "" {
		t.Fatal("stringFromMap should return empty string for nil or missing values")
	}
	body, err := readJSONResponse(&http.Response{Body: io.NopCloser(strings.NewReader("not-json"))})
	if err != nil || len(body.(JSONMap)) != 0 {
		t.Fatalf("readJSONResponse invalid body = %#v, %v", body, err)
	}
	body, err = readJSONResponse(&http.Response{Body: io.NopCloser(strings.NewReader("null"))})
	if err != nil || len(body.(JSONMap)) != 0 {
		t.Fatalf("readJSONResponse null body = %#v, %v", body, err)
	}
}

func TestNormalizeAdapterResultBranches(t *testing.T) {
	events := []any{map[string]any{"event_type": "run.message.delta", "payload": JSONMap{"text": "hi"}}}
	result := normalizeAdapterResult(map[string]any{
		"status": "success",
		"events": events,
		"answer": "ok",
	})
	if result.Status != "success" || len(result.Events) != 1 {
		t.Fatalf("result = %#v", result)
	}
	output := result.Output.(JSONMap)
	if output["answer"] != "ok" {
		t.Fatalf("output = %#v", output)
	}
	withOutput := normalizeAdapterResult(map[string]any{
		"status": "failed",
		"output": JSONMap{"reason": "bad input"},
	})
	if withOutput.Status != "failed" || withOutput.Output.(JSONMap)["reason"] != "bad input" {
		t.Fatalf("withOutput = %#v", withOutput)
	}
	if got := normalizeAdapterResult(AdapterResult{Output: JSONMap{"ok": true}}); got.Status != "success" || got.Output.(JSONMap)["ok"] != true {
		t.Fatalf("typed adapter result = %#v", got)
	}
	typed := &AdapterResult{Status: "success", Output: "done"}
	if got := normalizeAdapterResult(typed); got.Output != "done" {
		t.Fatalf("pointer adapter result = %#v", got)
	}
	if got := normalizeAdapterResult((*AdapterResult)(nil)); got.Status != "success" || len(got.Output.(JSONMap)) != 0 {
		t.Fatalf("nil pointer adapter result = %#v", got)
	}
	if got := normalizeAdapterResult("plain output"); got.Status != "success" || got.Output != "plain output" {
		t.Fatalf("raw adapter result = %#v", got)
	}
	filled := fillAdapterDefaults(AdapterResult{Error: &AgentError{Code: "BAD", Message: "bad"}})
	if filled.Status != "success" || filled.Output != nil {
		t.Fatalf("filled = %#v", filled)
	}
	if normalizeAgentError(fmt.Errorf("boom")).Code != "AGENT_NODE_ERROR" {
		t.Fatal("normalizeAgentError returned unexpected code")
	}
	if normalizeAgentError(nil) != nil {
		t.Fatal("nil error should normalize to nil")
	}
	if len(eventsFromAny([]RunEvent{{EventType: "done"}})) != 1 {
		t.Fatal("eventsFromAny should preserve typed events")
	}
	if len(eventsFromAny("not-events")) != 0 {
		t.Fatal("eventsFromAny should ignore unsupported values")
	}
	if len(normalizeMetadata(nil)) != 0 {
		t.Fatal("normalizeMetadata nil should return an empty map")
	}
	if got := normalizeMetadata(JSONMap{"x": "y"}); got["x"] != "y" {
		t.Fatalf("normalizeMetadata JSONMap = %#v", got)
	}
	if got := normalizeMetadata(map[string]any{"x": "y"}); got["x"] != "y" {
		t.Fatalf("normalizeMetadata map = %#v", got)
	}
	if len(normalizeMetadata("not-a-map")) != 0 {
		t.Fatal("normalizeMetadata should return an empty map for unsupported values")
	}
}

func TestSmallAdapterAndRuntimeBranches(t *testing.T) {
	if modelLabel("") != "default" || modelLabel("gpt-5") != "gpt-5" {
		t.Fatal("modelLabel returned an unexpected value")
	}
	allowed := map[string]bool{"PATH": true, "CUSTOM_ENV": true}
	if !adapterEnvKeyAllowed("PATH", allowed) || !adapterEnvKeyAllowed("LC_ALL", allowed) || !adapterEnvKeyAllowed("CUSTOM_ENV", allowed) {
		t.Fatal("adapterEnvKeyAllowed rejected allowed environment keys")
	}
	if adapterEnvKeyAllowed("OPENLINKER_AGENT_TOKEN", allowed) || adapterEnvKeyAllowed("OPENAI_API_KEY", allowed) {
		t.Fatal("adapterEnvKeyAllowed accepted credential environment keys")
	}
	if got, err := parseCommandOutput("", "warn"); err != nil || got.(JSONMap)["stderr"] != "warn" {
		t.Fatalf("parseCommandOutput empty = %#v, %v", got, err)
	}
	if got, err := parseCommandOutput("plain text", ""); err != nil || got.(JSONMap)["text"] != "plain text" {
		t.Fatalf("parseCommandOutput text = %#v, %v", got, err)
	}
	if got, err := parseCommandOutput(`{"answer":"ok"}`, ""); err != nil || got.(map[string]any)["answer"] != "ok" {
		t.Fatalf("parseCommandOutput json = %#v, %v", got, err)
	}
	if err := (&Node{}).applyDefaultsAndValidate(); err == nil || !strings.Contains(err.Error(), "Core v2 URL") {
		t.Fatalf("missing Core v2 URL error = %v", err)
	}
	if _, err := adapterFromEnv(func(string) string { return "" }, "module"); err == nil || !strings.Contains(err.Error(), "module adapter") {
		t.Fatalf("module adapter error = %v", err)
	}
	if _, err := adapterFromEnv(func(string) string { return "" }, "bad"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported adapter error = %v", err)
	}
	if _, err := adapterFromEnv(func(key string) string {
		if key == "OPENLINKER_AGENT_NODE_TIMEOUT_MS" {
			return "-1"
		}
		return ""
	}, "http"); err == nil || !strings.Contains(err.Error(), "OPENLINKER_AGENT_NODE_TIMEOUT_MS") {
		t.Fatalf("invalid adapter timeout error = %v", err)
	}
	if _, err := helperFromEnv(func(key string) string {
		if key == "OPENLINKER_AGENT_NODE_HELPER" {
			return "surprise"
		}
		return ""
	}, "http"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid helper mode error = %v", err)
	}
	if _, err := helperFromEnv(func(key string) string {
		if key == "OPENLINKER_AGENT_NODE_HELPER_PORT" {
			return "-1"
		}
		return ""
	}, "http"); err == nil || !strings.Contains(err.Error(), "OPENLINKER_AGENT_NODE_HELPER_PORT") {
		t.Fatalf("invalid helper port error = %v", err)
	}
	if err := sleepContext(context.Background(), time.Nanosecond); err != nil {
		t.Fatalf("sleepContext short duration = %v", err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(cancelCtx, time.Second); err == nil {
		t.Fatal("expected canceled sleep")
	}
}

func TestPublicA2AClientSendMessage(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/a2a/agents/sluggy" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("authorization") != "Bearer ol_public" || r.Header.Get("a2a-version") != "1.0" {
			t.Fatalf("headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, JSONMap{
			"jsonrpc": "2.0",
			"id":      received["id"],
			"result": JSONMap{
				"task": JSONMap{
					"id":     "task-public",
					"status": JSONMap{"state": "completed"},
				},
			},
		})
	}))
	defer server.Close()

	client := PublicA2AClient{APIBase: server.URL, Token: "ol_public"}
	result, err := client.SendMessage(context.Background(), "sluggy", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if received["method"] != "SendMessage" {
		t.Fatalf("body = %#v", received)
	}
	params := received["params"].(map[string]any)
	message := params["message"].(map[string]any)
	if message["role"] != "ROLE_USER" {
		t.Fatalf("message role = %#v", message)
	}
	task, ok := result.(*openlinker.A2ATask)
	if !ok || task.ID != "task-public" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPublicA2AClientReportsErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, JSONMap{"error": JSONMap{"code": "BAD", "message": "bad"}})
	}))
	defer server.Close()

	client := PublicA2AClient{APIBase: server.URL, Token: "ol_public"}
	if _, err := client.SendMessage(context.Background(), "sluggy", "hello"); err == nil {
		t.Fatal("expected json-rpc error")
	}
}

func TestLocalHelperServerRejectsBadRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helper := &LocalHelperServer{}
	if err := helper.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer helper.Stop(context.Background())

	emitted := 0
	var delegated CallAgentOptions
	runCtx := &RunContext{
		RunID: "run-helper",
		Emit: func(eventType string, payload any) {
			emitted++
		},
		CallAgent: func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error) {
			if targetAgentID == "agent-fail" {
				return nil, fmt.Errorf("delegate failed")
			}
			delegated = options
			return JSONMap{"target_agent_id": targetAgentID, "ok": true}, nil
		},
	}
	session := helper.CreateSession("run-helper", runCtx)
	defer session.Close()

	assertHelperStatus(t, http.MethodGet, session.Info.Endpoints.Events, session.Info.Token, nil, http.StatusMethodNotAllowed)
	assertHelperStatus(t, http.MethodGet, session.Info.Endpoints.CallAgent, session.Info.Token, nil, http.StatusMethodNotAllowed)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, "", JSONMap{"event_type": "run.message.delta"}, http.StatusUnauthorized)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, "", JSONMap{"target_agent_id": "agent-child"}, http.StatusUnauthorized)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, session.Info.Token, JSONMap{
		"run_id":     "other-run",
		"event_type": "run.message.delta",
	}, http.StatusConflict)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, session.Info.Token, JSONMap{}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, "not-json", http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-child",
	}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-child",
		"idempotency_key": " normalized ",
	}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-child",
		"idempotency_key": "unknown-field",
		"endpoint":        "/legacy/endpoint",
	}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"run_id":          "other-run",
		"target_agent_id": "agent-child",
	}, http.StatusConflict)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-child",
		"idempotency_key": "helper-chain-1",
		"input":           JSONMap{"q": "hello"},
		"reason":          "chain",
		"metadata":        JSONMap{"trace": "helper"},
	}, http.StatusOK)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-fail",
		"idempotency_key": "helper-fail-1",
	}, http.StatusBadGateway)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, session.Info.Token, JSONMap{
		"event_type": "run.message.delta",
		"payload":    JSONMap{"text": "ok"},
	}, http.StatusOK)
	if emitted != 1 {
		t.Fatalf("emitted = %d", emitted)
	}
	if delegated.IdempotencyKey != "helper-chain-1" || delegated.Reason != "chain" || delegated.Metadata.(map[string]any)["trace"] != "helper" {
		t.Fatalf("delegated options = %#v", delegated)
	}
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-child",
		"idempotency_key": "helper-chain-2",
		"input":           JSONMap{"q": "hello"},
		"reason":          "chain",
		"metadata":        JSONMap{"trace": "helper"},
	}, http.StatusOK)
	if delegated.IdempotencyKey != "helper-chain-2" {
		t.Fatalf("second helper intent key = %#v", delegated)
	}
}

func assertHelperStatus(t *testing.T, method, endpoint, token string, body any, want int) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	req.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		t.Fatalf("%s %s status = %d, want %d", method, endpoint, res.StatusCode, want)
	}
}
