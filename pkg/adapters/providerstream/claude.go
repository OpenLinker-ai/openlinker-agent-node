package providerstream

import "strings"

// NewClaudeObserver normalizes Claude JSONL progress without forwarding tool
// arguments, result content, names or IDs. An embedding product may suppress
// selected tool progress; suppression covers both halves of a tool lifecycle.
// The callback is local policy, not provider-supplied executable configuration.
func NewClaudeObserver(emit func(string, any) error, suppressTool func(name string) bool) *Observer {
	active := map[string]string{}
	return &Observer{emit: emit, progress: func(event map[string]any) []map[string]any {
		var payloads []map[string]any
		progress := func(phase, kind string) {
			payloads = append(payloads, map[string]any{
				"provider": "claude", "status": "provider_tool_" + phase,
				"phase": phase, "tool_kind": kind,
			})
		}
		block := func(content map[string]any) {
			switch stringValue(content["type"]) {
			case "tool_use":
				id, name := stringValue(content["id"]), stringValue(content["name"])
				if id == "" || active[id] != "" || len(active) >= 512 {
					return
				}
				kind := "tool"
				switch {
				case name == "Bash":
					kind = "command"
				case name == "WebSearch" || name == "WebFetch":
					kind = "web_search"
				case strings.HasPrefix(name, "mcp__"):
					kind = "mcp_tool"
				}
				if suppressTool != nil && suppressTool(name) {
					kind = "suppressed"
				}
				active[id] = kind
				if kind != "suppressed" {
					progress("started", kind)
				}
			case "tool_result":
				id := stringValue(content["tool_use_id"])
				kind := active[id]
				if kind == "" {
					return
				}
				delete(active, id)
				if kind == "suppressed" {
					return
				}
				phase := "completed"
				if failed, _ := content["is_error"].(bool); failed {
					phase = "failed"
				}
				progress(phase, kind)
			}
		}
		switch stringValue(event["type"]) {
		case "stream_event":
			nested, _ := event["event"].(map[string]any)
			switch stringValue(nested["type"]) {
			case "message_start":
				payloads = append(payloads, map[string]any{"provider": "claude", "status": "provider_processing", "phase": "started"})
			case "content_block_start":
				content, _ := nested["content_block"].(map[string]any)
				block(content)
			}
		case "assistant", "user":
			message, _ := event["message"].(map[string]any)
			contents, _ := message["content"].([]any)
			for _, content := range contents {
				value, _ := content.(map[string]any)
				block(value)
			}
		case "system":
			if event["subtype"] == "api_retry" {
				payloads = append(payloads, map[string]any{"provider": "claude", "status": "provider_retrying", "phase": "retrying"})
			}
		}
		return payloads
	}}
}
