package adapters

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func TestHostAuthPermitAcrossProcessesCancellationAndCrash(t *testing.T) {
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	path, err := hostAuthLockPath(c)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestHostAuthPermitChild$")
	child.Env = append(append([]string(nil), c.Env...), "OPENLINKER_TEST_AUTH_GATE_PROVIDER="+c.Provider)
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "HELD\n" {
		t.Fatalf("child failed to acquire: %q %v", line, err)
	}
	waitCtx, stop := context.WithCancel(ctx)
	waited := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		release, err := acquireHostAuthPermit(waitCtx, c, func(string, any) error { waited <- struct{}{}; return nil })
		if err == nil {
			_ = release()
			err = errors.New("acquired while another process held the group")
		}
		done <- err
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("waiting cancellation lost:", err)
	}
	// Waiting cancellation must not release the other process's ownership.
	if lock, err := appfiles.AcquireLock(path); !errors.Is(err, appfiles.ErrLockBusy) {
		if lock != nil {
			_ = lock.Release()
		}
		t.Fatalf("holder lost its lock: %v", err)
	}
	parallel := c
	parallel.HostAuthConcurrency = "client-managed"
	release, err := acquireHostAuthPermit(ctx, parallel, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = release()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	// No persistent busy flag or stale-file deletion is required after a crash.
	release, err = acquireHostAuthPermit(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	// Only this unique test group is removed, after every owner has exited.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestHostAuthPermitChild(t *testing.T) {
	group := os.Getenv("OPENLINKER_TEST_AUTH_GATE_PROVIDER")
	if group == "" {
		return
	}
	if sentinel := os.Getenv("OPENLINKER_TEST_PRIVATE_TMP_SENTINEL"); sentinel != "" {
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("test did not enter a private /tmp:", err)
		}
	}
	c := ProviderConfig{Provider: group, Env: os.Environ(), sandbox: &sessionsandbox.Session{}}
	release, err := acquireHostAuthPermit(context.Background(), c, func(string, any) error { _, err := os.Stdout.WriteString("WAITING\n"); return err })
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, _ = os.Stdout.WriteString("HELD\n")
	// Parent kills this helper to prove kernel cleanup instead of defer cleanup.
	for {
		time.Sleep(time.Second)
	}
}

func TestHostAuthPermitRejectsUnsafeLockWithoutRetry(t *testing.T) {
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	path, err := hostAuthLockPath(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = acquireHostAuthPermit(ctx, c, nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsafe path retried or accepted:", err)
	}
}

func TestHostAuthPermitWaitTimeoutAndEventFailure(t *testing.T) {
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	path, err := hostAuthLockPath(c)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := appfiles.AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = acquireHostAuthPermit(ctx, c, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting did not respect Run deadline: %v", err)
	}
	emitErr := errors.New("fixture event transport closed")
	if _, err := acquireHostAuthPermit(context.Background(), c, func(string, any) error { return emitErr }); !errors.Is(err, emitErr) {
		t.Fatalf("waiting ignored event failure: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := acquireHostAuthPermit(ctx, c, nil)
	if err != nil {
		t.Fatal("failed wait leaked admission:", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestHostAuthStateDirectoryAndHomeAliases(t *testing.T) {
	c := isolationConfig(t, "codex")
	home := t.TempDir()
	c.Env = []string{"HOME=" + home, "TMPDIR=" + t.TempDir(), "XDG_STATE_HOME=" + t.TempDir()}
	// Existing conventional XDG parents can stay readable; Node's directory is private.
	if err := os.Mkdir(filepath.Join(home, ".local"), 0755); err != nil {
		t.Fatal(err)
	}
	path, err := hostAuthLockPath(c)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalProspectivePath(home), ".local/state/openlinker-agent-node/host-auth-codex.lock")
	if path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("state is not private", err)
	}
	alias := filepath.Join(t.TempDir(), "home-alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	c.Env = []string{"HOME=" + alias}
	other, err := hostAuthLockPath(c)
	if err != nil || path != other {
		t.Fatal("HOME alias split the group", err)
	}
}

func TestHostAuthStateRejectsUnsafeDirectories(t *testing.T) {
	for _, target := range []string{"HOME", ".local", ".local/state", ".local/state/openlinker-agent-node"} {
		for _, kind := range []string{"shared-write", "symlink", "file"} {
			if target == "HOME" && kind == "symlink" {
				continue
			} // Canonical HOME aliases are supported.
			t.Run(target+"/"+kind, func(t *testing.T) {
				c := isolationConfig(t, "codex")
				home := filepath.Join(t.TempDir(), "home")
				c.Env = []string{"HOME=" + home}
				path := home
				if target != "HOME" {
					path = filepath.Join(home, target)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "shared-write":
					if err := os.Mkdir(path, 0777); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0777); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					destination := t.TempDir()
					if err := os.Symlink(destination, path); err != nil {
						t.Fatal(err)
					}
					defer func() {
						entries, err := os.ReadDir(destination)
						if err != nil || len(entries) != 0 {
							t.Error("followed unsafe state symlink", err)
						}
					}()
				case "file":
					if err := os.WriteFile(path, []byte("leave me alone"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := hostAuthLockPath(c); err == nil {
					t.Fatal("accepted unsafe host state")
				}
				if kind == "shared-write" {
					info, _ := os.Stat(path)
					if info.Mode().Perm() != 0777 {
						t.Fatal("silently chmodded operator directory")
					}
				}
			})
		}
	}
	c := isolationConfig(t, "codex")
	c.Env = []string{"HOME=relative-home"}
	if _, err := hostAuthLockPath(c); err == nil {
		t.Fatal("relative HOME accepted")
	}
	c.Env = []string{"PATH=/bin"}
	if _, err := hostAuthLockPath(c); err == nil {
		t.Fatal("missing HOME fell back to personal home")
	}
}

func TestHostAuthStateCannotBeGrantedToTools(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, grant := range []string{"read", "write"} {
			t.Run(provider+"/"+grant, func(t *testing.T) {
				c := isolationConfig(t, provider)
				path, err := hostAuthLockPath(c)
				if err != nil {
					t.Fatal(err)
				}
				state := filepath.Dir(path)
				if grant == "read" {
					c.SessionIsolation.ReadPaths = []string{state}
				} else {
					c.SessionIsolation.Root = state
				}
				_, close, err := prepareIsolatedSession(context.Background(), c, isolationRun("unsafe-root"))
				if err == nil {
					_ = close()
					t.Fatal("granted coordination state to tools")
				}
				if !strings.Contains(err.Error(), "cannot") {
					t.Fatalf("failed for wrong reason: %v", err)
				}
			})
		}
	}
}
