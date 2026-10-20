package agentdelegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const MetadataKey = "_openlinker_mcp_bindings"
const CallMetadataKey = "_openlinker_mcp_binding"
const Feature = "mcp_bindings.v1"

type BoundTool struct {
	BindingID   string         `json:"binding_id"`
	TargetID    string         `json:"target_agent_id"`
	Digest      string         `json:"digest"`
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}
type BoundSnapshot struct {
	Schema int         `json:"schema_version"`
	Tools  []BoundTool `json:"tools"`
}

var namePattern = regexp.MustCompile(`^mcp_[a-f0-9]{32}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidateBoundTools(tools []BoundTool) error {
	if len(tools) > 16 {
		return errors.New("too many MCP bindings")
	}
	seen := map[string]bool{}
	for _, t := range tools {
		raw, err := json.Marshal(t)
		if err != nil || len(raw) > 20*1024 || !uuidPattern.MatchString(t.BindingID) || !uuidPattern.MatchString(t.TargetID) || !digestPattern.MatchString(t.Digest) || !namePattern.MatchString(t.Name) || seen[t.Name] || t.InputSchema["type"] != "object" {
			return errors.New("invalid MCP binding snapshot")
		}
		seen[t.Name] = true
	}
	return nil
}
func DecodeBound(value any, trusted bool) ([]BoundTool, string, error) {
	if value == nil {
		return nil, "", nil
	}
	raw, err := json.Marshal(value)
	var snapshot BoundSnapshot
	if err != nil || len(raw) > 320*1024 || json.Unmarshal(raw, &snapshot) != nil || snapshot.Schema != 1 {
		return nil, "", errors.New("invalid MCP binding snapshot")
	}
	if len(snapshot.Tools) > 0 && !trusted {
		return nil, "", errors.New("MCP bindings require trusted Runtime authority")
	}
	if err = ValidateBoundTools(snapshot.Tools); err != nil {
		return nil, "", err
	}
	sort.Slice(snapshot.Tools, func(i, j int) bool { return snapshot.Tools[i].Name < snapshot.Tools[j].Name })
	if len(snapshot.Tools) == 0 {
		return nil, "", nil
	}
	raw, _ = json.Marshal(snapshot.Tools)
	digest := sha256.Sum256(raw)
	return snapshot.Tools, hex.EncodeToString(digest[:]), nil
}
func BoundToolNames(targets []string, tools []BoundTool) []string {
	names := []string{}
	if len(targets) > 0 {
		names = append(names, ToolNames...)
	}
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}
func BoundReceipt(tools []BoundTool) map[string]any {
	refs := []map[string]string{}
	for _, t := range tools {
		refs = append(refs, map[string]string{"binding_id": t.BindingID, "digest": t.Digest})
	}
	return map[string]any{"bindings": refs}
}

func (b *Broker) callTool(name string, raw []byte) (any, error) {
	for _, tool := range b.boundTools {
		if tool.Name != name {
			continue
		}
		if !b.toolsReady.Load() {
			if b.onTools != nil {
				if err := b.onTools(); err != nil {
					return nil, err
				}
			}
			b.toolsReady.Store(true)
		}
		var args struct {
			Arguments map[string]any `json:"arguments"`
			Key       string         `json:"request_key"`
		}
		if err := decodeArguments(raw, &args); err != nil {
			return nil, err
		}
		if args.Arguments == nil || len(args.Key) == 0 || len(args.Key) > 128 {
			return nil, safeToolError("arguments and request_key are required")
		}
		// Namespace keys so two tools cannot accidentally collide. The model never
		// controls target IDs, binding references, credentials or callback metadata.
		digest := sha256.Sum256([]byte(tool.BindingID + "\x00" + args.Key))
		request, _ := json.Marshal(map[string]any{"target_agent_id": tool.TargetID, "input": args.Arguments, "request_key": "mcp-" + hex.EncodeToString(digest[:])})
		result, err := b.callWithBinding("delegate_agent", request, &tool)
		if err != nil {
			return nil, err
		}
		summary, ok := result.(openlinker.RuntimeRunSummary)
		if !ok {
			return nil, safeToolError("invalid MCP execution response")
		}
		wait, _ := json.Marshal(map[string]any{"child_run_id": summary.RunID, "wait_ms": 30000})
		state, err := b.call("wait_delegated_run", wait)
		if err != nil {
			return nil, err
		}
		run, ok := state.(*openlinker.RuntimeDelegatedRun)
		if !ok {
			return nil, safeToolError("missing MCP execution result")
		}
		if run.Status == openlinker.RuntimeRunSuccess {
			if b.callbacks.BoundToolUsed != nil {
				if err := b.callbacks.BoundToolUsed(tool); err != nil {
					return nil, err
				}
			}
			return run.Output, nil
		}
		if run.Status == openlinker.RuntimeRunRunning {
			return map[string]any{"status": "running", "retry": "Call this tool again with the same arguments and request_key to wait for its result."}, nil
		}
		return nil, safeToolError("MCP tool execution failed. Its access may have changed; ask the Agent owner to review the tool configuration.")
	}
	if len(b.targets) == 0 {
		return nil, safeToolError("unknown MCP tool")
	}
	return b.call(name, raw)
}
