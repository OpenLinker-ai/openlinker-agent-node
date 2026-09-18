package agentnode

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupLogsNativePolicyBeforeProviderPreflight(t *testing.T) {
	dir := t.TempDir()
	node, err := NewFromEnvMap(Env{
		"OPENLINKER_AGENT_NODE_ADAPTER":         "codex",
		"OPENLINKER_AGENT_NODE_CODEX_BIN":       filepath.Join(dir, "missing-codex"),
		"OPENLINKER_AGENT_NODE_CODEX_WORKSPACE": dir,
		"OPENLINKER_AGENT_TOKEN":                "synthetic-private-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	node.Logger = log.New(&output, "", 0)
	if err := node.Start(context.Background()); err == nil {
		t.Fatal("provider preflight unexpectedly succeeded")
	}
	for _, value := range []string{`"session_isolation":"off"`, `"web_search":true`, `"provider_preflight":"not_run"`, `native_isolation_disabled`} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("missing policy field %s: %s", value, output.String())
		}
	}
	if strings.Contains(output.String(), dir) || strings.Contains(output.String(), "synthetic-private-token") {
		t.Fatal("startup policy log leaked private configuration")
	}
	if node.started || node.worker != nil {
		t.Fatal("failed preflight left a Worker running")
	}
}
