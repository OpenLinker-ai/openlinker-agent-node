package main

import (
	"context"
	"fmt"
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

func TestVersionCommandHasNoStartupSideEffects(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "agent-node")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-mod=readonly", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build version-command fixture: %v\n%s", err, output)
	}
	// This executable leaves an observable marker whenever it is invoked. A
	// positive control proves the marker works before --version must avoid it.
	marker := filepath.Join(root, "provider-was-executed")
	provider := filepath.Join(root, "provider")
	if runtime.GOOS == "windows" {
		provider += ".exe"
	}
	providerSource := filepath.Join(root, "provider.go")
	source := fmt.Sprintf(`package main
import ("os"; "fmt")
func main() {
  if err := os.WriteFile(%q, []byte("executed"), 0600); err != nil { os.Exit(2) }
  fmt.Println("2.1.259 (Claude Code)")
}
`, marker)
	if err := os.WriteFile(providerSource, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	buildProvider := exec.CommandContext(buildCtx, "go", "build", "-o", provider, providerSource)
	buildProvider.Env = append(os.Environ(), "GOWORK=off")
	if output, err := buildProvider.CombinedOutput(); err != nil {
		t.Fatalf("build provider sentinel: %v\n%s", err, output)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProbe()
	if err := exec.CommandContext(probeCtx, provider, "--version").Run(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "executed" {
		t.Fatalf("provider sentinel positive control failed: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "startup must not reach discovery", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, scenario := range []struct {
		name        string
		args        []string
		env         []string
		wantSuccess bool
	}{
		{name: "empty configuration", args: []string{"--version"}, wantSuccess: true},
		{name: "invalid configuration ignored", args: []string{"--version"}, wantSuccess: true,
			env: []string{"OPENLINKER_AGENT_NODE_CAPACITY=invalid", "OPENLINKER_AGENT_NODE_ADAPTER=invalid"}},
		{name: "configured worker and provider never start", args: []string{"--version"}, wantSuccess: true,
			env: []string{"OPENLINKER_AGENT_NODE_ADAPTER=claude", "OPENLINKER_AGENT_NODE_CLAUDE_BIN=" + provider,
				"OPENLINKER_AGENT_NODE_PUBLIC_A2A=true", "OPENLINKER_AGENT_NODE_HELPER=true"}},
		{name: "unknown arguments fail closed", args: []string{"--unsupported"}},
		{name: "version with extra arguments fails closed", args: []string{"--version", "serve"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			workspace := t.TempDir()
			state := filepath.Join(workspace, "must-not-create-state")
			providerWorkspace := filepath.Join(workspace, "provider-workspace")
			if err := os.Mkdir(providerWorkspace, 0o700); err != nil {
				t.Fatal(err)
			}
			env := []string{
				"HOME=" + workspace, "USERPROFILE=" + workspace,
				"OPENLINKER_URL=" + server.URL,
				"OPENLINKER_AGENT_ID=11111111-1111-4111-8111-111111111111",
				"OPENLINKER_AGENT_TOKEN=synthetic-version-test-token",
				"OPENLINKER_AGENT_NODE_DATA_DIR=" + state,
				"OPENLINKER_AGENT_NODE_CLAUDE_WORKSPACE=" + providerWorkspace,
				"OPENLINKER_AGENT_NODE_VERSION=must-not-override-build-identity",
			}
			if runtime.GOOS == "windows" {
				env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
			}
			env = append(env, scenario.env...)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, scenario.args...)
			command.Dir, command.Env = workspace, env
			var stderr strings.Builder
			command.Stderr = &stderr
			stdout, err := command.Output()
			if ctx.Err() != nil {
				t.Fatalf("version command exceeded deadline: %v", ctx.Err())
			}
			if scenario.wantSuccess {
				if err != nil || string(stdout) != "openlinker-agent-node/dev\n" || stderr.Len() != 0 {
					t.Fatalf("version result: error=%v stdout=%q stderr=%q", err, stdout, stderr.String())
				}
			} else if err == nil || len(stdout) != 0 || !strings.Contains(stderr.String(), "usage:") {
				t.Fatalf("invalid args must fail without starting: error=%v stdout=%q stderr=%q", err, stdout, stderr.String())
			}
			if requests.Load() != 0 {
				t.Fatalf("read-only command made %d discovery requests", requests.Load())
			}
			for _, forbidden := range []string{state, marker} {
				if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
					t.Fatalf("startup path must remain absent (%s): %v", filepath.Base(forbidden), err)
				}
			}
			if entries, err := os.ReadDir(providerWorkspace); err != nil || len(entries) != 0 {
				t.Fatalf("provider workspace changed: %v", err)
			}
		})
	}
}
