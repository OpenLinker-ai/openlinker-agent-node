package agenthost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostHandshakeRejectsWrongExecutablesAndCapabilities(t *testing.T) {
	for _, test := range []struct {
		output string
		ok     bool
	}{
		{`{"protocol":"openlinker.agent-host.v1","browser_proxy":true}`, true},
		{`{"protocol":"openlinker.agent-host.v0","browser_proxy":true}`, false},
		{`{"protocol":"openlinker.agent-host.v1","delegation_proxy":true}`, false},
		{``, false},
	} {
		bin := filepath.Join(t.TempDir(), "host")
		script := "#!/bin/sh\n[ \"$*\" = 'plugin capabilities' ] || exit 2\n[ -z \"${OPENLINKER_AGENT_TOKEN-}\" ] || exit 3\nprintf '%s\\n' '" + test.output + "'\n"
		if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OPENLINKER_AGENT_TOKEN", "must-not-inherit")
		_, err := Resolve(context.Background(), bin, "browser_proxy")
		if (err == nil) != test.ok {
			t.Fatalf("output %s: %v", test.output, err)
		}
	}
}
