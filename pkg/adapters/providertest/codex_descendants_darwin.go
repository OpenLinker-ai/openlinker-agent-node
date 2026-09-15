package providertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"golang.org/x/sys/unix"
)

// The executable and OS identity are deliberately identical to the independent
// sentinel. Only ancestry may select a target; names and user-wide kills fail.
func descendantCommand(mode, log string) *exec.Cmd {
	executable, _ := os.Executable()
	command := exec.Command(executable, "-test.run=TestCodexRPCFixtureProcess")
	command.Env = append(os.Environ(), "OPENLINKER_CODEX_RPC_FIXTURE="+mode, "TEST_LOG="+log)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stderr = os.Stderr // Also exercise the inherited diagnostic pipe.
	return command
}

func fixturePID(path string) {
	_ = os.WriteFile(path, []byte(fmt.Sprint(os.Getpid())), 0o600)
}

func codexRPCDescendantFixture(scenario string) bool {
	log := os.Getenv("TEST_LOG")
	if scenario == "descendant-leaf" || scenario == "descendant-child" {
		if scenario == "descendant-child" {
			child := descendantCommand("descendant-leaf", log)
			if child.Start() != nil {
				os.Exit(8)
			}
			fixturePID(log + ".child")
			for i := 0; i < 6000; i++ {
				if _, err := os.Stat(log + ".orphan"); err == nil {
					os.Exit(0)
				}
				time.Sleep(10 * time.Millisecond)
			}
		} else {
			fixturePID(log + ".leaf")
			for i := 0; i < 3000; i++ {
				_ = os.WriteFile(log+".heartbeat.next", []byte(fmt.Sprint(i)), 0o600)
				_ = os.Rename(log+".heartbeat.next", log+".heartbeat")
				time.Sleep(20 * time.Millisecond)
			}
		}
		os.Exit(0)
	}
	if !strings.HasPrefix(scenario, "cancel-descendants-") {
		return false
	}
	f := StartRPCFixture(scenario)
	fixturePID(log + ".provider")
	child := descendantCommand("descendant-child", log)
	if child.Start() != nil {
		os.Exit(8)
	}
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(log + ".leaf"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// All ancestors remain live long enough to be observed before reparenting.
	// This does not pretend to test an unobserved malicious double-fork.
	time.Sleep(250 * time.Millisecond)
	if scenario == "cancel-descendants-reparent" {
		_ = os.WriteFile(log+".orphan", []byte("exit"), 0o600)
		_ = child.Wait()
	}
	f.logFile(".descendants-ready", "yes", false)
	if scenario == "cancel-descendants-provider-exit" {
		go func() {
			for {
				if _, err := os.Stat(log + ".provider-exit"); err == nil {
					os.Exit(0)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	for {
		var message codexrpc.Message
		if f.decoder.Decode(&message) != nil {
			if scenario == "cancel-descendants-graceful" {
				// A real provider may still be unwinding tools after interrupted.
				time.Sleep(100 * time.Millisecond)
				for _, suffix := range []string{".leaf", ".child"} {
					raw, _ := os.ReadFile(log + suffix)
					var pid int
					_, _ = fmt.Sscan(string(raw), &pid)
					if pid > 1 {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
				_ = child.Wait()
				f.logFile(".drained", "yes", false)
				os.Exit(0)
			}
			time.Sleep(60 * time.Second)
			os.Exit(0)
		}
		if message.Method == "turn/interrupt" {
			f.logFile(".interrupt", string(message.Params), false)
			f.reply(message, map[string]any{})
			f.event("turn/completed", map[string]any{"threadId": FixtureThread,
				"turn": map[string]any{"id": FixtureTurn, "status": "interrupted", "items": []any{}}})
		}
	}
}

type fixtureProcessIdentity struct {
	pid   int
	start unix.Timeval
}

func identityFromFile(t *testing.T, path string) fixtureProcessIdentity {
	t.Helper()
	raw, err := os.ReadFile(path)
	var pid int
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(string(raw), &pid); err != nil || pid <= 1 {
		t.Fatal("invalid fixture PID")
	}
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		t.Fatal("fixture positive process evidence missing", err)
	}
	return fixtureProcessIdentity{pid, p.Proc.P_starttime}
}

func fixtureProcessLive(p fixtureProcessIdentity) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", p.pid)
	return err == nil && k.Proc.P_starttime == p.start && k.Proc.P_stat != 5
}

// CodexRPCCancellationStopsDetachedDescendants invokes the consumer's actual
// provider, then checks OS identities before test cleanup. ACK/interrupt or a
// test finalizer cannot satisfy the child-exit assertion.
func CodexRPCCancellationStopsDetachedDescendants(t *testing.T, run CodexRun) {
	t.Helper()
	for _, mode := range []string{"stubborn", "reparent", "graceful", "provider-exit"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			log, sentinelLog := filepath.Join(dir, "owned"), filepath.Join(dir, "other-session")
			bin := filepath.Join(dir, "codex")
			WriteCodexRPCFixture(t, bin, "cancel-descendants-"+mode)
			sentinel := descendantCommand("descendant-leaf", sentinelLog)
			if err := sentinel.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() })
			unrelated := exec.Command("/bin/sleep", "60")
			unrelated.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
			unrelatedInfo, err := unix.SysctlKinfoProc("kern.proc.pid", unrelated.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			unrelatedIdentity := fixtureProcessIdentity{unrelated.Process.Pid, unrelatedInfo.Proc.P_starttime}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			config := CodexConfig{Bin: bin, Workspace: dir, Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
			go func() { done <- run(ctx, config, "task", "") }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(log + ".descendants-ready"); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("real descendants not ready")
				}
				time.Sleep(5 * time.Millisecond)
			}
			owned := []fixtureProcessIdentity{identityFromFile(t, log+".provider"), identityFromFile(t, log+".leaf")}
			if mode != "reparent" {
				owned = append(owned, identityFromFile(t, log+".child"))
			}
			other := identityFromFile(t, sentinelLog+".leaf")
			otherHeartbeat, err := os.ReadFile(sentinelLog + ".heartbeat")
			if err != nil {
				t.Fatal("independent session heartbeat missing")
			}
			for _, p := range owned {
				if !fixtureProcessLive(p) {
					t.Fatal("positive owned-process control missing")
				}
				p := p
				t.Cleanup(func() {
					if fixtureProcessLive(p) {
						_ = syscall.Kill(p.pid, syscall.SIGKILL)
					}
				})
			}
			providerGroup, _ := syscall.Getpgid(owned[0].pid)
			leafGroup, _ := syscall.Getpgid(owned[1].pid)
			if providerGroup == leafGroup {
				t.Fatal("fixture did not escape initial process group")
			}
			if mode == "reparent" {
				leaf, _ := unix.SysctlKinfoProc("kern.proc.pid", owned[1].pid)
				if leaf.Eproc.Ppid != 1 {
					t.Fatal("fixture grandchild was not reparented")
				}
			}
			if mode == "provider-exit" {
				if err := os.WriteFile(log+".provider-exit", []byte("exit"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if mode == "provider-exit" {
					if err == nil {
						t.Fatal("provider exit incorrectly succeeded")
					}
				} else if !errors.Is(err, context.Canceled) {
					t.Fatalf("wrong cancellation result: %v", err)
				} else if err.Error() != context.Canceled.Error() {
					t.Errorf("unexpected cancellation cleanup error: %v", err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("cancellation exceeded bounded cleanup")
			}
			for _, p := range owned {
				if fixtureProcessLive(p) {
					t.Error("owned process survived product cancellation; test cleanup has not run")
				}
			}
			if !fixtureProcessLive(other) {
				t.Fatal("another session's same-command process was stopped")
			}
			if !fixtureProcessLive(unrelatedIdentity) {
				t.Fatal("unrelated sentinel was stopped")
			}
			progressDeadline := time.Now().Add(500 * time.Millisecond)
			for {
				afterHeartbeat, err := os.ReadFile(sentinelLog + ".heartbeat")
				if err == nil && len(afterHeartbeat) != 0 && string(afterHeartbeat) != string(otherHeartbeat) {
					break
				}
				if !fixtureProcessLive(other) || time.Now().After(progressDeadline) {
					t.Fatal("independent session stopped making progress")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if mode == "provider-exit" {
				return
			}
			raw, err := os.ReadFile(log + ".interrupt")
			var scope struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			if err != nil || json.Unmarshal(raw, &scope) != nil || scope.ThreadID != FixtureThread || scope.TurnID != FixtureTurn {
				t.Fatal("exact scoped interrupt missing")
			}
			if mode == "graceful" {
				if _, err := os.Stat(log + ".drained"); err != nil {
					t.Fatal("provider killed before EOF tool cleanup")
				}
			}
		})
	}
}
