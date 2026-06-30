package agentnode

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type PublicA2AServer struct {
	Host        string
	Port        int
	Slug        string
	Name        string
	Description string
	Token       string
	Adapter     Adapter

	server   *http.Server
	listener net.Listener
	baseURL  string
	mu       sync.RWMutex
	tasks    map[string]*publicA2ATask
	pushes   map[string]map[string]*publicA2APushConfig
}

type publicA2ATask struct {
	ID        string
	ContextID string
	State     string
	Output    any
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type publicA2APushConfig struct {
	ID             string
	TaskID         string
	URL            string
	Token          string
	Secret         string
	Authentication *openlinker.A2APushAuthenticationInfo
	Metadata       map[string]any
	EventTypes     []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type publicA2AJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type publicA2AJSONRPCResponse struct {
	JSONRPC string              `json:"jsonrpc"`
	ID      json.RawMessage     `json:"id"`
	Result  any                 `json:"result,omitempty"`
	Error   *publicA2AJSONError `json:"error,omitempty"`
}

type publicA2AJSONError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *PublicA2AServer) Start(ctx context.Context) error {
	if s.Adapter == nil {
		return fmt.Errorf("public A2A server requires adapter")
	}
	if s.Host == "" {
		s.Host = "127.0.0.1"
	}
	if s.Slug == "" {
		s.Slug = "agent-node"
	}
	if s.Name == "" {
		s.Name = s.Slug
	}
	s.mu.Lock()
	if s.tasks == nil {
		s.tasks = map[string]*publicA2ATask{}
	}
	if s.pushes == nil {
		s.pushes = map[string]map[string]*publicA2APushConfig{}
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
	mux.HandleFunc("/.well-known/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("/extendedAgentCard", s.handleExtendedAgentCard)
	mux.HandleFunc("/message:send", s.handleMessageSend)
	mux.HandleFunc("/message:stream", s.handleMessageStream)
	mux.HandleFunc("/tasks", s.handleTasks)
	mux.HandleFunc("/tasks/", s.handleTaskPath)
	mux.HandleFunc("/", s.handleJSONRPC)
	s.server = &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
		defer cancel()
		_ = s.Stop(shutdownCtx)
	}()
	go func() {
		_ = s.server.Serve(listener)
	}()
	return nil
}

func (s *PublicA2AServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (s *PublicA2AServer) BaseURL() string {
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

func (s *PublicA2AServer) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: publicA2AError(-32010, "authorization required")})
		return
	}
	var req publicA2AJSONRPCRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHelperBodyBytes)).Decode(&req); err != nil {
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: publicA2AError(-32700, "invalid JSON payload")})
		return
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32600, "invalid JSON-RPC request")})
		return
	}
	switch normalizePublicA2AMethod(req.Method) {
	case "SendMessage":
		var params openlinker.A2AMessageSendParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32602, "invalid SendMessage params")})
			return
		}
		task, err := s.runMessage(r.Context(), params)
		if err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32603, err.Error())})
			return
		}
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: openlinker.A2ASendMessageResponse{Task: task}})
	case "GetTask":
		var params openlinker.A2ATaskQueryParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32602, "invalid GetTask params")})
			return
		}
		task, ok := s.task(params.ID)
		if !ok {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32001, "task not found")})
			return
		}
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: task})
	case "ListTasks":
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: s.taskList()})
	case "CancelTask":
		var params openlinker.A2ATaskQueryParams
		_ = json.Unmarshal(req.Params, &params)
		task, err := s.cancelTask(params.ID)
		if err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32002, err.Error())})
			return
		}
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: task})
	case "CreateTaskPushNotificationConfig":
		var params openlinker.A2ATaskPushConfigParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32602, "invalid Push Config params")})
			return
		}
		cfg, err := s.createPushConfig(r.Context(), params)
		if err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32002, err.Error())})
			return
		}
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: cfg})
	case "GetTaskPushNotificationConfig":
		var params openlinker.A2ATaskPushConfigParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32602, "invalid Push Config params")})
			return
		}
		cfg, ok := s.pushConfig(publicA2ATaskIDFromPushParams(params), publicA2APushConfigID(params))
		if !ok {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32001, "push config not found")})
			return
		}
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: cfg})
	case "ListTaskPushNotificationConfigs":
		var params openlinker.A2ATaskPushConfigParams
		_ = json.Unmarshal(req.Params, &params)
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: s.pushConfigList(publicA2ATaskIDFromPushParams(params))})
	case "DeleteTaskPushNotificationConfig":
		var params openlinker.A2ATaskPushConfigParams
		_ = json.Unmarshal(req.Params, &params)
		if err := s.deletePushConfig(publicA2ATaskIDFromPushParams(params), publicA2APushConfigID(params)); err != nil {
			writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32001, err.Error())})
			return
		}
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: nil})
	case "GetExtendedAgentCard":
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Result: s.agentCard(true)})
	default:
		writeA2AJSONRPC(w, publicA2AJSONRPCResponse{JSONRPC: "2.0", ID: normalizeA2AJSONRPCID(req.ID), Error: publicA2AError(-32601, "method not found")})
	}
}

func (s *PublicA2AServer) handleMessageSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeA2AError(w, http.StatusUnauthorized, "AuthRequiredError", "authorization required")
		return
	}
	var params openlinker.A2AMessageSendParams
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHelperBodyBytes)).Decode(&params); err != nil {
		writeA2AError(w, http.StatusBadRequest, "InvalidParamsError", "invalid message params")
		return
	}
	task, err := s.runMessage(r.Context(), params)
	if err != nil {
		writeA2AError(w, http.StatusInternalServerError, "InvalidAgentResponseError", err.Error())
		return
	}
	writeA2AJSON(w, http.StatusOK, openlinker.A2ASendMessageResponse{Task: task})
}

func (s *PublicA2AServer) handleMessageStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeA2AError(w, http.StatusUnauthorized, "AuthRequiredError", "authorization required")
		return
	}
	var params openlinker.A2AMessageSendParams
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHelperBodyBytes)).Decode(&params); err != nil {
		writeA2AError(w, http.StatusBadRequest, "InvalidParamsError", "invalid message params")
		return
	}
	task, err := s.runMessage(r.Context(), params)
	if err != nil {
		writeA2AError(w, http.StatusInternalServerError, "InvalidAgentResponseError", err.Error())
		return
	}
	writePublicA2ASSE(w, "task", openlinker.A2AStreamResponse{Task: task})
}

func (s *PublicA2AServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeA2AError(w, http.StatusUnauthorized, "AuthRequiredError", "authorization required")
		return
	}
	writeA2AJSON(w, http.StatusOK, s.taskList())
}

func (s *PublicA2AServer) handleTaskPath(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeA2AError(w, http.StatusUnauthorized, "AuthRequiredError", "authorization required")
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/tasks/")
	switch {
	case publicA2APushPath(raw):
		s.handleTaskPushConfigPath(w, r, raw)
	case strings.HasSuffix(raw, "/subscribe"):
		if r.Method != http.MethodGet {
			writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
			return
		}
		taskID := strings.TrimSuffix(raw, "/subscribe")
		task, ok := s.task(taskID)
		if !ok {
			writeA2AError(w, http.StatusNotFound, "TaskNotFoundError", "task not found")
			return
		}
		writePublicA2ASSE(w, "task", openlinker.A2AStreamResponse{Task: task})
	case strings.HasSuffix(raw, ":cancel"):
		if r.Method != http.MethodPost {
			writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
			return
		}
		taskID := strings.TrimSuffix(raw, ":cancel")
		task, err := s.cancelTask(taskID)
		if err != nil {
			writeA2AError(w, http.StatusBadRequest, "TaskNotCancelableError", err.Error())
			return
		}
		writeA2AJSON(w, http.StatusOK, task)
	default:
		if r.Method != http.MethodGet {
			writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
			return
		}
		task, ok := s.task(raw)
		if !ok {
			writeA2AError(w, http.StatusNotFound, "TaskNotFoundError", "task not found")
			return
		}
		writeA2AJSON(w, http.StatusOK, task)
	}
}

func (s *PublicA2AServer) handleTaskPushConfigPath(w http.ResponseWriter, r *http.Request, raw string) {
	taskID, pushID, ok := splitPublicA2APushPath(raw)
	if !ok || strings.TrimSpace(taskID) == "" {
		writeA2AError(w, http.StatusNotFound, "TaskNotFoundError", "task not found")
		return
	}
	if pushID == "" {
		switch r.Method {
		case http.MethodPost:
			var params openlinker.A2ATaskPushConfigParams
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHelperBodyBytes)).Decode(&params); err != nil {
				writeA2AError(w, http.StatusBadRequest, "InvalidParamsError", "invalid Push Config params")
				return
			}
			params.TaskID = taskID
			cfg, err := s.createPushConfig(r.Context(), params)
			if err != nil {
				writeA2AError(w, http.StatusBadRequest, "InvalidParamsError", err.Error())
				return
			}
			writeA2AJSON(w, http.StatusCreated, cfg)
		case http.MethodGet:
			writeA2AJSON(w, http.StatusOK, s.pushConfigList(taskID))
		default:
			writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, ok := s.pushConfig(taskID, pushID)
		if !ok {
			writeA2AError(w, http.StatusNotFound, "TaskNotFoundError", "push config not found")
			return
		}
		writeA2AJSON(w, http.StatusOK, cfg)
	case http.MethodDelete:
		if err := s.deletePushConfig(taskID, pushID); err != nil {
			writeA2AError(w, http.StatusNotFound, "TaskNotFoundError", err.Error())
			return
		}
		writeA2AJSON(w, http.StatusOK, nil)
	default:
		writeA2AError(w, http.StatusMethodNotAllowed, "UnsupportedOperationError", "method not allowed")
	}
}

func (s *PublicA2AServer) runMessage(ctx context.Context, params openlinker.A2AMessageSendParams) (*openlinker.A2ATask, error) {
	taskID := "task-" + randomToken()
	contextID := strings.TrimSpace(params.Message.ContextID)
	if contextID == "" {
		contextID = "ctx-" + taskID
	}
	input := publicA2AInputFromMessage(params.Message)
	startedAt := time.Now().UTC()
	runCtx := RunContext{
		RunID:    taskID,
		Input:    input,
		Source:   "a2a_public",
		Metadata: JSONMap{"a2a_protocol_version": "1.0"},
		A2A: JSONMap{
			"current_run_id":      taskID,
			"protocol_context_id": contextID,
			"protocol_task_id":    taskID,
		},
	}
	raw, err := s.Adapter.Run(ctx, input, runCtx)
	result := normalizeAdapterResult(raw)
	if err != nil {
		result.Status = "failed"
		result.Error = normalizeAgentError(err)
	}
	state := "TASK_STATE_COMPLETED"
	if result.Status == "failed" || result.Error != nil {
		state = "TASK_STATE_FAILED"
	}
	task := &publicA2ATask{
		ID:        taskID,
		ContextID: contextID,
		State:     state,
		Output:    result.Output,
		CreatedAt: startedAt,
		UpdatedAt: time.Now().UTC(),
	}
	if result.Error != nil {
		task.Error = result.Error.Message
	}
	inlinePush := publicA2AInlinePushConfig(taskID, params.Configuration)
	s.mu.Lock()
	s.tasks[taskID] = task
	if inlinePush != nil {
		if s.pushes == nil {
			s.pushes = map[string]map[string]*publicA2APushConfig{}
		}
		if s.pushes[taskID] == nil {
			s.pushes[taskID] = map[string]*publicA2APushConfig{}
		}
		s.pushes[taskID][inlinePush.ID] = inlinePush
	}
	pushes := s.pushConfigsForTaskLocked(taskID)
	s.mu.Unlock()
	s.deliverTerminalPushNotifications(ctx, task, pushes)
	return publicA2ATaskDTO(task), nil
}

func (s *PublicA2AServer) task(taskID string) (*openlinker.A2ATask, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return nil, false
	}
	return publicA2ATaskDTO(task), true
}

func (s *PublicA2AServer) taskList() openlinker.A2ATaskListResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := make([]openlinker.A2ATask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, *publicA2ATaskDTO(task))
	}
	return openlinker.A2ATaskListResponse{Tasks: tasks, PageSize: int32(len(tasks)), TotalSize: int32(len(tasks))}
}

func (s *PublicA2AServer) createPushConfig(ctx context.Context, params openlinker.A2ATaskPushConfigParams) (*openlinker.A2ATaskPushNotificationConfig, error) {
	taskID := publicA2ATaskIDFromPushParams(params)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	cfg, err := publicA2APushConfigFromParams(taskID, params)
	if err != nil {
		return nil, err
	}
	var terminalTask *publicA2ATask
	s.mu.Lock()
	task, ok := s.tasks[taskID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("task not found")
	}
	if s.pushes == nil {
		s.pushes = map[string]map[string]*publicA2APushConfig{}
	}
	if s.pushes[taskID] == nil {
		s.pushes[taskID] = map[string]*publicA2APushConfig{}
	}
	s.pushes[taskID][cfg.ID] = cfg
	if publicA2ATaskTerminal(task.State) {
		terminalTask = clonePublicA2ATask(task)
	}
	s.mu.Unlock()
	if terminalTask != nil {
		s.deliverTerminalPushNotifications(ctx, terminalTask, []*publicA2APushConfig{cfg})
	}
	return publicA2APushConfigDTO(cfg), nil
}

func (s *PublicA2AServer) pushConfig(taskID, configID string) (*openlinker.A2ATaskPushNotificationConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfgs := s.pushes[strings.TrimSpace(taskID)]
	cfg, ok := cfgs[strings.TrimSpace(configID)]
	if !ok {
		return nil, false
	}
	return publicA2APushConfigDTO(cfg), true
}

func (s *PublicA2AServer) pushConfigList(taskID string) openlinker.A2ATaskPushConfigList {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfgs := s.pushes[strings.TrimSpace(taskID)]
	items := make([]openlinker.A2ATaskPushNotificationConfig, 0, len(cfgs))
	for _, cfg := range cfgs {
		items = append(items, *publicA2APushConfigDTO(cfg))
	}
	return openlinker.A2ATaskPushConfigList{Configs: items, Items: items}
}

func (s *PublicA2AServer) deletePushConfig(taskID, configID string) error {
	taskID = strings.TrimSpace(taskID)
	configID = strings.TrimSpace(configID)
	if taskID == "" || configID == "" {
		return fmt.Errorf("task id and push config id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfgs := s.pushes[taskID]
	if cfgs == nil {
		return fmt.Errorf("push config not found")
	}
	if _, ok := cfgs[configID]; !ok {
		return fmt.Errorf("push config not found")
	}
	delete(cfgs, configID)
	return nil
}

func (s *PublicA2AServer) pushConfigsForTaskLocked(taskID string) []*publicA2APushConfig {
	cfgs := s.pushes[strings.TrimSpace(taskID)]
	out := make([]*publicA2APushConfig, 0, len(cfgs))
	for _, cfg := range cfgs {
		out = append(out, cfg)
	}
	return out
}

func (s *PublicA2AServer) cancelTask(taskID string) (*openlinker.A2ATask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return nil, fmt.Errorf("task not found")
	}
	if task.State == "TASK_STATE_COMPLETED" || task.State == "TASK_STATE_FAILED" || task.State == "TASK_STATE_CANCELED" {
		return nil, fmt.Errorf("task is already terminal")
	}
	task.State = "TASK_STATE_CANCELED"
	task.UpdatedAt = time.Now().UTC()
	dto := publicA2ATaskDTO(task)
	pushes := s.pushConfigsForTaskLocked(task.ID)
	terminal := clonePublicA2ATask(task)
	go s.deliverTerminalPushNotifications(context.Background(), terminal, pushes)
	return dto, nil
}

func (s *PublicA2AServer) deliverTerminalPushNotifications(ctx context.Context, task *publicA2ATask, configs []*publicA2APushConfig) {
	if task == nil || len(configs) == 0 {
		return
	}
	eventType := publicA2ATerminalEventType(task.State)
	if eventType == "" {
		return
	}
	for _, cfg := range configs {
		if cfg == nil || !publicA2APushWantsEvent(cfg, eventType) {
			continue
		}
		s.deliverPushNotification(ctx, task, cfg, eventType)
	}
}

func (s *PublicA2AServer) deliverPushNotification(ctx context.Context, task *publicA2ATask, cfg *publicA2APushConfig, eventType string) {
	payload := JSONMap{
		"event_type": eventType,
		"task_id":    task.ID,
		"run_id":     task.ID,
		"agent_slug": s.Slug,
		"source":     "openlinker-agent-node-public-a2a",
		"task":       publicA2ATaskDTO(task),
		"metadata":   cfg.Metadata,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	deliverCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(deliverCtx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-OpenLinker-Event", eventType)
	req.Header.Set("X-OpenLinker-Task-Id", task.ID)
	if cfg.Secret != "" {
		req.Header.Set("X-OpenLinker-Signature", "sha256="+publicA2ASignPayload(body, cfg.Secret))
	}
	if auth := publicA2AAuthorizationHeader(cfg); auth != "" {
		req.Header.Set("authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func publicA2ATaskDTO(task *publicA2ATask) *openlinker.A2ATask {
	out := &openlinker.A2ATask{
		ID:        task.ID,
		ContextID: task.ContextID,
		Status: openlinker.A2ATaskStatus{
			State:     task.State,
			Timestamp: task.UpdatedAt.Format(time.RFC3339),
		},
		Metadata: map[string]any{"source": "openlinker-agent-node"},
	}
	if task.Output != nil {
		out.Artifacts = []openlinker.A2AArtifact{{
			ArtifactID: "output",
			Parts: []map[string]any{{
				"data": task.Output,
			}},
		}}
	}
	if task.Error != "" {
		out.Status.Message = &openlinker.A2AMessage{Role: "ROLE_AGENT", Parts: []map[string]any{{"text": task.Error}}}
	}
	return out
}

func publicA2AInputFromMessage(message openlinker.A2AMessage) any {
	if len(message.Parts) == 1 {
		part := message.Parts[0]
		if data, ok := part["data"]; ok {
			return data
		}
		if text, ok := part["text"].(string); ok {
			return JSONMap{"message": text, "text": text}
		}
	}
	var texts []string
	for _, part := range message.Parts {
		if text, ok := part["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	if len(texts) > 0 {
		text := strings.Join(texts, "\n")
		return JSONMap{"message": text, "text": text}
	}
	return JSONMap{"parts": message.Parts}
}

func (s *PublicA2AServer) agentCard(extended bool) map[string]any {
	return map[string]any{
		"name":               s.Name,
		"description":        s.Description,
		"url":                s.baseURL,
		"version":            "v1",
		"protocolVersion":    "1.0",
		"protocolVersions":   []string{"1.0"},
		"preferredTransport": "JSONRPC",
		"additionalInterfaces": []map[string]any{
			{"url": s.baseURL, "transport": "JSONRPC"},
			{"url": s.baseURL, "transport": "HTTP+JSON"},
		},
		"supportedInterfaces": []map[string]any{
			{"url": s.baseURL, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"},
			{"url": s.baseURL, "protocolBinding": "HTTP+JSON", "protocolVersion": "1.0"},
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
	if strings.TrimSpace(s.Token) == "" {
		return true
	}
	return strings.TrimSpace(r.Header.Get("authorization")) == "Bearer "+strings.TrimSpace(s.Token)
}

func normalizePublicA2AMethod(method string) string {
	switch strings.TrimSpace(method) {
	case "SendMessage", "message/send", "message:send":
		return "SendMessage"
	case "GetTask", "tasks/get":
		return "GetTask"
	case "ListTasks", "tasks/list":
		return "ListTasks"
	case "CancelTask", "tasks/cancel":
		return "CancelTask"
	case "CreateTaskPushNotificationConfig", "tasks/pushNotificationConfig/set", "tasks/pushNotificationConfigs/set":
		return "CreateTaskPushNotificationConfig"
	case "GetTaskPushNotificationConfig", "tasks/pushNotificationConfig/get", "tasks/pushNotificationConfigs/get":
		return "GetTaskPushNotificationConfig"
	case "ListTaskPushNotificationConfigs", "tasks/pushNotificationConfig/list", "tasks/pushNotificationConfigs/list":
		return "ListTaskPushNotificationConfigs"
	case "DeleteTaskPushNotificationConfig", "tasks/pushNotificationConfig/delete", "tasks/pushNotificationConfigs/delete":
		return "DeleteTaskPushNotificationConfig"
	case "GetExtendedAgentCard", "agent/getExtendedCard":
		return "GetExtendedAgentCard"
	default:
		return strings.TrimSpace(method)
	}
}

func normalizeA2AJSONRPCID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func publicA2AError(code int, message string) *publicA2AJSONError {
	return &publicA2AJSONError{Code: code, Message: message}
}

func writeA2AJSONRPC(w http.ResponseWriter, value publicA2AJSONRPCResponse) {
	writeA2AJSON(w, http.StatusOK, value)
}

func writeA2AJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/a2a+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeA2AError(w http.ResponseWriter, status int, code, message string) {
	writeA2AJSON(w, status, JSONMap{"error": JSONMap{"code": code, "message": message}})
}

func writePublicA2ASSE(w http.ResponseWriter, event string, payload any) {
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.WriteHeader(http.StatusOK)
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(payload)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, strings.TrimSpace(buf.String()))
}

func publicA2AInlinePushConfig(taskID string, cfg *openlinker.A2ASendConfiguration) *publicA2APushConfig {
	if cfg == nil {
		return nil
	}
	params := openlinker.A2ATaskPushConfigParams{TaskID: taskID}
	if cfg.TaskPushNotificationConfig != nil {
		params.PushNotificationConfigID = cfg.TaskPushNotificationConfig.ID
		params.PushNotificationConfig = cfg.TaskPushNotificationConfig.PushNotificationConfig
		params.URL = cfg.TaskPushNotificationConfig.URL
		params.Token = cfg.TaskPushNotificationConfig.Token
		params.Secret = cfg.TaskPushNotificationConfig.Secret
		params.Authentication = cfg.TaskPushNotificationConfig.Authentication
		params.Metadata = cfg.TaskPushNotificationConfig.Metadata
		params.EventTypes = cfg.TaskPushNotificationConfig.EventTypes
		params.EventTypesAlias = cfg.TaskPushNotificationConfig.EventTypesAlias
	} else if cfg.PushNotificationConfig != nil {
		params.PushNotificationConfig = *cfg.PushNotificationConfig
	} else {
		return nil
	}
	out, err := publicA2APushConfigFromParams(taskID, params)
	if err != nil {
		return nil
	}
	return out
}

func publicA2APushConfigFromParams(taskID string, params openlinker.A2ATaskPushConfigParams) (*publicA2APushConfig, error) {
	push := params.PushNotificationConfig
	if push.URL == "" {
		push.URL = params.URL
	}
	if push.Token == "" {
		push.Token = params.Token
	}
	if push.Secret == "" {
		push.Secret = params.Secret
	}
	if push.Authentication == nil {
		push.Authentication = params.Authentication
	}
	if push.Metadata == nil {
		push.Metadata = params.Metadata
	}
	if len(push.EventTypes) == 0 {
		push.EventTypes = params.EventTypes
	}
	if len(push.EventTypesAlias) == 0 {
		push.EventTypesAlias = params.EventTypesAlias
	}
	id := strings.TrimSpace(params.PushNotificationConfigID)
	if id == "" {
		id = strings.TrimSpace(params.ID)
	}
	if id == "" {
		id = strings.TrimSpace(push.ID)
	}
	if id == "" {
		id = "push-" + randomToken()
	}
	if strings.TrimSpace(push.URL) == "" {
		return nil, fmt.Errorf("push notification URL is required")
	}
	now := time.Now().UTC()
	return &publicA2APushConfig{
		ID:             id,
		TaskID:         strings.TrimSpace(taskID),
		URL:            strings.TrimSpace(push.URL),
		Token:          strings.TrimSpace(push.Token),
		Secret:         strings.TrimSpace(push.Secret),
		Authentication: push.Authentication,
		Metadata:       push.Metadata,
		EventTypes:     append([]string{}, append(push.EventTypes, push.EventTypesAlias...)...),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func publicA2APushConfigDTO(cfg *publicA2APushConfig) *openlinker.A2ATaskPushNotificationConfig {
	if cfg == nil {
		return nil
	}
	var auth *openlinker.A2APushAuthenticationInfo
	if cfg.Authentication != nil {
		auth = &openlinker.A2APushAuthenticationInfo{Scheme: cfg.Authentication.Scheme}
	}
	push := openlinker.A2APushNotificationConfig{
		ID:             cfg.ID,
		URL:            cfg.URL,
		Authentication: auth,
		Metadata:       cfg.Metadata,
		EventTypes:     append([]string{}, cfg.EventTypes...),
	}
	return &openlinker.A2ATaskPushNotificationConfig{
		ID:                     cfg.ID,
		TaskID:                 cfg.TaskID,
		URL:                    cfg.URL,
		Authentication:         auth,
		Metadata:               cfg.Metadata,
		EventTypes:             append([]string{}, cfg.EventTypes...),
		PushNotificationConfig: push,
	}
}

func publicA2ATaskIDFromPushParams(params openlinker.A2ATaskPushConfigParams) string {
	if strings.TrimSpace(params.TaskID) != "" {
		return strings.TrimSpace(params.TaskID)
	}
	return strings.TrimSpace(params.ID)
}

func publicA2APushConfigID(params openlinker.A2ATaskPushConfigParams) string {
	if strings.TrimSpace(params.PushNotificationConfigID) != "" {
		return strings.TrimSpace(params.PushNotificationConfigID)
	}
	if strings.TrimSpace(params.PushNotificationConfig.ID) != "" {
		return strings.TrimSpace(params.PushNotificationConfig.ID)
	}
	return strings.TrimSpace(params.ID)
}

func publicA2APushPath(raw string) bool {
	return strings.Contains(raw, "/pushNotificationConfig")
}

func splitPublicA2APushPath(raw string) (string, string, bool) {
	for _, marker := range []string{"/pushNotificationConfigs", "/pushNotificationConfig"} {
		before, after, ok := strings.Cut(raw, marker)
		if !ok {
			continue
		}
		return strings.Trim(before, "/"), strings.Trim(strings.TrimPrefix(after, "/"), "/"), true
	}
	return "", "", false
}

func publicA2ATaskTerminal(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "COMPLETED", "FAILED", "CANCELED":
		return true
	default:
		return false
	}
}

func publicA2ATerminalEventType(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "TASK_STATE_COMPLETED", "COMPLETED":
		return "run.completed"
	case "TASK_STATE_FAILED", "FAILED":
		return "run.failed"
	case "TASK_STATE_CANCELED", "CANCELED":
		return "run.canceled"
	default:
		return ""
	}
}

func publicA2APushWantsEvent(cfg *publicA2APushConfig, eventType string) bool {
	if len(cfg.EventTypes) == 0 {
		return true
	}
	for _, allowed := range cfg.EventTypes {
		if strings.EqualFold(strings.TrimSpace(allowed), eventType) {
			return true
		}
	}
	return false
}

func publicA2AAuthorizationHeader(cfg *publicA2APushConfig) string {
	if cfg.Authentication != nil && strings.TrimSpace(cfg.Authentication.Credentials) != "" {
		scheme := strings.TrimSpace(cfg.Authentication.Scheme)
		if scheme == "" {
			scheme = "Bearer"
		}
		return scheme + " " + strings.TrimSpace(cfg.Authentication.Credentials)
	}
	if cfg.Token != "" {
		return "Bearer " + cfg.Token
	}
	return ""
}

func publicA2ASignPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func clonePublicA2ATask(task *publicA2ATask) *publicA2ATask {
	if task == nil {
		return nil
	}
	copy := *task
	return &copy
}
