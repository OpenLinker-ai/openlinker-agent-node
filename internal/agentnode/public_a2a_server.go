package agentnode

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxPublicA2ARequestBodyBytes int64 = 1024 * 1024
const maxPublicA2AProxyErrorBodyBytes = 64 * 1024

var publicA2AHopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// publicA2AProxy is intentionally limited to the SDK compatibility proxy.
// AgentNode owns only the local listener and Agent Card appearance; Core owns
// all A2A messages, tasks, runs, streams, and push notification state.
// Runtime listener authentication, authorization, and availability envelopes
// are normalized by the SDK to the exact A2A_PROXY_UNAVAILABLE contract before
// they cross this interface; AgentNode must not duplicate Runtime wire logic.
type publicA2AProxy interface {
	http.Handler
	Close()
}

type PublicA2AServer struct {
	Host        string
	Port        int
	Slug        string
	Name        string
	Description string
	Token       string

	mu           sync.RWMutex
	server       *http.Server
	listener     net.Listener
	baseURL      string
	proxy        publicA2AProxy
	lifetimeStop chan struct{}
	lifetimeDone chan struct{}
}

type publicA2AJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

type publicA2AJSONRPCResponse struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      json.RawMessage        `json:"id"`
	Result  any                    `json:"result,omitempty"`
	Error   *publicA2AJSONRPCError `json:"error,omitempty"`
}

type publicA2AJSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *PublicA2AServer) setProxy(proxy publicA2AProxy) error {
	if proxy == nil {
		return errors.New("public A2A server requires SDK Runtime A2A proxy")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return errors.New("public A2A server is already started")
	}
	if s.proxy != nil {
		return errors.New("public A2A server already has an SDK Runtime A2A proxy")
	}
	s.proxy = proxy
	return nil
}

func (s *PublicA2AServer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.server != nil {
		s.mu.Unlock()
		return errors.New("public A2A server is already started")
	}
	if s.Host == "" {
		s.Host = "127.0.0.1"
	}
	s.Slug = strings.TrimSpace(s.Slug)
	if s.Slug == "" {
		s.Slug = "agent-node"
	}
	if s.Name == "" {
		s.Name = s.Slug
	}
	if !publicA2AHostIsLoopback(s.Host) && strings.TrimSpace(s.Token) == "" {
		return s.failStartLocked(errors.New("public A2A token is required when binding to non-loopback host"))
	}
	if s.proxy == nil {
		s.mu.Unlock()
		return errors.New("public A2A server requires SDK Runtime A2A proxy")
	}

	listenAddress := net.JoinHostPort(strings.Trim(strings.TrimSpace(s.Host), "[]"), strconv.Itoa(s.Port))
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return s.failStartLocked(err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return s.failStartLocked(errors.New("public A2A listener did not return a TCP address"))
	}
	baseURL := "http://" + net.JoinHostPort(address.IP.String(), strconv.Itoa(address.Port))

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("/extendedAgentCard", s.handleExtendedAgentCard)
	mux.HandleFunc("/", s.handleProxy)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Streaming responses are bounded by caller cancellation and Core, not
		// by a listener-wide write deadline that would truncate SSE sessions.
		WriteTimeout:   0,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}
	lifetimeStop := make(chan struct{})
	lifetimeDone := make(chan struct{})

	s.listener = listener
	s.baseURL = baseURL
	s.server = server
	s.lifetimeStop = lifetimeStop
	s.lifetimeDone = lifetimeDone
	s.mu.Unlock()

	go func() { // #nosec G118 -- shutdown uses a fresh bounded context after parent cancellation.
		defer close(lifetimeDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
			defer cancel()
			_ = s.stopGeneration(shutdownCtx, server)
		case <-lifetimeStop:
		}
	}()
	go func() {
		_ = server.Serve(listener)
	}()
	return nil
}

func (s *PublicA2AServer) failStartLocked(startErr error) error {
	proxy := s.proxy
	s.proxy = nil
	s.mu.Unlock()
	if proxy != nil {
		proxy.Close()
	}
	return startErr
}

func (s *PublicA2AServer) Stop(ctx context.Context) error {
	return s.stopGeneration(ctx, nil)
}

func (s *PublicA2AServer) stopGeneration(ctx context.Context, expectedServer *http.Server) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if expectedServer != nil && s.server != expectedServer {
		s.mu.Unlock()
		return nil
	}
	server := s.server
	proxy := s.proxy
	lifetimeStop := s.lifetimeStop
	s.server = nil
	s.listener = nil
	s.proxy = nil
	s.lifetimeStop = nil
	s.lifetimeDone = nil
	s.mu.Unlock()

	if lifetimeStop != nil {
		close(lifetimeStop)
	}
	var shutdownErr error
	if server != nil {
		shutdownErr = server.Shutdown(ctx)
	}
	if proxy != nil {
		proxy.Close()
	}
	return shutdownErr
}

func (s *PublicA2AServer) BaseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseURL
}

func (s *PublicA2AServer) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	writeA2AJSON(w, http.StatusOK, s.agentCard(false))
}

func (s *PublicA2AServer) handleExtendedAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeA2AError(w, http.StatusUnauthorized, "AuthRequiredError", "authorization required")
		return
	}
	writeA2AJSON(w, http.StatusOK, s.agentCard(true))
}

func (s *PublicA2AServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	jsonRPCRoot := r.URL.Path == "/"
	if jsonRPCRoot && r.Method != http.MethodPost {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	if !s.authorized(r) {
		if jsonRPCRoot {
			writePublicA2AJSONRPCError(w, json.RawMessage("null"), -32010, "authorization required", nil)
			return
		}
		writeA2AError(w, http.StatusUnauthorized, "AuthRequiredError", "authorization required")
		return
	}
	requestBody, ok := preparePublicA2AProxyRequest(w, r)
	if !ok {
		return
	}
	var jsonRPCRequest publicA2AJSONRPCRequest
	validJSONRPCRequest := false
	if jsonRPCRoot {
		jsonRPCRequest, validJSONRPCRequest = decodePublicA2AJSONRPCRequest(requestBody)
		if validJSONRPCRequest && publicA2AExtendedCardMethod(jsonRPCRequest.Method) {
			writePublicA2AJSONRPCResult(w, jsonRPCRequest.ID, s.agentCard(true))
			return
		}
	}
	s.mu.RLock()
	proxy := s.proxy
	s.mu.RUnlock()
	if proxy == nil {
		writeA2AError(w, http.StatusServiceUnavailable, "A2AProxyUnavailableError", "Core A2A proxy is unavailable")
		return
	}
	response := &publicA2AProxyResponseWriter{target: w}
	proxy.ServeHTTP(response, r)
	if jsonRPCRoot {
		response.finishJSONRPC(jsonRPCID(jsonRPCRequest.ID, validJSONRPCRequest))
	} else {
		response.finishHTTP()
	}
}

func preparePublicA2AProxyRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var body []byte
	if r.Body != nil {
		originalBody := r.Body
		limited := http.MaxBytesReader(w, originalBody, maxPublicA2ARequestBodyBytes)
		var err error
		body, err = io.ReadAll(limited)
		_ = originalBody.Close()
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeA2AError(w, http.StatusRequestEntityTooLarge, "RequestBodyTooLargeError", "request body exceeds 1 MiB")
			} else {
				writeA2AError(w, http.StatusBadRequest, "InvalidRequestBodyError", "request body could not be read")
			}
			return nil, false
		}
		if len(body) == 0 {
			r.Body = http.NoBody
		} else {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		r.ContentLength = int64(len(body))
	}
	removePublicA2AHopByHopHeaders(r.Header)
	r.TransferEncoding = nil
	r.Trailer = nil
	return body, true
}

func decodePublicA2AJSONRPCRequest(body []byte) (publicA2AJSONRPCRequest, bool) {
	var request publicA2AJSONRPCRequest
	if err := json.Unmarshal(body, &request); err != nil || request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		return publicA2AJSONRPCRequest{}, false
	}
	return request, true
}

func publicA2AExtendedCardMethod(method string) bool {
	switch strings.TrimSpace(method) {
	case "GetExtendedAgentCard", "agent/getExtendedCard":
		return true
	default:
		return false
	}
}

func jsonRPCID(id json.RawMessage, validRequest bool) json.RawMessage {
	if !validRequest || len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

type publicA2AProxyResponseWriter struct {
	target      http.ResponseWriter
	status      int
	wroteHeader bool
	holdError   bool
	body        bytes.Buffer
}

func (w *publicA2AProxyResponseWriter) Header() http.Header {
	return w.target.Header()
}

func (w *publicA2AProxyResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	if status == http.StatusBadGateway {
		w.holdError = true
		return
	}
	w.target.WriteHeader(status)
}

func (w *publicA2AProxyResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.holdError {
		return w.target.Write(body)
	}
	if w.body.Len()+len(body) <= maxPublicA2AProxyErrorBodyBytes {
		return w.body.Write(body)
	}
	if err := w.flushHeldError(); err != nil {
		return 0, err
	}
	return w.target.Write(body)
}

func (w *publicA2AProxyResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.holdError {
		// The SDK's ReverseProxy uses immediate flushing so successful SSE
		// responses remain live. A locally generated proxy-unavailable response
		// must stay buffered until finishHTTP/finishJSONRPC converts it to the
		// public listener's compatibility envelope.
		return
	}
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *publicA2AProxyResponseWriter) flushHeldError() error {
	if !w.holdError {
		return nil
	}
	w.holdError = false
	w.target.WriteHeader(w.status)
	if w.body.Len() == 0 {
		return nil
	}
	_, err := w.target.Write(w.body.Bytes())
	w.body.Reset()
	return err
}

func (w *publicA2AProxyResponseWriter) finishJSONRPC(id json.RawMessage) {
	if !w.holdError {
		if !w.wroteHeader {
			w.target.WriteHeader(http.StatusOK)
		}
		return
	}
	if !runtimeA2AProxyUnavailable(w.body.Bytes()) {
		_ = w.flushHeldError()
		return
	}
	w.holdError = false
	w.target.Header().Del("Content-Length")
	writePublicA2AJSONRPCError(w.target, id, -32013, "Core A2A service is unavailable", JSONMap{
		"code":        "A2A_PROXY_UNAVAILABLE",
		"http_status": http.StatusBadGateway,
	})
}

func (w *publicA2AProxyResponseWriter) finishHTTP() {
	if !w.holdError {
		if !w.wroteHeader {
			w.target.WriteHeader(http.StatusOK)
		}
		return
	}
	if !runtimeA2AProxyUnavailable(w.body.Bytes()) {
		_ = w.flushHeldError()
		return
	}
	w.holdError = false
	w.target.Header().Del("Content-Length")
	writeA2AError(w.target, http.StatusBadGateway, "A2A_PROXY_UNAVAILABLE", "Core A2A service is unavailable")
}

func runtimeA2AProxyUnavailable(body []byte) bool {
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &response) == nil && response.Error.Code == "A2A_PROXY_UNAVAILABLE"
}

func removePublicA2AHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if name := strings.TrimSpace(token); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range publicA2AHopByHopHeaders {
		header.Del(name)
	}
}

func (s *PublicA2AServer) agentCard(extended bool) map[string]any {
	baseURL := s.BaseURL()
	return map[string]any{
		"name":               s.Name,
		"description":        s.Description,
		"url":                baseURL,
		"version":            "v1",
		"protocolVersion":    "1.0",
		"protocolVersions":   []string{"1.0"},
		"preferredTransport": "JSONRPC",
		"additionalInterfaces": []map[string]any{
			{"url": baseURL, "transport": "JSONRPC"},
			{"url": baseURL, "transport": "HTTP+JSON"},
		},
		"supportedInterfaces": []map[string]any{
			{"url": baseURL, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"},
			{"url": baseURL, "protocolBinding": "HTTP+JSON", "protocolVersion": "1.0"},
		},
		"capabilities": map[string]any{
			"streaming":         true,
			"pushNotifications": true,
			"extendedAgentCard": true,
		},
		"defaultInputModes":  []string{"text/plain", "application/json"},
		"defaultOutputModes": []string{"application/json", "text/plain"},
		"skills": []map[string]any{{
			"id":          s.Slug,
			"name":        s.Name,
			"description": s.Description,
		}},
		"supportsAuthenticatedExtendedCard": true,
		"openlinker": map[string]any{
			"agent_node": true,
			"extended":   extended,
		},
	}
}

func (s *PublicA2AServer) authorized(r *http.Request) bool {
	expectedToken := strings.TrimSpace(s.Token)
	if expectedToken == "" {
		return true
	}
	expected := []byte("Bearer " + expectedToken)
	actual := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
	return len(actual) == len(expected) && subtle.ConstantTimeCompare(actual, expected) == 1
}

func publicA2AHostIsLoopback(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	return ip != nil && ip.IsLoopback()
}

func writeA2AJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/a2a+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeA2AError(w http.ResponseWriter, status int, code, message string) {
	writeA2AJSON(w, status, JSONMap{"error": JSONMap{"code": code, "message": message}})
}

func writePublicA2AJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeA2AJSON(w, http.StatusOK, publicA2AJSONRPCResponse{
		JSONRPC: "2.0",
		ID:      jsonRPCID(id, true),
		Result:  result,
	})
}

func writePublicA2AJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	writeA2AJSON(w, http.StatusOK, publicA2AJSONRPCResponse{
		JSONRPC: "2.0",
		ID:      jsonRPCID(id, true),
		Error:   &publicA2AJSONRPCError{Code: code, Message: message, Data: data},
	})
}
