package providersession

import (
	"reflect"
	"testing"
)

// This layout freezes the pre-extraction message JSON field order and omitempty
// behavior; the leaf accepts it without importing either product or the SDK.
type message struct {
	RunID         string         `json:"run_id"`
	EventSequence *int32         `json:"event_sequence,omitempty"`
	Role          string         `json:"role"`
	Content       string         `json:"content"`
	Payload       map[string]any `json:"payload,omitempty"`
	CreatedAt     string         `json:"created_at,omitempty"`
}

func TestHistoryHashAndConservativeCursorBehavior(t *testing.T) {
	first := message{RunID: "run-1", Role: "user", Content: "query"}
	const frozen = "a1f6d2e5acc3844e78bbfdf7340d11a431184861687c5bc9c6a5d81b02647500"
	if HistoryKey(first) != frozen {
		t.Fatal("historical hash format changed")
	}
	corrected, another := first, first
	corrected.Content = "corrected query"
	another.RunID = "run-2"
	all := []message{first, corrected, another}
	seen := RememberHistory([]string(nil), []message{first, first}, 2)
	if !reflect.DeepEqual(seen, []string{frozen}) {
		t.Fatal("duplicate cursor changed order")
	}
	if got := UnseenHistory(all, seen); !reflect.DeepEqual(got, all[1:]) {
		t.Fatal("correction or another Run was suppressed", got)
	}
	if got := UnseenHistory(all, nil); !reflect.DeepEqual(got, all) {
		t.Fatal("missing legacy cursor omitted context")
	}
	seen = RememberHistory(seen, all[1:], 2)
	if len(seen) != 2 || seen[0] != HistoryKey(corrected) || seen[1] != HistoryKey(another) {
		t.Fatal("cursor bound/order changed")
	}
	if got := UnseenHistory(all, seen); !reflect.DeepEqual(got, all[:1]) {
		t.Fatal("evicted context should replay conservatively")
	}
	if got := UnseenHistory(all[1:], seen); got != nil {
		t.Fatal("empty result must retain nil slice encoding")
	}
	if !reflect.DeepEqual(all, []message{first, corrected, another}) {
		t.Fatal("filter mutated caller history")
	}
}
