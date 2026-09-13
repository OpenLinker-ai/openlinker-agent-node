package codexturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexhome"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

func TestCodexRPCFixtureProcess(t *testing.T) { providertest.CodexRPCFixtureProcess() }

func TestCanceledRequestDoesNotPrepareCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prepared := false
	_, _, err := Run(ctx, Config{Prepare: func(context.Context) (PreparedCommand, error) {
		prepared = true
		return PreparedCommand{}, errors.New("must not prepare")
	}})
	if !errors.Is(err, context.Canceled) || prepared {
		t.Fatalf("canceled request prepared a command: %v", err)
	}
}

func TestCancellationDuringPreparationDoesNotStartAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var command *exec.Cmd
	cleanups := 0
	_, _, err := Run(ctx, Config{Prepare: func(processCtx context.Context) (PreparedCommand, error) {
		executable, err := os.Executable()
		if err != nil {
			return PreparedCommand{}, err
		}
		command = exec.CommandContext(processCtx, executable, "-test.run=^$")
		// This context intentionally differs from the command context. Without
		// the shared pre-Start check, Cmd.Start would create a real process.
		cancel()
		return PreparedCommand{Command: command, Cleanup: func() { cleanups++ }}, nil
	}})
	if !errors.Is(err, context.Canceled) || command == nil || command.Process != nil || cleanups != 1 {
		t.Fatalf("late cancellation started a process or missed cleanup: %v, cleanups=%d", err, cleanups)
	}
}

func TestPreparationAndStartFailuresReleaseFactoryResources(t *testing.T) {
	for _, phase := range []string{"prepare", "missing-command", "start"} {
		t.Run(phase, func(t *testing.T) {
			cleanups := 0
			_, _, err := Run(context.Background(), Config{Prepare: func(ctx context.Context) (PreparedCommand, error) {
				prepared := PreparedCommand{Cleanup: func() { cleanups++ }}
				if phase == "prepare" {
					return prepared, errors.New("preparation failed")
				}
				if phase == "start" {
					prepared.Command = exec.CommandContext(ctx, filepath.Join(t.TempDir(), "absent"))
				}
				return prepared, nil
			}})
			if err == nil || cleanups != 1 {
				t.Fatalf("failure leaked factory resources: %v, %d", err, cleanups)
			}
		})
	}
}

func TestNativeFactoryCleanupFollowsProcessShutdown(t *testing.T) {
	if !codexhome.Supported {
		t.Skip("native CODEX_HOME requires POSIX")
	}
	workspace := t.TempDir()
	bin := filepath.Join(workspace, "codex")
	providertest.WriteCodexRPCFixture(t, bin, "ephemeral")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var prepared PreparedCommand
	var home string
	cleanups := 0
	_, answer, err := Run(ctx, Config{Sandbox: "read-only", Prompt: "task", Prepare: func(ctx context.Context) (PreparedCommand, error) {
		var err error
		prepared, err = PrepareNative(ctx, NativeCommand{Bin: bin, Workspace: workspace, Arguments: func(string) []string { return nil }})
		if err != nil {
			return prepared, err
		}
		for _, value := range prepared.Command.Env {
			if strings.HasPrefix(value, "CODEX_HOME=") {
				home = strings.TrimPrefix(value, "CODEX_HOME=")
			}
		}
		cleanup := prepared.Cleanup
		prepared.Cleanup = func() {
			cleanups++
			if prepared.Command.ProcessState == nil || !prepared.Command.ProcessState.Exited() {
				t.Error("native home removed before waiting for app-server")
			}
			cleanup()
		}
		return prepared, nil
	}})
	if err != nil || answer != "final answer" || home == "" || cleanups != 1 {
		t.Fatalf("native lifecycle failed: %v %q, cleanups=%d", err, answer, cleanups)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("temporary native home survived cleanup", err)
	}
}

func TestConfiguredPermissionsSurviveStartAndResume(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("profile=%v/resume=%v", configured, resume), func(t *testing.T) {
				dir := t.TempDir()
				bin := filepath.Join(dir, "codex")
				log := filepath.Join(dir, "rpc")
				providertest.WriteCodexRPCFixture(t, bin, "ephemeral")
				session := ""
				if resume {
					session = providertest.FixtureThread
				}
				overrides := map[string]json.RawMessage{}
				_, _, err := Run(context.Background(), Config{Sandbox: "workspace-write", UseConfiguredPermissions: configured, SessionID: session, Prompt: "fixture", ThreadConfig: overrides, BeforeThread: func(context.Context, *codexrpc.Client) error {
					if configured {
						overrides["permissions.openlinker_session.filesystem"] = json.RawMessage(`{"/literal.path/helper":"read"}`)
					}
					return nil
				}, Prepare: func(ctx context.Context) (PreparedCommand, error) {
					cmd := exec.CommandContext(ctx, bin)
					cmd.Env = append(os.Environ(), "TEST_LOG="+log)
					return PreparedCommand{Command: cmd, Workspace: dir}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(log + ".requests")
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.Contains(line, "thread/start") || strings.Contains(line, "thread/resume") {
						if strings.Contains(line, `"sandbox":"workspace-write"`) == configured {
							t.Fatalf("wrong legacy sandbox override: %s", line)
						}
						if strings.Contains(line, `"/literal.path/helper":"read"`) != configured {
							t.Fatalf("lost dynamic permission override: %s", line)
						}
					}
				}
			})
		}
	}
}
