package providertest

import (
	"reflect"
	"testing"
)

// SessionHistory runs the same identity/cursor contract against each product's
// own typed message, persistence adapter and continuation policy.
func SessionHistory[T any](t *testing.T, first, corrected T, hash func(T) string, save func(string, []T) error, filter func(string, []T) []T) {
	t.Helper()
	const frozenFullMessage = "68d5f28e827db848332fe1833e8e66517ffb3e4f03dbe1523b59c713abf836a8"
	if hash(first) != frozenFullMessage {
		t.Fatal("production ConversationMessage full-field hash changed")
	}
	all := []T{first, corrected}
	if err := save("old-session", all[:1]); err != nil {
		t.Fatal(err)
	}
	if got := filter("old-session", all); !reflect.DeepEqual(got, all[1:]) {
		t.Fatal("same-session positive control failed", got)
	}
	for _, id := range []string{"different-session", ""} {
		if got := filter(id, all); !reflect.DeepEqual(got, all) {
			t.Fatal("unbound session omitted history", id, got)
		}
	}
	if err := save("new-session", all[1:]); err != nil {
		t.Fatal(err)
	}
	if got := filter("new-session", all); !reflect.DeepEqual(got, all[:1]) {
		t.Fatal("new session inherited previous cursor", got)
	}
	if got := filter("old-session", all); !reflect.DeepEqual(got, all) {
		t.Fatal("superseded session consumed successor cursor", got)
	}
	if err := save("new-session", all[:1]); err != nil {
		t.Fatal(err)
	}
	if got := filter("new-session", all); len(got) != 0 {
		t.Fatal("same session lost saved cursor", got)
	}
}
