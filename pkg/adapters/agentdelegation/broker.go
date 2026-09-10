// Package agentdelegation exposes only active-Attempt callbacks over a private
// local MCP socket. It has no platform client, credentials, or Runtime Worker.
package agentdelegation

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const SocketEnvironment = "OPENLINKER_DELEGATION_SOCKET"
const maxMessageBytes = 4 << 20

type safeToolError string

var errUnknownMethod = safeToolError("unknown delegation MCP method")

func (e safeToolError) Error() string { return string(e) }

var ToolNames = []string{"delegate_agent", "get_delegated_run", "wait_delegated_run"}
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Callbacks struct {
	CallAgent func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error)
	ReadRun   func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error)
}

type Broker struct {
	Socket      string
	ctx         context.Context
	cancel      context.CancelFunc
	listener    net.Listener
	directory   string
	runID       string
	targets     []string
	callbacks   Callbacks
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
	once        sync.Once
	// Serializing tools bounds active child creation and makes request-key
	// conflicts deterministic even if a host opens multiple MCP connections.
	calls    sync.Mutex
	requests map[string]delegationRequest
}

type delegationRequest struct {
	Digest   [32]byte
	RunID    string
	Terminal bool
	Summary  openlinker.RuntimeRunSummary
}

func ValidateTargets(targets []string) error {
	if len(targets) > 32 {
		return safeToolError("at most 32 delegation targets are allowed")
	}
	seen := map[string]bool{}
	for _, id := range targets {
		if !uuidPattern.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" || seen[id] {
			return safeToolError("delegation targets must be distinct lowercase Agent UUIDs")
		}
		seen[id] = true
	}
	return nil
}

func Start(parent context.Context, root, runID string, targets []string, callbacks Callbacks) (*Broker, error) {
	if len(targets) == 0 || callbacks.CallAgent == nil || callbacks.ReadRun == nil || !uuidPattern.MatchString(runID) {
		return nil, safeToolError("delegation requires targets and active Attempt callbacks with result support")
	}
	if err := ValidateTargets(targets); err != nil {
		return nil, err
	}
	if root != "" && !filepath.IsAbs(root) {
		return nil, safeToolError("delegation broker root must be absolute")
	}
	directory, err := os.MkdirTemp(root, "ol-d-")
	if err != nil {
		return nil, fmt.Errorf("create private delegation directory: %w", err)
	}
	socketMode, err := delegationSocketMode(root, directory)
	if err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	socket := filepath.Join(directory, "mcp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(directory)
		return nil, fmt.Errorf("listen on delegation socket: %w", err)
	}
	if err := os.Chmod(socket, socketMode); err != nil {
		listener.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	if socketMode == 0o660 {
		// Create the socket before chmod: Linux may clear an inherited setgid
		// bit when the Runtime owner is not a member of the Provider group.
		// The socket must inherit that group before exposing the directory.
		if err := os.Chmod(directory, 0o710); err != nil {
			listener.Close()
			os.RemoveAll(directory)
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(parent)
	broker := &Broker{Socket: socket, ctx: ctx, cancel: cancel, listener: listener, directory: directory,
		runID: runID, targets: append([]string(nil), targets...), callbacks: callbacks,
		connections: map[net.Conn]struct{}{}, requests: map[string]delegationRequest{}}
	broker.wg.Add(1)
	go broker.accept()
	go func() { <-ctx.Done(); broker.Close() }()
	return broker, nil
}

func (b *Broker) Close() {
	b.once.Do(func() {
		b.cancel()
		b.listener.Close()
		b.mu.Lock()
		for connection := range b.connections {
			connection.Close()
		}
		b.mu.Unlock()
		b.wg.Wait()
		os.RemoveAll(b.directory)
	})
}

func (b *Broker) accept() {
	defer b.wg.Done()
	for {
		connection, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.ctx.Err() != nil || len(b.connections) >= 8 {
			b.mu.Unlock()
			connection.Close()
			continue
		}
		b.connections[connection] = struct{}{}
		b.wg.Add(1)
		b.mu.Unlock()
		go func() {
			defer b.wg.Done()
			defer func() { connection.Close(); b.mu.Lock(); delete(b.connections, connection); b.mu.Unlock() }()
			b.serve(connection)
		}()
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (b *Broker) serve(connection net.Conn) {
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	encoder := json.NewEncoder(connection)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		result, err := b.dispatch(request)
		if err != nil {
			code := -32602
			if errors.Is(err, errUnknownMethod) {
				code = -32601
			}
			response["error"] = map[string]any{"code": code, "message": err.Error()}
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

func (b *Broker) dispatch(request rpcRequest) (any, error) {
	if request.JSONRPC != "2.0" {
		return nil, safeToolError("invalid JSON-RPC version")
	}
	switch request.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "openlinker-delegation", "version": "1"}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": b.tools()}, nil
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta,omitempty"`
		}
		if err := decodeArguments(request.Params, &call); err != nil {
			return nil, err
		}
		value, err := b.call(call.Name, call.Arguments)
		result := map[string]any{}
		var text string
		if err != nil {
			// Callback errors can contain HTTP diagnostics. Never forward raw errors
			// from the credential-bearing Worker to the model transport.
			text = "Delegation operation failed. Reuse the same request_key when retrying creation."
			result["isError"] = true
			if local, ok := err.(safeToolError); ok {
				text = local.Error()
			}
		} else {
			raw, marshalErr := json.Marshal(value)
			if marshalErr != nil || len(raw) > maxMessageBytes {
				text = "Delegated result exceeds the tool response limit."
				result["isError"] = true
			} else {
				text = string(raw)
			}
		}
		result["content"] = []any{map[string]any{"type": "text", "text": text}}
		return result, nil
	default:
		return nil, errUnknownMethod
	}
}

func decodeArguments(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return safeToolError("invalid delegation arguments")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return safeToolError("invalid delegation arguments")
	}
	return nil
}

func (b *Broker) call(name string, raw []byte) (any, error) {
	b.calls.Lock()
	defer b.calls.Unlock()
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, stopCall := context.WithTimeout(b.ctx, 30*time.Second)
	defer stopCall()
	switch name {
	case "delegate_agent":
		var request struct {
			Target string         `json:"target_agent_id"`
			Input  map[string]any `json:"input"`
			Reason string         `json:"reason,omitempty"`
			Key    string         `json:"request_key"`
		}
		if err := decodeArguments(raw, &request); err != nil {
			return nil, err
		}
		allowed := false
		for _, target := range b.targets {
			if request.Target == target {
				allowed = true
			}
		}
		if !allowed || request.Input == nil || len(request.Key) == 0 || len(request.Key) > 128 || strings.TrimSpace(request.Key) != request.Key || len([]rune(request.Reason)) > 500 {
			return nil, safeToolError("invalid delegation request")
		}
		canonical, _ := json.Marshal(request)
		digest := sha256.Sum256(canonical)
		previous, exists := b.requests[request.Key]
		if exists && previous.Digest != digest {
			return nil, safeToolError("request_key conflicts with a previous request")
		}
		if exists && previous.RunID != "" {
			return previous.Summary, nil
		}
		if !exists {
			if len(b.requests) >= 64 {
				return nil, safeToolError("delegation request limit reached")
			}
			// An unknown creation outcome must be retried with the original key;
			// otherwise a transport failure could accidentally start another child.
			for key, pending := range b.requests {
				if pending.Terminal {
					continue
				}
				if pending.RunID == "" {
					return nil, safeToolError("retry the unresolved delegation request first")
				}
				state, err := b.callbacks.ReadRun(callCtx, pending.RunID)
				if err != nil {
					return nil, err
				}
				if !terminalRun(state) {
					return nil, safeToolError("wait for the active delegated Run first")
				}
				pending.Terminal = true
				b.requests[key] = pending
			}
			b.requests[request.Key] = delegationRequest{Digest: digest}
		}
		keyDigest := sha256.Sum256([]byte(b.runID + "\x00" + request.Key))
		result, err := b.callbacks.CallAgent(callCtx, request.Target, request.Input, openlinker.RuntimeCallOptions{
			Reason: request.Reason, IdempotencyKey: "native-delegation-" + hex.EncodeToString(keyDigest[:]),
		})
		if err != nil {
			if reason := definiteRejection(err); reason != "" && !exists {
				// A prior ambiguous attempt may already have created a child. A
				// rejection of its retry cannot prove that earlier attempt failed.
				delete(b.requests, request.Key)
				return nil, safeToolError(reason)
			}
			return nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		var summary openlinker.RuntimeRunSummary
		if json.Unmarshal(encoded, &summary) != nil || !uuidPattern.MatchString(summary.RunID) {
			return nil, safeToolError("invalid delegated Run response")
		}
		b.requests[request.Key] = delegationRequest{Digest: digest, RunID: summary.RunID, Summary: summary}
		return summary, nil
	case "get_delegated_run", "wait_delegated_run":
		var request struct {
			RunID  string `json:"child_run_id"`
			WaitMS int    `json:"wait_ms,omitempty"`
		}
		if err := decodeArguments(raw, &request); err != nil {
			return nil, err
		}
		if !uuidPattern.MatchString(request.RunID) || request.WaitMS < 0 || request.WaitMS > 30000 || (name == "get_delegated_run" && request.WaitMS != 0) {
			return nil, safeToolError("invalid delegated Run read")
		}
		duration := time.Duration(request.WaitMS) * time.Millisecond
		if name == "wait_delegated_run" && duration == 0 {
			duration = 30 * time.Second
		}
		deadline := time.Now().Add(duration)
		readCtx := callCtx
		if duration > 0 {
			var stopRead context.CancelFunc
			readCtx, stopRead = context.WithTimeout(callCtx, duration)
			defer stopRead()
		}
		var last *openlinker.RuntimeDelegatedRun
		for {
			result, err := b.callbacks.ReadRun(readCtx, request.RunID)
			if err != nil {
				if last != nil && errors.Is(err, context.DeadlineExceeded) && b.ctx.Err() == nil {
					return last, nil
				}
				return nil, err
			}
			if result == nil {
				return nil, safeToolError("missing delegated Run result")
			}
			last = result
			if terminalRun(result) {
				b.markTerminal(request.RunID)
			}
			remaining := time.Until(deadline)
			if result.Status != openlinker.RuntimeRunRunning || remaining <= 0 {
				return result, nil
			}
			timer := time.NewTimer(min(time.Second, remaining))
			select {
			case <-b.ctx.Done():
				timer.Stop()
				return nil, b.ctx.Err()
			case <-timer.C:
			}
		}
	default:
		return nil, safeToolError("unknown delegation tool")
	}
}

func (b *Broker) tools() []any {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	readProperties := func() map[string]any {
		return map[string]any{"child_run_id": map[string]any{"type": "string", "format": "uuid"}}
	}
	waitProperties := readProperties()
	waitProperties["wait_ms"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 30000, "default": 30000}
	return []any{
		map[string]any{"name": "delegate_agent", "description": "Delegate an explicit task/context to an allowed Agent. Reuse request_key for retries. One child may run at a time. This returns a Run ID; use wait_delegated_run to obtain its result before finishing.", "inputSchema": object(map[string]any{
			"target_agent_id": map[string]any{"type": "string", "enum": b.targets},
			"input":           map[string]any{"type": "object"}, "reason": map[string]any{"type": "string", "maxLength": 500},
			"request_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		}, "target_agent_id", "input", "request_key")},
		map[string]any{"name": "get_delegated_run", "description": "Read status, output and error of a direct child Run of this active parent.", "inputSchema": object(readProperties(), "child_run_id")},
		map[string]any{"name": "wait_delegated_run", "description": "Wait up to 30 seconds for a direct child result. A running response requires another wait. The wait stops with the parent Attempt.", "inputSchema": object(waitProperties, "child_run_id")},
	}
}

// EnsureComplete prevents the parent provider's prose from disguising an
// unresolved creation or a still-running child as a successful delegation.
func (b *Broker) EnsureComplete() error {
	b.calls.Lock()
	defer b.calls.Unlock()
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	for key, request := range b.requests {
		if request.Terminal {
			continue
		}
		if request.RunID == "" {
			return safeToolError("delegation creation is unresolved; retry its request_key before finishing")
		}
		run, err := b.callbacks.ReadRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if !terminalRun(run) {
			return safeToolError("delegated Run is unfinished: " + request.RunID)
		}
		request.Terminal = true
		b.requests[key] = request
	}
	return nil
}

// Terminal Runtime states are immutable. Cache only that fact, not live status
// or output, so later creations never reread the whole history over the network.
func (b *Broker) markTerminal(runID string) {
	for key, request := range b.requests {
		if request.RunID == runID {
			request.Terminal = true
			b.requests[key] = request
		}
	}
}

func terminalRun(run *openlinker.RuntimeDelegatedRun) bool {
	if run == nil {
		return false
	}
	switch run.Status {
	case openlinker.RuntimeRunSuccess, openlinker.RuntimeRunFailed, openlinker.RuntimeRunCanceled, openlinker.RuntimeRunTimeout:
		return true
	default:
		return false
	}
}

// Only known Core rejections with matching HTTP status prove no child was
// created. Transport/decode errors, timeouts, 5xx and unknown errors stay pending.
// NOT_FOUND is ambiguous: Core also uses it if the post-creation summary is
// unavailable, so HTTP 404 alone cannot establish that no child was created.
// Never include Core's message/body, which can contain credentials or internals.
func definiteRejection(err error) string {
	var e *openlinker.Error
	if !errors.As(err, &e) {
		return ""
	}
	switch {
	case e.StatusCode == http.StatusForbidden && (e.Code == "PERMISSION_DENIED" || e.Code == "FORBIDDEN"):
		return "Delegation rejected: permission denied. No child was created; you may continue or choose another allowed Agent."
	case e.StatusCode == http.StatusUnauthorized && e.Code == "UNAUTHORIZED":
		return "Delegation rejected: authorization is invalid. No child was created."
	case (e.StatusCode == http.StatusUnprocessableEntity && e.Code == "VALIDATION_FAILED") || (e.StatusCode == http.StatusBadRequest && e.Code == "BAD_REQUEST"):
		return "Delegation rejected: invalid request. No child was created; correct the request before retrying."
	case e.StatusCode == http.StatusTooManyRequests && e.Code == "RATE_LIMITED":
		return "Delegation rejected: rate limit reached. No child was created; retry later."
	default:
		return ""
	}
}
