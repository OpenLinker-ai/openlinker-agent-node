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
)

func TestNewFromEnvMapOpenClawRuntimeWS(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_API_ROOT":                "https://example.test/api/v1",
		"OPENLINKER_RUNTIME_TOKEN":           "ol_live_env",
		"OPENLINKER_AGENT_NODE_ADAPTER":      "openclaw",
		"OPENLINKER_AGENT_NODE_HTTP_URL":     "http://127.0.0.1:18080/run",
		"OPENLINKER_AGENT_NODE_HTTP_HEADERS": `{"x-openlinker-agent":"node"}`,
		"OPENLINKER_AGENT_NODE_RECONNECT":    "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.APIBase != "https://example.test" || node.RuntimeToken != "ol_live_env" {
		t.Fatalf("node config = %#v", node)
	}
	ws, ok := node.Connector.(*RuntimeWSConnector)
	if !ok {
		t.Fatalf("connector = %T", node.Connector)
	}
	if ws.Reconnect {
		t.Fatal("expected reconnect=false")
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

func TestNewFromEnvMapRuntimePullCommand(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_API_BASE":                     "https://api.example.test",
		"OPENLINKER_RUNTIME_TOKEN":                "ol_live_pull",
		"OPENLINKER_AGENT_NODE_CONNECTOR":         "runtime_pull",
		"OPENLINKER_AGENT_NODE_PULL_WAIT_SECONDS": "2",
		"OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS": "3",
		"OPENLINKER_AGENT_NODE_MAX_RUNS":          "4",
		"OPENLINKER_AGENT_NODE_STOP_ON_EMPTY":     "true",
		"OPENLINKER_AGENT_NODE_ADAPTER":           "command",
		"OPENLINKER_AGENT_NODE_COMMAND":           "/usr/local/bin/openclaw",
		"OPENLINKER_AGENT_NODE_ARGS":              `["run","--json"]`,
		"OPENLINKER_AGENT_NODE_CWD":               "/tmp",
		"OPENLINKER_AGENT_NODE_HELPER":            "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	pull, ok := node.Connector.(*RuntimePullConnector)
	if !ok {
		t.Fatalf("connector = %T", node.Connector)
	}
	if pull.Wait != 2*time.Second || pull.Heartbeat != 3*time.Second || pull.MaxRuns != 4 || !pull.StopOnEmpty {
		t.Fatalf("pull connector = %#v", pull)
	}
	adapter, ok := node.Adapter.(CommandAdapter)
	if !ok {
		t.Fatalf("adapter = %T", node.Adapter)
	}
	if adapter.Command != "/usr/local/bin/openclaw" || strings.Join(adapter.Args, " ") != "run --json" || adapter.CWD != "/tmp" {
		t.Fatalf("command adapter = %#v", adapter)
	}
	if node.Helper != nil {
		t.Fatalf("helper should be disabled: %#v", node.Helper)
	}
}

func TestNewFromEnvMapCodexAndInvalidEnv(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_API_BASE":                       "https://api.example.test",
		"OPENLINKER_RUNTIME_TOKEN":                  "ol_live_codex",
		"OPENLINKER_AGENT_NODE_CODEX_WORKSPACE":     "/workspace",
		"OPENLINKER_AGENT_NODE_CODEX_SANDBOX":       "workspace-write",
		"OPENLINKER_AGENT_NODE_CODEX_APPROVAL":      "never",
		"OPENLINKER_AGENT_NODE_CODEX_MODEL":         "gpt-5",
		"OPENLINKER_AGENT_NODE_CODEX_BIN":           "codex",
		"OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE": "ok",
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
	if node.Helper == nil {
		t.Fatal("codex adapter should enable helper in auto mode")
	}

	if _, err := NewFromEnvMap(Env{
		"OPENLINKER_API_BASE":           "https://api.example.test",
		"OPENLINKER_RUNTIME_TOKEN":      "ol_live_bad",
		"OPENLINKER_AGENT_NODE_ADAPTER": "module",
	}); err == nil || !strings.Contains(err.Error(), "module adapter is not supported") {
		t.Fatalf("module adapter error = %v", err)
	}
	if _, err := NewFromEnvMap(Env{
		"OPENLINKER_API_BASE":             "https://api.example.test",
		"OPENLINKER_RUNTIME_TOKEN":        "ol_live_bad",
		"OPENLINKER_AGENT_NODE_CONNECTOR": "runtime_pull",
		"OPENLINKER_AGENT_NODE_ARGS":      "not-json",
		"OPENLINKER_AGENT_NODE_COMMAND":   "openclaw",
	}); err == nil || !strings.Contains(err.Error(), "JSON string array") {
		t.Fatalf("args parse error = %v", err)
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
	wsURL, err := websocketURL("https://example.test/api/v1/", "/agent-runtime/ws")
	if err != nil || wsURL != "wss://example.test/api/v1/agent-runtime/ws" {
		t.Fatalf("websocketURL = %q, %v", wsURL, err)
	}
	res := &http.Response{Header: http.Header{"Retry-After": []string{"3"}}}
	if retryAfterDuration(res, time.Second) != 3*time.Second {
		t.Fatal("retryAfterDuration did not parse seconds")
	}
	if retryAfterDuration(&http.Response{Header: http.Header{"Retry-After": []string{"bad"}}}, time.Second) != time.Second {
		t.Fatal("retryAfterDuration should fall back on invalid values")
	}
	if stringFromMap(JSONMap{"answer": 123}, "answer") != "123" {
		t.Fatal("stringFromMap should stringify values")
	}
	body, err := readJSONResponse(&http.Response{Body: io.NopCloser(strings.NewReader("not-json"))})
	if err != nil || len(body.(JSONMap)) != 0 {
		t.Fatalf("readJSONResponse invalid body = %#v, %v", body, err)
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
	if normalizeMetadata("not-a-map") == nil {
		t.Fatal("normalizeMetadata should return an empty map")
	}
}

func TestSmallAdapterAndConnectorBranches(t *testing.T) {
	if modelLabel("") != "default" || modelLabel("gpt-5") != "gpt-5" {
		t.Fatal("modelLabel returned an unexpected value")
	}
	if !looksSecretKey("OPENLINKER_RUNTIME_TOKEN") || looksSecretKey("OPENLINKER_PUBLIC_HOST") {
		t.Fatal("looksSecretKey returned an unexpected value")
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
	if err := (&RuntimePullConnector{}).SendRunEvent(context.Background(), "run-id", RunEvent{EventType: "noop"}); err != nil {
		t.Fatalf("SendRunEvent = %v", err)
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
		writeJSON(w, http.StatusOK, JSONMap{"jsonrpc": "2.0", "result": JSONMap{"ok": true}})
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
	if result.(map[string]any)["result"] == nil {
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
	runCtx := &RunContext{
		RunID: "run-helper",
		Emit: func(eventType string, payload any) {
			emitted++
		},
		CallAgent: func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error) {
			return JSONMap{"target_agent_id": targetAgentID, "ok": true}, nil
		},
	}
	session := helper.CreateSession("run-helper", runCtx)
	defer session.Close()

	assertHelperStatus(t, http.MethodGet, session.Info.Endpoints.Events, session.Info.Token, nil, http.StatusMethodNotAllowed)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, "", JSONMap{"event_type": "run.message.delta"}, http.StatusUnauthorized)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, session.Info.Token, JSONMap{
		"run_id":     "other-run",
		"event_type": "run.message.delta",
	}, http.StatusConflict)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, session.Info.Token, JSONMap{}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{}, http.StatusBadRequest)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"run_id":          "other-run",
		"target_agent_id": "agent-child",
	}, http.StatusConflict)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.CallAgent, session.Info.Token, JSONMap{
		"target_agent_id": "agent-child",
		"input":           JSONMap{"q": "hello"},
	}, http.StatusOK)
	assertHelperStatus(t, http.MethodPost, session.Info.Endpoints.Events, session.Info.Token, JSONMap{
		"event_type": "run.message.delta",
		"payload":    JSONMap{"text": "ok"},
	}, http.StatusOK)
	if emitted != 1 {
		t.Fatalf("emitted = %d", emitted)
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
