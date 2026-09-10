package providerprocess

import (
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentUsesEffectiveIdentityWithoutLeakingParentCredentials(t *testing.T) {
	input := []string{"PATH=/bin", "HOME=/provider", "USER=forged", "USER=other", "OPENLINKER_AGENT_TOKEN=secret", "ANTHROPIC_API_KEY=allowed", "LC_ALL=en_US"}
	snapshot := append([]string(nil), input...)
	got := Environment(input, []string{"ANTHROPIC_API_KEY", "USER"})
	counts := map[string]int{}
	for _, entry := range got {
		key, value, _ := strings.Cut(entry, "=")
		counts[key]++
		if key == "OPENLINKER_AGENT_TOKEN" || (key == "USER" && value != effectiveUsername()) {
			t.Fatalf("unexpected environment key %q", key)
		}
	}
	if effectiveUsername() != "" && counts["USER"] != 1 {
		t.Fatal("effective identity must be present exactly once")
	}
	if counts["ANTHROPIC_API_KEY"] != 1 || counts["LC_ALL"] != 1 || !reflect.DeepEqual(input, snapshot) {
		t.Fatal("allowlist semantics or immutable input changed")
	}
}

func TestWithIdentityIsIdempotent(t *testing.T) {
	one := WithIdentity([]string{"PATH=/bin", "USER=forged"})
	if !reflect.DeepEqual(one, WithIdentity(one)) {
		t.Fatal("identity normalization must be idempotent")
	}
}
