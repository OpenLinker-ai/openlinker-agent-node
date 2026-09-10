package providerstream

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestClaudePolicyHookPairsSuppressionAndKeepsSanitizedProgress(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		var got []map[string]any
		var inspected []string
		observer := NewClaudeObserver(func(event string, payload any) error {
			if event != "run.status.changed" {
				t.Fatalf("event = %s", event)
			}
			got = append(got, payload.(map[string]any))
			return nil
		}, func(name string) bool {
			inspected = append(inspected, name)
			return suppress && name == "mcp__private__hidden"
		})
		lines := []string{
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"private-id","name":"mcp__private__hidden","input":{"secret":"not-for-events"}},{"type":"tool_use","id":"safe-id","name":"Bash","input":{"command":"not-for-events"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"private-id","content":"not-for-events"},{"type":"tool_result","tool_use_id":"safe-id","is_error":true,"content":"not-for-events"}]}}`,
		}
		for _, line := range lines {
			observer.ObserveLine([]byte(line))
		}
		if !reflect.DeepEqual(inspected, []string{"mcp__private__hidden", "Bash"}) {
			t.Fatal(inspected)
		}
		want := 4
		if suppress {
			want = 2
		}
		if len(got) != want {
			t.Fatalf("suppression=%v: got %d events", suppress, len(got))
		}
		for _, payload := range got {
			if len(payload) != 4 || payload["provider"] != "claude" {
				t.Fatal(payload)
			}
			raw, _ := json.Marshal(payload)
			for _, secret := range []string{"not-for-events", "private-id", "safe-id", "Bash", "hidden"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("raw tool data escaped: %s", raw)
				}
			}
		}
		if got[len(got)-1]["status"] != "provider_tool_failed" {
			t.Fatal(got)
		}
	}
}

func TestCodexHookFragmentedJSONLFlushAndNilPolicy(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		var got []map[string]any
		var filter func(map[string]any) bool
		if suppress {
			filter = func(item map[string]any) bool { return item["server"] == "private" }
		}
		observer := NewCodexObserver(func(_ string, payload any) error { got = append(got, payload.(map[string]any)); return nil }, filter)
		data := `{"type":"item.started","item":{"type":"mcp_tool_call","server":"private","tool":"secret-name","arguments":"private-content"}}` + "\n" +
			`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"private","tool":"secret-name"}}` + "\n" +
			`{"type":"item.completed","item":{"type":"command_execution","status":"failed","command":"private-command"}}`
		for i := 0; i < len(data); i += 7 {
			end := min(i+7, len(data))
			if n, err := observer.Write([]byte(data[i:end])); n != end-i || err != nil {
				t.Fatalf("Write: %d %v", n, err)
			}
		}
		observer.Flush()
		want := 3
		if suppress {
			want = 1
		}
		if len(got) != want {
			t.Fatalf("suppression=%v: %+v", suppress, got)
		}
		if got[len(got)-1]["status"] != "provider_tool_failed" {
			t.Fatal(got)
		}
		for _, payload := range got {
			raw, _ := json.Marshal(payload)
			if len(payload) != 4 || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") {
				t.Fatalf("unsafe output: %s", raw)
			}
		}
	}
	var observer *Observer
	observer.Flush()
	observer.ObserveLine([]byte(`{}`))
	if n, err := observer.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("nil observer: %d %v", n, err)
	}
}
