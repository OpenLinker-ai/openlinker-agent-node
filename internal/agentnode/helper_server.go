package agentnode

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

const maxHelperBodyBytes = 1024 * 1024

type LocalHelperServer struct {
	Host string
	Port int

	server   *http.Server
	listener net.Listener
	baseURL  string
	mu       sync.RWMutex
	sessions map[string]*helperSessionState
}

type LocalHelperSession struct {
	Info  *HelperInfo
	close func()
}

type helperSessionState struct {
	runID  string
	runCtx *RunContext
}

func (s *LocalHelperServer) Start(ctx context.Context) error {
	if s.Host == "" {
		s.Host = "127.0.0.1"
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*helperSessionState{}
	}
	s.mu.Unlock()

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.Host, s.Port))
	if err != nil {
		return err
	}
	s.listener = listener
	address := listener.Addr().(*net.TCPAddr)
	s.baseURL = fmt.Sprintf("http://%s:%d", address.IP.String(), address.Port)
	mux := http.NewServeMux()
	mux.HandleFunc("/a2a/call", s.handleCallAgent)
	mux.HandleFunc("/events", s.handleEvent)
	s.server = &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
		defer cancel()
		_ = s.Stop(shutdownCtx)
	}()
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			// The node owns lifecycle logging; keep helper silent here.
		}
	}()
	return nil
}

func (s *LocalHelperServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.sessions = map[string]*helperSessionState{}
	server := s.server
	s.server = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (s *LocalHelperServer) CreateSession(runID string, runCtx *RunContext) *LocalHelperSession {
	token := "olh_" + randomToken()
	state := &helperSessionState{runID: runID, runCtx: runCtx}
	s.mu.Lock()
	s.sessions[token] = state
	s.mu.Unlock()
	info := &HelperInfo{
		BaseURL: s.baseURL,
		Token:   token,
		Headers: map[string]string{
			"authorization": "Bearer " + token,
		},
		Endpoints: HelperEndpoints{
			CallAgent: s.baseURL + "/a2a/call",
			Events:    s.baseURL + "/events",
		},
	}
	return &LocalHelperSession{
		Info: info,
		close: func() {
			s.mu.Lock()
			delete(s.sessions, token)
			s.mu.Unlock()
		},
	}
}

func (s *LocalHelperSession) Close() {
	if s != nil && s.close != nil {
		s.close()
	}
}

func (s *LocalHelperServer) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, JSONMap{"error": JSONMap{"code": "METHOD_NOT_ALLOWED", "message": "method not allowed"}})
		return
	}
	session := s.authenticate(r)
	if session == nil {
		writeJSON(w, http.StatusUnauthorized, JSONMap{"error": JSONMap{"code": "UNAUTHORIZED", "message": "invalid helper token"}})
		return
	}
	var body struct {
		RunID     string `json:"run_id"`
		EventType string `json:"event_type"`
		Payload   any    `json:"payload"`
	}
	if err := decodeHelperJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, JSONMap{"error": JSONMap{"code": "INVALID_JSON", "message": err.Error()}})
		return
	}
	if body.RunID != "" && body.RunID != session.runID {
		writeJSON(w, http.StatusConflict, JSONMap{"error": JSONMap{"code": "RUN_MISMATCH", "message": "helper token belongs to a different run"}})
		return
	}
	if body.EventType == "" {
		writeJSON(w, http.StatusBadRequest, JSONMap{"error": JSONMap{"code": "INVALID_EVENT", "message": "event_type is required"}})
		return
	}
	session.runCtx.Emit(body.EventType, body.Payload)
	writeJSON(w, http.StatusOK, JSONMap{"ok": true, "run_id": session.runID})
}

func (s *LocalHelperServer) handleCallAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, JSONMap{"error": JSONMap{"code": "METHOD_NOT_ALLOWED", "message": "method not allowed"}})
		return
	}
	session := s.authenticate(r)
	if session == nil {
		writeJSON(w, http.StatusUnauthorized, JSONMap{"error": JSONMap{"code": "UNAUTHORIZED", "message": "invalid helper token"}})
		return
	}
	var body struct {
		RunID                     string              `json:"run_id"`
		TargetAgentID             string              `json:"target_agent_id"`
		Reason                    string              `json:"reason"`
		Input                     any                 `json:"input"`
		Metadata                  any                 `json:"metadata"`
		Endpoint                  string              `json:"endpoint"`
		ContextID                 string              `json:"context_id"`
		ContextIDAlias            string              `json:"contextId"`
		TraceID                   string              `json:"trace_id"`
		TraceIDAlias              string              `json:"traceId"`
		ReferenceTaskIDs          []string            `json:"reference_task_ids"`
		ReferenceTaskIDsAlias     []string            `json:"referenceTaskIds"`
		TaskCallback              *TaskCallbackConfig `json:"task_callback"`
		PushNotification          *TaskCallbackConfig `json:"push_notification"`
		PushNotificationConfig    *TaskCallbackConfig `json:"pushNotificationConfig"`
		PushNotificationShorthand *TaskCallbackConfig `json:"pushNotification"`
	}
	if err := decodeHelperJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, JSONMap{"error": JSONMap{"code": "INVALID_JSON", "message": err.Error()}})
		return
	}
	if body.RunID != "" && body.RunID != session.runID {
		writeJSON(w, http.StatusConflict, JSONMap{"error": JSONMap{"code": "RUN_MISMATCH", "message": "helper token belongs to a different run"}})
		return
	}
	if body.TargetAgentID == "" {
		writeJSON(w, http.StatusBadRequest, JSONMap{"error": JSONMap{"code": "INVALID_TARGET_AGENT", "message": "target_agent_id is required"}})
		return
	}
	taskCallback := body.TaskCallback
	if taskCallback == nil {
		taskCallback = body.PushNotification
	}
	if taskCallback == nil {
		taskCallback = body.PushNotificationConfig
	}
	if taskCallback == nil {
		taskCallback = body.PushNotificationShorthand
	}
	result, err := session.runCtx.CallAgent(r.Context(), body.TargetAgentID, body.Input, CallAgentOptions{
		Reason:           body.Reason,
		Metadata:         body.Metadata,
		Endpoint:         body.Endpoint,
		ContextID:        firstNonEmpty(body.ContextID, body.ContextIDAlias),
		TraceID:          firstNonEmpty(body.TraceID, body.TraceIDAlias),
		ReferenceTaskIDs: firstNonEmptyStrings(body.ReferenceTaskIDs, body.ReferenceTaskIDsAlias),
		TaskCallback:     taskCallback,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, JSONMap{"error": JSONMap{"code": "A2A_CALL_FAILED", "message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *LocalHelperServer) authenticate(r *http.Request) *helperSessionState {
	auth := r.Header.Get("authorization")
	token := ""
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token = strings.TrimSpace(auth[len("bearer "):])
	}
	if token == "" {
		token = r.Header.Get("x-openlinker-agent-node-token")
	}
	if token == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[token]
}

func decodeHelperJSON(r *http.Request, value any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxHelperBodyBytes))
	return decoder.Decode(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomToken() string {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}
