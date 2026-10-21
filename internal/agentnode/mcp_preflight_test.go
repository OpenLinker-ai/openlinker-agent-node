package agentnode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
)

func TestMCPPreflightDoesNotSilentlyChangeFeatures(t *testing.T) {
	bin := writeFakeCodex(t, "#!/bin/sh\ncase \"$*\" in\n--version) echo 'codex-cli 0.153.0';;\n'app-server --help') echo 'app-server generate-json-schema --listen --config --disable';;\n'plugin capabilities') echo '{\"protocol\":\"openlinker.agent-host.v1\",\"delegation_proxy\":true}';;\n*) exit 99;;\nesac\n")
	for _, failure := range []string{"proxy", "broker"} {
		t.Run(failure, func(t *testing.T) {
			config := adapters.ProviderConfig{Provider: "codex", Bin: bin, DelegationProxyBin: bin}
			if failure == "proxy" {
				config.DelegationProxyBin = filepath.Join(t.TempDir(), "missing")
			} else {
				config.DelegationBrokerRoot = filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(config.DelegationBrokerRoot, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := &NativeAdapter{Config: config}
			before := a.RuntimeFeatures()
			err := a.Preflight(context.Background())
			if err == nil || !strings.Contains(err.Error(), "MCP delivery preflight") {
				t.Fatalf("missing local cause: %v", err)
			}
			if !slices.Equal(before, a.RuntimeFeatures()) || !slices.Contains(before, agentdelegation.Feature) {
				t.Fatal("capabilities silently shrank")
			}
		})
	}
}
