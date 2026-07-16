package agentnode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicA2AServerKeepsLocalCardsAndProxiesA2AToCore(t *testing.T) {
	observations := make(chan publicA2ACoreObservation, 16)
	core, mtls := newPublicA2ATestCore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		peerName := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			peerName = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		observations <- publicA2ACoreObservation{
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.RawQuery,
			Authorization: r.Header.Get("Authorization"),
			Cookie:        r.Header.Get("Cookie"),
			SDK:           r.Header.Get("X-OpenLinker-SDK"),
			PeerName:      peerName,
			Body:          string(body),
			Host:          r.Host,
			Header:        r.Header.Clone(),
		}
		w.Header().Set("X-Core-A2A", "canonical")
		w.Header().Set("Connection", "X-Core-Hop")
		w.Header().Set("X-Core-Hop", "must-not-leak")
		w.Header().Set("Keep-Alive", "timeout=5")
		if strings.HasSuffix(r.URL.Path, "/message:stream") || strings.HasSuffix(r.URL.Path, "/subscribe") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: task\ndata: {\"source\":\"core\"}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(25 * time.Millisecond)
			_, _ = io.WriteString(w, "event: status\ndata: {\"state\":\"completed\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/a2a+json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"source": "core", "path": r.URL.Path})
	}))
	defer core.Close()

	server := &PublicA2AServer{
		Host:        "127.0.0.1",
		Port:        0,
		Slug:        "local-agent",
		Name:        "Local Agent",
		Description: "Local A2A test agent",
		Token:       "public-token",
	}
	node := &Node{
		RuntimeURL:     core.URL,
		AgentToken:     "ol_agent_runtime",
		MTLSCertFile:   mtls.CertFile,
		MTLSKeyFile:    mtls.KeyFile,
		MTLSCAFile:     mtls.CAFile,
		MTLSServerName: "runtime.test",
		PublicA2A:      server,
	}
	proxy, err := node.newPublicA2AProxy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.setProxy(proxy); err != nil {
		proxy.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	server.mu.RLock()
	writeTimeout := server.server.WriteTimeout
	server.mu.RUnlock()
	if writeTimeout != 0 {
		t.Fatalf("public A2A listener write timeout = %s; SSE must not be truncated by a listener deadline", writeTimeout)
	}

	card := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/.well-known/agent-card.json", "", nil)
	if card.StatusCode != http.StatusOK || !strings.Contains(card.Body, `"Local Agent"`) || !strings.Contains(card.Body, server.BaseURL()) {
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
			t.Fatalf("public AgentNode must not advertise gRPC: %#v", supported)
		}
	}
	assertNoPublicA2ACoreRequest(t, observations)

	unauthorizedCard := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/extendedAgentCard", "", nil)
	if unauthorizedCard.StatusCode != http.StatusUnauthorized {
		t.Fatalf("extended card without auth = %d %s", unauthorizedCard.StatusCode, unauthorizedCard.Body)
	}
	extended := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/extendedAgentCard", "", map[string]string{"Authorization": "Bearer public-token"})
	if extended.StatusCode != http.StatusOK || !strings.Contains(extended.Body, `"extended":true`) {
		t.Fatalf("extended card = %d %s", extended.StatusCode, extended.Body)
	}
	unauthorizedMessage := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/message:send", `{}`, nil)
	if unauthorizedMessage.StatusCode != http.StatusUnauthorized || unauthorizedMessage.Header.Get("Content-Type") != "application/a2a+json" || !strings.Contains(unauthorizedMessage.Body, "AuthRequiredError") {
		t.Fatalf("message without auth = %d %s", unauthorizedMessage.StatusCode, unauthorizedMessage.Body)
	}
	assertNoPublicA2ACoreRequest(t, observations)

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		statusCode int
		stream     bool
	}{
		{name: "message", method: http.MethodPost, path: "/message:send?version=0.3", body: `{"message":{"messageId":"msg-1"}}`, statusCode: http.StatusAccepted},
		{name: "task", method: http.MethodGet, path: "/tasks/task-1", statusCode: http.StatusAccepted},
		{name: "push", method: http.MethodPost, path: "/tasks/task-1/pushNotificationConfig", body: `{"url":"https://callback.example.test"}`, statusCode: http.StatusAccepted},
		{name: "jsonrpc", method: http.MethodPost, path: "/", body: `{"jsonrpc":"2.0","id":"rpc-1","method":"GetTask","params":{"id":"task-1"}}`, statusCode: http.StatusAccepted},
		{name: "message stream", method: http.MethodPost, path: "/message:stream", body: `{"message":{"messageId":"msg-stream"}}`, statusCode: http.StatusOK, stream: true},
		{name: "task stream", method: http.MethodGet, path: "/tasks/task-1/subscribe", statusCode: http.StatusOK, stream: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := doPublicA2ARequest(t, tc.method, server.BaseURL()+tc.path, tc.body, map[string]string{
				"Authorization":       "Bearer public-token",
				"Cookie":              "browser-secret=must-not-leak",
				"Connection":          "keep-alive, Authorization, X-OpenLinker-SDK, X-Remove-Me, Upgrade",
				"Keep-Alive":          "timeout=30",
				"Proxy-Authorization": "Basic must-not-leak",
				"Proxy-Connection":    "keep-alive",
				"Te":                  "gzip",
				"Upgrade":             "websocket",
				"X-OpenLinker-SDK":    "attacker-controlled",
				"X-Forwarded-For":     "198.51.100.10",
				"Forwarded":           "for=198.51.100.10",
				"X-Remove-Me":         "connection-token-secret",
			})
			if response.StatusCode != tc.statusCode || response.Header.Get("X-Core-A2A") != "canonical" {
				t.Fatalf("response = %d headers=%v body=%s", response.StatusCode, response.Header, response.Body)
			}
			if tc.stream {
				if response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Body, "event: task") || !strings.Contains(response.Body, "event: status") {
					t.Fatalf("stream response = headers=%v body=%s", response.Header, response.Body)
				}
			} else if !strings.Contains(response.Body, `"source":"core"`) {
				t.Fatalf("response was not passed through from Core: %s", response.Body)
			}

			observation := waitPublicA2ACoreRequest(t, observations)
			expectedPath := "/api/v1/agent-runtime/a2a-proxy/agents/local-agent"
			requestPath := strings.SplitN(tc.path, "?", 2)[0]
			if requestPath != "/" {
				expectedPath += requestPath
			}
			expectedQuery := ""
			if parts := strings.SplitN(tc.path, "?", 2); len(parts) == 2 {
				expectedQuery = parts[1]
			}
			if observation.Method != tc.method || observation.Path != expectedPath || observation.Query != expectedQuery {
				t.Fatalf("Core method/path/query = %s %s?%s", observation.Method, observation.Path, observation.Query)
			}
			if observation.Authorization != "Bearer ol_agent_runtime" || observation.Cookie != "" {
				t.Fatalf("Core auth/cookie = %q / %q", observation.Authorization, observation.Cookie)
			}
			if observation.SDK != "openlinker-go/runtime-worker" || observation.PeerName != "agent-node-test" {
				t.Fatalf("Core SDK/mTLS identity = %q / %q", observation.SDK, observation.PeerName)
			}
			if observation.Body != tc.body {
				t.Fatalf("Core request body = %q, want %q", observation.Body, tc.body)
			}
			for _, name := range []string{
				"Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Connection",
				"Te", "Trailer", "Upgrade", "X-Forwarded-For", "Forwarded", "X-Remove-Me",
			} {
				if value := observation.Header.Get(name); value != "" {
					t.Fatalf("hop-by-hop or spoofable header %s leaked to Core: %q", name, value)
				}
			}
			if observation.Host != strings.TrimPrefix(core.URL, "https://") {
				t.Fatalf("Core Host = %q, want %q", observation.Host, strings.TrimPrefix(core.URL, "https://"))
			}
			if response.Header.Get("X-Core-Hop") != "" || response.Header.Get("Keep-Alive") != "" || response.Header.Get("Connection") != "" {
				t.Fatalf("Core response hop-by-hop headers leaked to client: %v", response.Header)
			}
		})
	}

	tooLarge := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/message:send", strings.Repeat("x", int(maxPublicA2ARequestBodyBytes)+1), map[string]string{
		"Authorization": "Bearer public-token",
	})
	if tooLarge.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(tooLarge.Body, "RequestBodyTooLargeError") {
		t.Fatalf("oversized request = %d %s", tooLarge.StatusCode, tooLarge.Body)
	}
	assertNoPublicA2ACoreRequest(t, observations)

	core.Close()
	unavailable := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/message:send", `{}`, map[string]string{
		"Authorization": "Bearer public-token",
	})
	if unavailable.StatusCode != http.StatusBadGateway || unavailable.Header.Get("Content-Type") != "application/a2a+json" || !strings.Contains(unavailable.Body, `"code":"A2A_PROXY_UNAVAILABLE"`) {
		t.Fatalf("unavailable Core response = %d %s", unavailable.StatusCode, unavailable.Body)
	}
	for _, secret := range []string{"ol_agent_runtime", "runtime.test", core.URL} {
		if strings.Contains(unavailable.Body, secret) {
			t.Fatalf("unavailable Core response leaked %q: %s", secret, unavailable.Body)
		}
	}
	rpcUnavailable := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/", `{"jsonrpc":"2.0","id":"offline","method":"GetTask","params":{"id":"task-1"}}`, map[string]string{
		"Authorization": "Bearer public-token",
	})
	var rpcUnavailableEnvelope publicA2AJSONRPCResponse
	if err := json.Unmarshal([]byte(rpcUnavailable.Body), &rpcUnavailableEnvelope); err != nil {
		t.Fatal(err)
	}
	if rpcUnavailable.StatusCode != http.StatusOK || rpcUnavailable.Header.Get("Content-Type") != "application/a2a+json" || string(rpcUnavailableEnvelope.ID) != `"offline"` || rpcUnavailableEnvelope.Error == nil || rpcUnavailableEnvelope.Error.Code != -32013 {
		t.Fatalf("JSON-RPC unavailable Core response = %d headers=%v envelope=%#v body=%s", rpcUnavailable.StatusCode, rpcUnavailable.Header, rpcUnavailableEnvelope, rpcUnavailable.Body)
	}
	data, ok := rpcUnavailableEnvelope.Error.Data.(map[string]any)
	if !ok || data["code"] != "A2A_PROXY_UNAVAILABLE" {
		t.Fatalf("JSON-RPC unavailable Core error data = %#v", rpcUnavailableEnvelope.Error.Data)
	}
	for _, secret := range []string{"ol_agent_runtime", "runtime.test", core.URL} {
		if strings.Contains(rpcUnavailable.Body, secret) {
			t.Fatalf("JSON-RPC unavailable Core response leaked %q: %s", secret, rpcUnavailable.Body)
		}
	}
}

func TestPublicA2AServerJSONRPCBoundarySnapshots(t *testing.T) {
	proxy := &recordingPublicA2AProxy{}
	server := &PublicA2AServer{
		Host:        "127.0.0.1",
		Port:        0,
		Slug:        "snapshot-agent",
		Name:        "Snapshot Agent",
		Description: "JSON-RPC compatibility snapshot",
		Token:       "public-token",
	}
	if err := server.setProxy(proxy); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())

	unauthorized := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/", `{"jsonrpc":"2.0","id":"must-not-echo","method":"GetTask","params":{"id":"task-1"}}`, nil)
	var denied publicA2AJSONRPCResponse
	if err := json.Unmarshal([]byte(unauthorized.Body), &denied); err != nil {
		t.Fatal(err)
	}
	if unauthorized.StatusCode != http.StatusOK || unauthorized.Header.Get("Content-Type") != "application/a2a+json" || denied.JSONRPC != "2.0" || string(denied.ID) != "null" || denied.Error == nil || denied.Error.Code != -32010 || denied.Error.Message != "authorization required" {
		t.Fatalf("root JSON-RPC unauthorized snapshot = %d headers=%v envelope=%#v body=%s", unauthorized.StatusCode, unauthorized.Header, denied, unauthorized.Body)
	}
	wrongMethod := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/", "", map[string]string{"Authorization": "Bearer public-token"})
	if wrongMethod.StatusCode != http.StatusMethodNotAllowed || wrongMethod.Header.Get("Content-Type") != "application/a2a+json" || !strings.Contains(wrongMethod.Body, "UnsupportedOperationError") {
		t.Fatalf("root non-POST snapshot = %d headers=%v body=%s", wrongMethod.StatusCode, wrongMethod.Header, wrongMethod.Body)
	}

	for _, tc := range []struct {
		method     string
		id         string
		expectedID string
	}{
		{method: "GetExtendedAgentCard", id: `"card-modern"`, expectedID: `"card-modern"`},
		{method: "agent/getExtendedCard", id: `7`, expectedID: `7`},
	} {
		body := `{"jsonrpc":"2.0","id":` + tc.id + `,"method":"` + tc.method + `","params":{}}`
		response := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+"/", body, map[string]string{"Authorization": "Bearer public-token"})
		var envelope publicA2AJSONRPCResponse
		if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
			t.Fatal(err)
		}
		card, ok := envelope.Result.(map[string]any)
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/a2a+json" || envelope.JSONRPC != "2.0" || string(envelope.ID) != tc.expectedID || envelope.Error != nil || !ok {
			t.Fatalf("extended card %s snapshot = %d headers=%v envelope=%#v body=%s", tc.method, response.StatusCode, response.Header, envelope, response.Body)
		}
		if card["url"] != server.BaseURL() {
			t.Fatalf("extended card %s URL = %#v, want AgentNode URL %q", tc.method, card["url"], server.BaseURL())
		}
		openLinkerMetadata, ok := card["openlinker"].(map[string]any)
		if !ok || openLinkerMetadata["extended"] != true || openLinkerMetadata["agent_node"] != true {
			t.Fatalf("extended card %s metadata = %#v", tc.method, card["openlinker"])
		}
	}
	if proxy.requests.Load() != 0 {
		t.Fatalf("local JSON-RPC boundary responses unexpectedly reached Core proxy %d times", proxy.requests.Load())
	}
}

func TestPublicA2AProxyUnavailableFlushWaitsForBoundaryNormalization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		request    string
		statusCode int
		body       string
	}{
		{
			name:       "http",
			path:       "/message:send",
			request:    `{"message":{"messageId":"flush-http"}}`,
			statusCode: http.StatusBadGateway,
			body:       "{\"error\":{\"code\":\"A2A_PROXY_UNAVAILABLE\",\"message\":\"Core A2A service is unavailable\"}}\n",
		},
		{
			name:       "jsonrpc",
			path:       "/",
			request:    `{"jsonrpc":"2.0","id":"flush-rpc","method":"GetTask","params":{"id":"task-1"}}`,
			statusCode: http.StatusOK,
			body:       "{\"jsonrpc\":\"2.0\",\"id\":\"flush-rpc\",\"error\":{\"code\":-32013,\"message\":\"Core A2A service is unavailable\",\"data\":{\"code\":\"A2A_PROXY_UNAVAILABLE\",\"http_status\":502}}}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := &handlerPublicA2AProxy{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := `{"error":{"code":"A2A_PROXY_UNAVAILABLE","message":"Core A2A service is unavailable"}}`
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, body)
				w.(http.Flusher).Flush()
			})}
			server := &PublicA2AServer{Host: "127.0.0.1", Port: 0, Token: "public-token"}
			if err := server.setProxy(proxy); err != nil {
				t.Fatal(err)
			}
			if err := server.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer server.Stop(context.Background())

			response := doPublicA2ARequest(t, http.MethodPost, server.BaseURL()+tc.path, tc.request, map[string]string{
				"Authorization": "Bearer public-token",
			})
			if response.StatusCode != tc.statusCode || response.Header.Get("Content-Type") != "application/a2a+json" || response.Body != tc.body {
				t.Fatalf("normalized flushed response = %d headers=%v body=%q, want status=%d body=%q", response.StatusCode, response.Header, response.Body, tc.statusCode, tc.body)
			}
		})
	}
}

func TestPublicA2AProxyResponseWriterKeepsSuccessfulSSEFlushStreaming(t *testing.T) {
	target := httptest.NewRecorder()
	response := &publicA2AProxyResponseWriter{target: target}
	response.Header().Set("Content-Type", "text/event-stream")
	response.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(response, "event: task\ndata: {}\n\n"); err != nil {
		t.Fatal(err)
	}
	response.Flush()

	if !target.Flushed || target.Code != http.StatusOK || target.Body.String() != "event: task\ndata: {}\n\n" {
		t.Fatalf("successful SSE flush: flushed=%v status=%d body=%q", target.Flushed, target.Code, target.Body.String())
	}
}

func TestRuntimeA2AProxyUnavailableContractAcceptsOnlyExactSDKCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "exact SDK code", body: `{"error":{"code":"A2A_PROXY_UNAVAILABLE","message":"Core A2A service is unavailable"}}`, want: true},
		{name: "near miss", body: `{"error":{"code":"A2A_PROXY_UNAVAILABLE_RETRY"}}`},
		{name: "Core Runtime unauthorized envelope", body: `{"type":"runtime.error","body":{"code":"UNAUTHORIZED","message":"runtime credential rejected"}}`},
		{name: "Core Runtime forbidden envelope", body: `{"type":"runtime.error","body":{"code":"FORBIDDEN","message":"runtime transport forbidden"}}`},
		{name: "Core Runtime unavailable envelope", body: `{"type":"runtime.error","body":{"code":"SERVICE_UNAVAILABLE","message":"runtime unavailable"}}`},
		{name: "invalid JSON", body: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeA2AProxyUnavailable([]byte(tc.body)); got != tc.want {
				t.Fatalf("runtimeA2AProxyUnavailable(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestPublicA2AFromEnvContainsOnlyListenerAppearanceAndToken(t *testing.T) {
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_URL":                               "https://api.example.test",
		"OPENLINKER_AGENT_TOKEN":                       "ol_agent_public",
		"OPENLINKER_AGENT_NODE_ADAPTER":                "command",
		"OPENLINKER_AGENT_NODE_COMMAND":                "/bin/echo",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A":             "true",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_HOST":        "127.0.0.1",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_PORT":        "0",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_SLUG":        "env-agent",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_NAME":        "Env Agent",
		"OPENLINKER_AGENT_NODE_PUBLIC_A2A_DESCRIPTION": "Env description",
		"OPENLINKER_PUBLIC_A2A_TOKEN":                  "env-token",
		"OPENLINKER_AGENT_NODE_CLAIM_WAIT_SECONDS":     "1",
		"OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS":      "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.PublicA2A == nil || node.PublicA2A.Slug != "env-agent" || node.PublicA2A.Token != "env-token" || node.PublicA2A.Name != "Env Agent" {
		t.Fatalf("public A2A config = %#v", node.PublicA2A)
	}
}

func TestPublicA2AServerRequiresTokenOnNonLoopback(t *testing.T) {
	server := &PublicA2AServer{Host: "0.0.0.0", Port: 0}
	if err := server.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("non-loopback without token error = %v", err)
	}
}

func TestPublicA2AServerRequiresSDKProxy(t *testing.T) {
	server := &PublicA2AServer{Host: "127.0.0.1", Port: 0}
	if err := server.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "SDK Runtime A2A proxy") {
		t.Fatalf("missing proxy error = %v", err)
	}
}

func TestNodeStopAdaptersClosesSDKProxy(t *testing.T) {
	proxy := &recordingPublicA2AProxy{}
	server := &PublicA2AServer{Host: "127.0.0.1", Port: 0}
	if err := server.setProxy(proxy); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("duplicate start error = %v", err)
	}
	if proxy.closes.Load() != 0 {
		t.Fatalf("duplicate Start closed active proxy %d times", proxy.closes.Load())
	}
	node := &Node{PublicA2A: server}
	if err := node.stopAdapters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if proxy.closes.Load() != 1 {
		t.Fatalf("proxy closes = %d", proxy.closes.Load())
	}
}

func TestPublicA2AServerStartFailureClosesProxyExactlyOnce(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	proxy := &recordingPublicA2AProxy{}
	server := &PublicA2AServer{Host: "127.0.0.1", Port: port}
	if err := server.setProxy(proxy); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("expected occupied listener address to fail")
	}
	server.mu.RLock()
	attachedProxy := server.proxy
	startedServer := server.server
	server.mu.RUnlock()
	if attachedProxy != nil || startedServer != nil {
		t.Fatalf("failed Start retained proxy/server: proxy=%T server=%p", attachedProxy, startedServer)
	}
	if proxy.closes.Load() != 1 {
		t.Fatalf("failed Start proxy closes = %d", proxy.closes.Load())
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if proxy.closes.Load() != 1 {
		t.Fatalf("Stop after failed Start closed proxy again: %d", proxy.closes.Load())
	}
}

func TestPublicA2AServerRestartRetiresOldContextGeneration(t *testing.T) {
	firstProxy := &recordingPublicA2AProxy{}
	server := &PublicA2AServer{Host: "127.0.0.1", Port: 0}
	if err := server.setProxy(firstProxy); err != nil {
		t.Fatal(err)
	}
	firstContext, cancelFirst := context.WithCancel(context.Background())
	if err := server.Start(firstContext); err != nil {
		cancelFirst()
		t.Fatal(err)
	}
	server.mu.RLock()
	firstLifetimeDone := server.lifetimeDone
	server.mu.RUnlock()
	if err := server.Stop(context.Background()); err != nil {
		cancelFirst()
		t.Fatal(err)
	}
	if firstProxy.closes.Load() != 1 {
		cancelFirst()
		t.Fatalf("first generation proxy closes = %d", firstProxy.closes.Load())
	}

	secondProxy := &recordingPublicA2AProxy{}
	if err := server.setProxy(secondProxy); err != nil {
		cancelFirst()
		t.Fatal(err)
	}
	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	if err := server.Start(secondContext); err != nil {
		cancelFirst()
		t.Fatal(err)
	}
	defer server.Stop(context.Background())

	// The old parent may be canceled after the same server object has started a
	// new generation. Its retired watcher must not stop or close the new one.
	cancelFirst()
	select {
	case <-firstLifetimeDone:
	case <-time.After(time.Second):
		t.Fatal("first generation context watcher did not retire")
	}
	response := doPublicA2ARequest(t, http.MethodGet, server.BaseURL()+"/tasks/task-2", "", nil)
	if response.StatusCode != http.StatusNoContent || secondProxy.requests.Load() != 1 {
		t.Fatalf("second generation response=%d requests=%d", response.StatusCode, secondProxy.requests.Load())
	}
	if secondProxy.closes.Load() != 0 {
		t.Fatalf("old context closed second generation proxy %d times", secondProxy.closes.Load())
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if secondProxy.closes.Load() != 1 {
		t.Fatalf("second generation proxy closes = %d", secondProxy.closes.Load())
	}
}

type publicA2AHTTPResult struct {
	StatusCode int
	Header     http.Header
	Body       string
}

type publicA2ACoreObservation struct {
	Method        string
	Path          string
	Query         string
	Authorization string
	Cookie        string
	SDK           string
	PeerName      string
	Body          string
	Host          string
	Header        http.Header
}

type publicA2AMTLSFiles struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

type recordingPublicA2AProxy struct {
	closes   atomic.Int32
	requests atomic.Int32
}

func (proxy *recordingPublicA2AProxy) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	proxy.requests.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

func (proxy *recordingPublicA2AProxy) Close() {
	proxy.closes.Add(1)
}

type handlerPublicA2AProxy struct {
	handler http.Handler
	closes  atomic.Int32
}

func (proxy *handlerPublicA2AProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proxy.handler.ServeHTTP(w, r)
}

func (proxy *handlerPublicA2AProxy) Close() {
	proxy.closes.Add(1)
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
		req.Header.Set("Content-Type", "application/a2a+json")
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
	return publicA2AHTTPResult{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: string(raw)}
}

func waitPublicA2ACoreRequest(t *testing.T, observations <-chan publicA2ACoreObservation) publicA2ACoreObservation {
	t.Helper()
	select {
	case observation := <-observations:
		return observation
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Core A2A proxy request")
		return publicA2ACoreObservation{}
	}
}

func assertNoPublicA2ACoreRequest(t *testing.T, observations <-chan publicA2ACoreObservation) {
	t.Helper()
	select {
	case observation := <-observations:
		t.Fatalf("unexpected Core A2A request: %#v", observation)
	default:
	}
}

func newPublicA2ATestCore(t *testing.T, handler http.Handler) (*httptest.Server, publicA2AMTLSFiles) {
	t.Helper()
	caPEM, serverCertificate, clientCertPEM, clientKeyPEM := publicA2ATestPKI(t)
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append public A2A test CA")
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}
	server.StartTLS()

	directory := t.TempDir()
	files := publicA2AMTLSFiles{
		CertFile: filepath.Join(directory, "client.crt"),
		KeyFile:  filepath.Join(directory, "client.key"),
		CAFile:   filepath.Join(directory, "ca.crt"),
	}
	for path, raw := range map[string][]byte{
		files.CertFile: clientCertPEM,
		files.KeyFile:  clientKeyPEM,
		files.CAFile:   caPEM,
	} {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			server.Close()
			t.Fatal(err)
		}
	}
	return server, files
}

func publicA2ATestPKI(t *testing.T) ([]byte, tls.Certificate, []byte, []byte) {
	t.Helper()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "OpenLinker Public A2A Test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	issue := func(serial int64, commonName string, usage x509.ExtKeyUsage, dnsNames []string) ([]byte, []byte, tls.Certificate) {
		publicKey, privateKey, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			t.Fatal(generateErr)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: commonName},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			DNSNames:     dnsNames,
		}
		certificateDER, createErr := x509.CreateCertificate(rand.Reader, template, caCertificate, publicKey, caPrivate)
		if createErr != nil {
			t.Fatal(createErr)
		}
		privateDER, marshalErr := x509.MarshalPKCS8PrivateKey(privateKey)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
		privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
		pair, pairErr := tls.X509KeyPair(certificatePEM, privatePEM)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		return certificatePEM, privatePEM, pair
	}
	_, _, serverCertificate := issue(2, "runtime.test", x509.ExtKeyUsageServerAuth, []string{"runtime.test"})
	clientCertificatePEM, clientPrivatePEM, _ := issue(3, "agent-node-test", x509.ExtKeyUsageClientAuth, nil)
	return caPEM, serverCertificate, clientCertificatePEM, clientPrivatePEM
}
