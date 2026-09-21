package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompiledConfigurationCheckDoesNotStartProviderOrWorker(t *testing.T) {
	binary := buildTransportFixture(t, "agent-node", ".")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected discovery", 503)
	}))
	defer server.Close()
	for _, provider := range []string{"codex", "claude"} {
		// "" is the unset default, which must report native isolation.
		for _, mode := range []string{"off", "native", ""} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				want := mode
				if want == "" {
					want = "native"
				}
				if want == "native" && (os.Geteuid() == 0 || runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
					t.Skip("native configuration requires non-root macOS/Linux")
				}
				workspace := t.TempDir()
				state, sessions := filepath.Join(workspace, "sdk-state"), filepath.Join(workspace, "sessions")
				name := "OPENLINKER_AGENT_NODE_" + strings.ToUpper(provider)
				env := append(transportEnv(t),
					"OPENLINKER_URL="+server.URL,
					"OPENLINKER_AGENT_NODE_ADAPTER="+provider,
					"OPENLINKER_AGENT_NODE_DATA_DIR="+state,
					"OPENLINKER_AGENT_NODE_SESSION_ISOLATION="+mode,
					"OPENLINKER_AGENT_NODE_CAPACITY=2",
					"OPENLINKER_AGENT_NODE_PUBLIC_A2A=true", "OPENLINKER_AGENT_NODE_HELPER=true",
					name+"_BIN="+filepath.Join(workspace, "provider-must-not-run"),
					name+"_WEB_SEARCH=true", name+"_SESSION_REUSE=true",
					"OPENLINKER_AGENT_TOKEN=synthetic-private-platform-token", "CODEX_API_KEY=synthetic-private-model-key",
				)
				if want == "native" {
					env = append(env, "OPENLINKER_AGENT_NODE_SESSION_ROOT="+sessions)
				} else {
					// Native mode rejects a workspace it would never use.
					env = append(env, name+"_WORKSPACE="+workspace)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, binary, "--check-config")
				command.Env = env
				command.Dir = workspace
				var stderr strings.Builder
				command.Stderr = &stderr
				raw, err := command.Output()
				if err != nil || stderr.Len() != 0 {
					t.Fatalf("configuration-only command failed: %v %s", err, stderr.String())
				}
				var report struct {
					Scope             string
					ProviderPreflight string `json:"provider_preflight"`
					Capacity          int64
					Native            struct {
						Provider            string
						WebSearch           bool   `json:"web_search"`
						SessionIsolation    string `json:"session_isolation"`
						HostAuthConcurrency string `json:"host_auth_concurrency"`
					}
					Warnings []string
				}
				if err := json.Unmarshal(raw, &report); err != nil {
					t.Fatal(err)
				}
				if report.Scope != "configured_policy" || report.ProviderPreflight != "not_run" || report.Capacity != 2 || report.Native.Provider != provider || !report.Native.WebSearch || report.Native.SessionIsolation != want {
					t.Fatalf("incorrect policy report: %s", raw)
				}
				warning := "native_isolation_disabled"
				if want == "native" {
					warning = "serial_host_auth_capacity_gt_one"
					if report.Native.HostAuthConcurrency != "serial" {
						t.Fatalf("missing default auth serialization: %s", raw)
					}
				}
				if !strings.Contains(strings.Join(report.Warnings, ","), warning) {
					t.Fatalf("missing actionable warning %s: %s", warning, raw)
				}
				for _, sensitive := range []string{workspace, "synthetic-private-platform-token", "synthetic-private-model-key", server.URL} {
					if strings.Contains(string(raw), sensitive) {
						t.Fatal("configuration report disclosed non-allowlisted data")
					}
				}
				if requests.Load() != 0 {
					t.Fatal("configuration check started network discovery")
				}
				if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
					t.Fatal("configuration check created state or provider files", err)
				}
			})
		}
	}
	for _, args := range [][]string{{"--check-config"}, {"--check-config", "extra"}} {
		command := exec.Command(binary, args...)
		command.Env = append(transportEnv(t), "OPENLINKER_AGENT_NODE_ADAPTER=codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION=typo-private-do-not-echo")
		var stdout, stderr strings.Builder
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err == nil || stdout.Len() != 0 || strings.Contains(stderr.String(), "typo-private-do-not-echo") {
			t.Fatal("invalid check accepted or leaked invalid value")
		}
	}
}
