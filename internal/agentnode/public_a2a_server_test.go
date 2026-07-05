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

func TestPublicA2AServerHTTPAndJSONRPC(t *testing.T) {
	adapterCalls := 0
	server := &PublicA2AServer{
		Host:        "127.0.0.1",
		Port:        0,
		Slug:        "local-agent",
		Name:        "Local Agent",
		Description: "Local A2A test agent",
		Token:       "public-token",
		Adapter: AdapterFunc(func(_ context.Context, input any, runCtx RunContext) (any, error) {
			adapterCalls++
			if runCtx.A2A["protocol_context_id"] == "" {
				t.Fatalf("missing A2A context: %#v", runCtx)
			}
			return JSONMap{"output": JSONMap{"echo": input, "run_id": runCtx.RunID}}, nil
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())

	card := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/.well-known/agent-card.json", "", nil)
	if card.StatusCode != http.StatusOK || !strings.Contains(card.Body, `"Local Agent"`) {
		t.Fatalf("agent card = %d %s", card.StatusCode, card.Body)
	}
	var cardPayload map[string]any
	if err := json.Unmarshal([]byte(card.Body), &cardPayload); err != nil {
		t.Fatal(err)
	}
	supported, ok := cardPayload["supportedInterfaces"].([]any)
	if !ok || len(supported) != 2 {
		t.Fatalf("supportedInterfaces = %#v", cardPayload["supportedInterfaces"])
	}
	for _, raw := range supported {
		item := raw.(map[string]any)
		if strings.EqualFold(item["protocolBinding"].(string), "grpc") {
			t.Fatalf("public Agent Node must not advertise gRPC: %#v", supported)
		}
	}
	capabilities, ok := cardPayload["capabilities"].(map[string]any)
	if !ok || capabilities["pushNotifications"] != true {
		t.Fatalf("capabilities = %#v", cardPayload["capabilities"])
	}

	unauthorized := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/extendedAgentCard", "", nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("extended card without auth = %d %s", unauthorized.StatusCode, unauthorized.Body)
	}
	extended := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/extendedAgentCard", "", map[string]string{"authorization": "Bearer public-token"})
	if extended.StatusCode != http.StatusOK || !strings.Contains(extended.Body, `"extended":true`) {
		t.Fatalf("extended card = %d %s", extended.StatusCode, extended.Body)
	}

	sendBody := `{"message":{"contextId":"ctx-public","role":"ROLE_USER","parts":[{"text":"hello"}]}}`
	send := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/message:send", sendBody, map[string]string{"authorization": "Bearer public-token"})
	if send.StatusCode != http.StatusOK {
		t.Fatalf("message send = %d %s", send.StatusCode, send.Body)
	}
	var sendResp struct {
		Task struct {
			ID        string `json:"id"`
			ContextID string `json:"contextId"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(send.Body), &sendResp); err != nil {
		t.Fatal(err)
	}
	if sendResp.Task.ID == "" || sendResp.Task.ContextID != "ctx-public" {
		t.Fatalf("send response = %#v", sendResp)
	}

	task := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/"+sendResp.Task.ID, "", map[string]string{"authorization": "Bearer public-token"})
	if task.StatusCode != http.StatusOK || !strings.Contains(task.Body, sendResp.Task.ID) {
		t.Fatalf("get task = %d %s", task.StatusCode, task.Body)
	}
	wrongTaskMethod := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/tasks/"+sendResp.Task.ID, "", map[string]string{"authorization": "Bearer public-token"})
	if wrongTaskMethod.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post get task = %d %s", wrongTaskMethod.StatusCode, wrongTaskMethod.Body)
	}
	list := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks", "", map[string]string{"authorization": "Bearer public-token"})
	if list.StatusCode != http.StatusOK || !strings.Contains(list.Body, `"totalSize":1`) {
		t.Fatalf("list tasks = %d %s", list.StatusCode, list.Body)
	}
	subscribe := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/"+sendResp.Task.ID+"/subscribe", "", map[string]string{"authorization": "Bearer public-token"})
	if subscribe.StatusCode != http.StatusOK || !strings.Contains(subscribe.Body, "event: task") {
		t.Fatalf("subscribe = %d %s", subscribe.StatusCode, subscribe.Body)
	}
	wrongSubscribeMethod := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/tasks/"+sendResp.Task.ID+"/subscribe", "", map[string]string{"authorization": "Bearer public-token"})
	if wrongSubscribeMethod.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post subscribe = %d %s", wrongSubscribeMethod.StatusCode, wrongSubscribeMethod.Body)
	}
	wrongCancelMethod := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/"+sendResp.Task.ID+":cancel", "", map[string]string{"authorization": "Bearer public-token"})
	if wrongCancelMethod.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("get cancel = %d %s", wrongCancelMethod.StatusCode, wrongCancelMethod.Body)
	}
	cancelResp := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/tasks/"+sendResp.Task.ID+":cancel", "", map[string]string{"authorization": "Bearer public-token"})
	if cancelResp.StatusCode != http.StatusBadRequest || !strings.Contains(cancelResp.Body, "TaskNotCancelableError") {
		t.Fatalf("cancel terminal task = %d %s", cancelResp.StatusCode, cancelResp.Body)
	}

	rpcBody := `{"jsonrpc":"2.0","id":"get","method":"GetTask","params":{"id":"` + sendResp.Task.ID + `"}}`
	rpc := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/", rpcBody, map[string]string{"authorization": "Bearer public-token"})
	if rpc.StatusCode != http.StatusOK || !strings.Contains(rpc.Body, `"jsonrpc":"2.0"`) || !strings.Contains(rpc.Body, sendResp.Task.ID) {
		t.Fatalf("jsonrpc get task = %d %s", rpc.StatusCode, rpc.Body)
	}
	if adapterCalls != 1 {
		t.Fatalf("adapter calls = %d", adapterCalls)
	}
}

func TestPublicA2AServerPushConfig(t *testing.T) {
	webhookHits := make(chan publicA2AWebhookObservation, 4)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		webhookHits <- publicA2AWebhookObservation{
			Authorization: r.Header.Get("authorization"),
			Signature:     r.Header.Get("X-OpenLinker-Signature"),
			Body:          body,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	server := &PublicA2AServer{
		Host:               "127.0.0.1",
		Port:               0,
		Slug:               "push-agent",
		Name:               "Push Agent",
		Description:        "Local A2A push test agent",
		Token:              "public-token",
		AllowLocalPushURLs: true,
		Adapter: AdapterFunc(func(_ context.Context, input any, _ RunContext) (any, error) {
			return JSONMap{"ok": true, "echo": input}, nil
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())

	sendBody := `{
		"message":{"contextId":"ctx-push","role":"ROLE_USER","parts":[{"text":"hello push"}]},
		"configuration":{
			"pushNotificationConfig":{
				"url":"` + webhook.URL + `",
				"secret":"inline-secret",
				"authentication":{"scheme":"Bearer","credentials":"inline-token"},
				"eventTypes":["run.completed"],
				"metadata":{"case":"inline"}
			}
		}
	}`
	send := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/message:send", sendBody, map[string]string{"authorization": "Bearer public-token"})
	if send.StatusCode != http.StatusOK {
		t.Fatalf("message send = %d %s", send.StatusCode, send.Body)
	}
	var sendResp struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(send.Body), &sendResp); err != nil {
		t.Fatal(err)
	}
	inlineHit := waitPublicA2AWebhook(t, webhookHits)
	if inlineHit.Authorization != "Bearer inline-token" {
		t.Fatalf("inline authorization = %q", inlineHit.Authorization)
	}
	if !openlinker.VerifyTaskCallbackSignature(inlineHit.Body, "inline-secret", inlineHit.Signature) {
		t.Fatalf("inline signature did not verify: %q", inlineHit.Signature)
	}
	assertPublicA2AWebhookPayload(t, inlineHit.Body, sendResp.Task.ID, "run.completed")

	createBody := `{
		"pushNotificationConfig":{
			"url":"` + webhook.URL + `",
			"secret":"set-secret",
			"authentication":{"scheme":"Bearer","credentials":"set-token"},
			"eventTypes":["run.completed"],
			"metadata":{"case":"set"}
		}
	}`
	create := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/tasks/"+sendResp.Task.ID+"/pushNotificationConfig", createBody, map[string]string{"authorization": "Bearer public-token"})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create push config = %d %s", create.StatusCode, create.Body)
	}
	if strings.Contains(create.Body, "set-token") || strings.Contains(create.Body, "set-secret") {
		t.Fatalf("push config response leaked credentials: %s", create.Body)
	}
	var created struct {
		ID             string `json:"id"`
		Authentication struct {
			Scheme      string `json:"scheme"`
			Credentials string `json:"credentials"`
		} `json:"authentication"`
	}
	if err := json.Unmarshal([]byte(create.Body), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Authentication.Scheme != "Bearer" || created.Authentication.Credentials != "" {
		t.Fatalf("created push config = %#v", created)
	}
	setHit := waitPublicA2AWebhook(t, webhookHits)
	if setHit.Authorization != "Bearer set-token" {
		t.Fatalf("set authorization = %q", setHit.Authorization)
	}
	if !openlinker.VerifyTaskCallbackSignature(setHit.Body, "set-secret", setHit.Signature) {
		t.Fatalf("set signature did not verify: %q", setHit.Signature)
	}
	assertPublicA2AWebhookPayload(t, setHit.Body, sendResp.Task.ID, "run.completed")

	list := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/"+sendResp.Task.ID+"/pushNotificationConfig", "", map[string]string{"authorization": "Bearer public-token"})
	if list.StatusCode != http.StatusOK || !strings.Contains(list.Body, created.ID) {
		t.Fatalf("list push configs = %d %s", list.StatusCode, list.Body)
	}
	get := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/"+sendResp.Task.ID+"/pushNotificationConfig/"+created.ID, "", map[string]string{"authorization": "Bearer public-token"})
	if get.StatusCode != http.StatusOK || !strings.Contains(get.Body, created.ID) || strings.Contains(get.Body, "set-token") {
		t.Fatalf("get push config = %d %s", get.StatusCode, get.Body)
	}
	rpcListBody := `{"jsonrpc":"2.0","id":"list-push","method":"ListTaskPushNotificationConfigs","params":{"id":"` + sendResp.Task.ID + `"}}`
	rpcList := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/", rpcListBody, map[string]string{"authorization": "Bearer public-token"})
	if rpcList.StatusCode != http.StatusOK || !strings.Contains(rpcList.Body, created.ID) {
		t.Fatalf("jsonrpc list push = %d %s", rpcList.StatusCode, rpcList.Body)
	}
	rpcDeleteBody := `{"jsonrpc":"2.0","id":"delete-push","method":"DeleteTaskPushNotificationConfig","params":{"id":"` + sendResp.Task.ID + `","pushNotificationConfigId":"` + created.ID + `"}}`
	rpcDelete := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/", rpcDeleteBody, map[string]string{"authorization": "Bearer public-token"})
	if rpcDelete.StatusCode != http.StatusOK || strings.Contains(rpcDelete.Body, `"error"`) {
		t.Fatalf("jsonrpc delete push = %d %s", rpcDelete.StatusCode, rpcDelete.Body)
	}
	listAfterDelete := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/"+sendResp.Task.ID+"/pushNotificationConfig", "", map[string]string{"authorization": "Bearer public-token"})
	if listAfterDelete.StatusCode != http.StatusOK || strings.Contains(listAfterDelete.Body, created.ID) {
		t.Fatalf("list after delete = %d %s", listAfterDelete.StatusCode, listAfterDelete.Body)
	}
}

func TestPublicA2AFromEnv(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_API_BASE":                                    "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":                                 "ol_agent_public",
		"OPENLINKER_AGENT_NODE_CONNECTOR":                        "runtime_pull",
		"OPENLINKER_AGENT_NODE_ADAPTER":                          "command",
		"OPENLINKER_AGENT_NODE_COMMAND":                          "/bin/echo",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A":                       "true",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_HOST":                  "127.0.0.1",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_PORT":                  "0",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_SLUG":                  "env-agent",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_NAME":                  "Env Agent",
		"OPENLINKER_PUBLIC_A2A_TOKEN":                            "env-token",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_ALLOW_LOCAL_PUSH_URLS": "true",
		"OPENLINKER_AGENT_NODE_PULL_WAIT_SECONDS":                "1",
		"OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS":                "1",
		"OPENLINKER_AGENT_NODE_STOP_ON_EMPTY":                    "true",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_DESCRIPTION":           "Env description",
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.PublicA2A == nil || node.PublicA2A.Slug != "env-agent" || node.PublicA2A.Token != "env-token" {
		t.Fatalf("public A2A config = %#v", node.PublicA2A)
	}
	if !node.PublicA2A.AllowLocalPushURLs {
		t.Fatalf("public A2A local push URL flag was not applied")
	}
}

func TestPublicA2AServerRequiresTokenOnNonLoopback(t *testing.T) {
	server := &PublicA2AServer{
		Host: "0.0.0.0",
		Port: 0,
		Adapter: AdapterFunc(func(context.Context, any, RunContext) (any, error) {
			return JSONMap{"ok": true}, nil
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("non-loopback without token error = %v", err)
	}
}

func TestPublicA2AServerBoundsStoredTasksAndPushConfigs(t *testing.T) {
	server := &PublicA2AServer{
		AllowLocalPushURLs: true,
		Adapter: AdapterFunc(func(_ context.Context, input any, _ RunContext) (any, error) {
			return JSONMap{"output": input}, nil
		}),
	}
	params := openlinker.A2AMessageSendParams{
		Message: openlinker.A2AMessage{
			Role:  "ROLE_USER",
			Parts: []map[string]any{{"text": "hello"}},
		},
	}
	for i := 0; i < maxPublicA2ATasks+3; i++ {
		if _, err := server.runMessage(context.Background(), params); err != nil {
			t.Fatal(err)
		}
	}
	if len(server.tasks) != maxPublicA2ATasks {
		t.Fatalf("stored task count = %d", len(server.tasks))
	}

	taskID := "task-push-limit"
	server.tasks[taskID] = &publicA2ATask{
		ID:        taskID,
		ContextID: "ctx-push-limit",
		State:     "TASK_STATE_RUNNING",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	for i := 0; i < maxPublicA2APushConfigsPerTask; i++ {
		_, err := server.createPushConfig(context.Background(), openlinker.A2ATaskPushConfigParams{
			TaskID: taskID,
			PushNotificationConfig: openlinker.A2APushNotificationConfig{
				ID:  fmt.Sprintf("push-%d", i),
				URL: "http://127.0.0.1/callback",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := server.createPushConfig(context.Background(), openlinker.A2ATaskPushConfigParams{
		TaskID: taskID,
		PushNotificationConfig: openlinker.A2APushNotificationConfig{
			ID:  "push-over-limit",
			URL: "http://127.0.0.1/callback",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "too many push configs") {
		t.Fatalf("push config limit error = %v", err)
	}
}

func TestPublicA2APushURLPolicy(t *testing.T) {
	if err := validatePublicA2APushURL(context.Background(), "http://127.0.0.1/callback", true); err != nil {
		t.Fatalf("loopback local push URL should be allowed with explicit flag: %v", err)
	}
	for _, raw := range []string{
		"http://127.0.0.1/callback",
		"http://169.254.169.254/latest/meta-data",
		"https://169.254.169.254/latest/meta-data",
		"https://198.18.1.32/callback",
		"https://100.64.0.1/callback",
		"https://192.0.2.1/callback",
		"https://[::1]/callback",
		"https://user:pass@example.com/callback",
	} {
		if err := validatePublicA2APushURL(context.Background(), raw, false); err == nil {
			t.Fatalf("push URL %q should have been rejected", raw)
		}
	}
}

func TestPublicA2APushHTTPClientRejectsUnsafeRedirect(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer redirector.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirector.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newPublicA2APushHTTPClient(time.Second, true).Do(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "public IPs") {
		t.Fatalf("unsafe redirect error = %v", err)
	}
}

type publicA2AHTTPResult struct {
	StatusCode int
	Body       string
}

type publicA2AWebhookObservation struct {
	Authorization string
	Signature     string
	Body          []byte
}

func doPublicA2ARequest(t *testing.T, method, url, body string, headers map[string]string) publicA2AHTTPResult {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("content-type", "application/a2a+json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return publicA2AHTTPResult{StatusCode: resp.StatusCode, Body: string(raw)}
}

func waitPublicA2AWebhook(t *testing.T, hits <-chan publicA2AWebhookObservation) publicA2AWebhookObservation {
	t.Helper()
	select {
	case hit := <-hits:
		return hit
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook hit")
	}
	return publicA2AWebhookObservation{}
}

func assertPublicA2AWebhookPayload(t *testing.T, body []byte, taskID, eventType string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["task_id"] != taskID || payload["event_type"] != eventType || payload["source"] != "openlinker-agent-node-public-a2a" {
		t.Fatalf("webhook payload = %#v", payload)
	}
}
