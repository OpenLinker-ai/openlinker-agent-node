package adapters

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func TestHostAuthPermitAcrossProcessesCancellationAndCrash(t *testing.T) {
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	// Unique group avoids coordinating with any real Node running this test user.
	c.Provider = fmt.Sprintf("fixture-%d", os.Getpid())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestHostAuthPermitChild$")
	child.Env = []string{"OPENLINKER_TEST_AUTH_GATE_GROUP=" + c.Provider}
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
	if lock, err := appfiles.AcquireLock(hostAuthLockPath(c.Provider)); !errors.Is(err, appfiles.ErrLockBusy) {
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
	if err := os.Remove(hostAuthLockPath(c.Provider)); err != nil {
		t.Fatal(err)
	}
}

func TestHostAuthPermitChild(t *testing.T) {
	group := os.Getenv("OPENLINKER_TEST_AUTH_GATE_GROUP")
	if group == "" {
		return
	}
	c := ProviderConfig{Provider: group, sandbox: &sessionsandbox.Session{}}
	release, err := acquireHostAuthPermit(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("HELD")
	// Parent kills this helper to prove kernel cleanup instead of defer cleanup.
	for {
		time.Sleep(time.Second)
	}
}

func TestHostAuthPermitRejectsUnsafeLockWithoutRetry(t *testing.T) {
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	c.Provider = fmt.Sprintf("unsafe-fixture-%d", os.Getpid())
	path := hostAuthLockPath(c.Provider)
	if err := os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := acquireHostAuthPermit(ctx, c, nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsafe path retried or accepted:", err)
	}
}

func TestHostAuthPermitWaitTimeoutAndEventFailure(t *testing.T) {
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	c.Provider = fmt.Sprintf("wait-fixture-%d", os.Getpid())
	lock, err := appfiles.AcquireLock(hostAuthLockPath(c.Provider))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hostAuthLockPath(c.Provider))
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := acquireHostAuthPermit(ctx, c, nil); !errors.Is(err, context.DeadlineExceeded) {
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
