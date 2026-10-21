package providertest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ClaudeFixtureEvents streams one deterministic Anthropic message to an official
// Claude client. It is a test protocol fixture, not a provider implementation.
func ClaudeFixtureEvents(w http.ResponseWriter, n int, b map[string]any, tool bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(kind string, v any) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, fixtureJSON(v)) }
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprint("msg_fixture_", n), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 1}}})
	start := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": b["text"]}
	stop := "end_turn"
	if tool {
		start = map[string]any{"type": "tool_use", "id": b["id"], "name": b["name"], "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": fixtureJSON(b["input"])}
		stop = "tool_use"
	}
	emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": start})
	emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

func fixtureJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }
