package agentnode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
)

func TestNativeClaudeCredentialMatrix(t *testing.T) {
	const secret = "synthetic-key-must-not-appear-in-diagnostics"
	for _, provider := range []string{"codex", "claude"} {
		for _, delegation := range []bool{false, true} {
			for _, source := range []string{"absent", "whitespace", "environment", "file", "missing-file", "insecure-file", "empty-file", "symlink", "oversize-file", "conflict"} {
				t.Run(provider+"/delegation="+strconv.FormatBool(delegation)+"/"+source, func(t *testing.T) {
					if runtime.GOOS == "windows" && (source == "insecure-file" || source == "symlink") {
						t.Skip("POSIX file permissions/symlink fixture")
					}
					file := filepath.Join(t.TempDir(), "private-key-path")
					env := []string{"PATH=" + os.Getenv("PATH"), "UNRELATED=preserved"}
					sourceValid := source == "environment" || source == "file"
					fileInvalid := source != "absent" && source != "whitespace" && !sourceValid
					if runtime.GOOS == "windows" && source == "file" {
						fileInvalid = true
					}
					switch source {
					case "whitespace":
						env = append(env, "ANTHROPIC_API_KEY= \t ", "ANTHROPIC_API_KEY_FILE=  ")
					case "environment":
						env = append(env, "ANTHROPIC_API_KEY=  "+secret+" \n")
					case "absent":
					default:
						if source != "missing-file" {
							content := secret + "\n"
							if source == "empty-file" {
								content = " \n"
							} else if source == "oversize-file" {
								content = strings.Repeat("x", (64<<10)+1)
							}
							if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
								t.Fatal(err)
							}
							if source == "insecure-file" {
								if err := os.Chmod(file, 0o644); err != nil {
									t.Fatal(err)
								}
							} else if source == "symlink" {
								link := file + "-link"
								if err := os.Symlink(file, link); err != nil {
									t.Fatal(err)
								}
								file = link
							}
						}
						env = append(env, "ANTHROPIC_API_KEY_FILE="+file)
						if source == "conflict" {
							env = append(env, "ANTHROPIC_API_KEY="+secret)
						}
					}
					config := agentexec.ProviderConfig{Provider: provider, Env: env, Bin: filepath.Join(t.TempDir(), "must-not-run")}
					if delegation {
						config.DelegationTargets = []string{"22222222-2222-4222-8222-222222222222"}
					}
					wantFailure := provider == "claude" && (fileInvalid || (delegation && !sourceValid))
					prepared, err := prepareNativeCredentials(config)
					if (err != nil) != wantFailure {
						t.Fatalf("auth policy: %v (want failure=%v)", err, wantFailure)
					}
					if err != nil {
						if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), file) {
							t.Fatal("credential diagnostic leaked secret or path")
						}
						// Both lifecycle startup and direct Run must fail on auth,
						// before the missing provider/host or SDK configuration.
						adapter := &NativeAdapter{Config: config}
						node := &Node{OpenLinkerURL: "http://127.0.0.1:1", Adapter: adapter}
						if startupErr := node.Start(context.Background()); startupErr == nil || !strings.Contains(startupErr.Error(), err.Error()) {
							t.Fatalf("Worker startup bypassed auth gate: %v", startupErr)
						}
						if _, runErr := adapter.Run(context.Background(), "task", RunContext{}); runErr == nil || runErr.Error() != err.Error() {
							t.Fatalf("Run bypassed auth gate: %v", runErr)
						}
						return
					}
					if provider == "claude" && sourceValid && (delegation || source == "file") {
						if !containsEnv(prepared.Env, "ANTHROPIC_API_KEY="+secret) || containsEnvName(prepared.Env, "ANTHROPIC_API_KEY_FILE") {
							t.Fatal("validated key did not reach effective environment or file path escaped")
						}
					}
					if !containsEnv(prepared.Env, "UNRELATED=preserved") {
						t.Fatal("unrelated environment changed")
					}
				})
			}
		}
	}
}

func TestNativeCredentialEnvironmentIsAuthoritativeAndPinned(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-inherited-key")
	t.Setenv("ANTHROPIC_API_KEY_FILE", "")
	config := agentexec.ProviderConfig{Provider: "claude", DelegationTargets: []string{"22222222-2222-4222-8222-222222222222"}}
	adapter := &NativeAdapter{Config: config}
	first, err := adapter.configuration()
	if err != nil || !containsEnv(first.Env, "ANTHROPIC_API_KEY=synthetic-inherited-key") {
		t.Fatal("nil environment not inherited", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "changed-after-preflight")
	second, err := adapter.configuration()
	if err != nil || !containsEnv(second.Env, "ANTHROPIC_API_KEY=synthetic-inherited-key") {
		t.Fatal("execution credentials changed after preparation", err)
	}
	config.Env = []string{}
	if _, err := prepareNativeCredentials(config); err == nil {
		t.Fatal("explicit empty environment used parent credentials")
	}
	config.Env = []string{"ANTHROPIC_API_KEY=first", "ANTHROPIC_API_KEY=last"}
	prepared, err := prepareNativeCredentials(config)
	if err != nil || !containsEnv(prepared.Env, "ANTHROPIC_API_KEY=last") || containsEnv(prepared.Env, "ANTHROPIC_API_KEY=first") {
		t.Fatal("duplicate environment did not use exec's last value", err)
	}
}

func containsEnv(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsEnvName(values []string, name string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, name+"=") {
			return true
		}
	}
	return false
}
