package sessionsandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires non-root POSIX host")
	}
	return Config{Mode: "docker", Root: filepath.Join(t.TempDir(), "sessions"), Image: "sha256:" + strings.Repeat("a", 64), Namespace: "core.example"}
}

func TestSessionSandboxPrivatePathsAndScope(t *testing.T) {
	c := testConfig(t)
	root, err := privateRoot(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("root is not private")
	}
	linked := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRoot(linked); err == nil {
		t.Fatal("symlink root accepted")
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRoot(root); err == nil {
		t.Fatal("public root accepted")
	}
	if Scope("a\x00b", "c") == Scope("a", "b\x00c") || Scope("a", "b") == Scope("b", "a") {
		t.Fatal("ambiguous namespace framing")
	}
	for _, value := range []string{"host", "container:other", "default", "net,host", "--privileged"} {
		c.Network = value
		if c.Validate() == nil {
			t.Fatalf("unsafe network accepted: %s", value)
		}
	}
}

func realConfig(t *testing.T) Config {
	t.Helper()
	c := testConfig(t)
	c.Image = os.Getenv("OPENLINKER_TEST_SESSION_IMAGE")
	if c.Image == "" {
		t.Skip("run scripts/test-session-isolation.sh")
	}
	return c
}

func TestDockerSessionLockProcess(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_LOCK_ROOT") == "" {
		t.Skip("subprocess helper")
	}
	c := Config{Mode: "docker", Root: os.Getenv("OPENLINKER_TEST_LOCK_ROOT"), Image: os.Getenv("OPENLINKER_TEST_SESSION_IMAGE"), Namespace: "core.example"}
	session, err := Open(context.Background(), c, "same-session")
	if err == nil {
		_ = session.Close()
		t.Fatal("second process acquired the same session")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("wrong failure: %v", err)
	}
}

func TestDockerSessionCrossProcessLockAndOrphanRecovery(t *testing.T) {
	c := realConfig(t)
	ctx := context.Background()
	session, err := Open(ctx, c, "same-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	other := exec.Command(executable, "-test.run=^TestDockerSessionLockProcess$", "-test.v")
	other.Env = append(os.Environ(), "OPENLINKER_TEST_LOCK_ROOT="+c.Root)
	if out, err := other.CombinedOutput(); err != nil {
		t.Fatalf("lock process: %v\n%s", err, out)
	}
	command, err := session.Command(ctx, "/codex", []string{"child"}, []string{"HOME=/session/home"})
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Wait() })
	heartbeat := filepath.Join(session.data, "workspace", "heartbeat")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(heartbeat); err != nil {
		t.Fatal("orphan fixture did not start")
	}
	// Simulate the OS releasing Node's lock at process death while Docker keeps
	// the client's descendants alive. Next Open must fence before using files.
	if err := session.lock.Release(); err != nil {
		t.Fatal(err)
	}
	session.lock = nil
	restarted, err := Open(ctx, c, "same-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("old container survived ownership recovery")
	}
	if restarted.Store() != session.Store() {
		t.Fatal("Node restart changed the private session mapping")
	}
}

func TestDockerSessionRefusesSymlinkReentryAndHostFallback(t *testing.T) {
	c := realConfig(t)
	session, err := Open(context.Background(), c, "symlink")
	if err != nil {
		t.Fatal(err)
	}
	data := session.data
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, "home")); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(data, "home")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), c, "symlink"); err == nil {
		t.Fatal("unsafe persisted HOME accepted")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatal("Node wrote through native symlink")
	}
	c.Image = "sha256:" + strings.Repeat("f", 64)
	if _, err := Open(context.Background(), c, "missing-image"); err == nil {
		t.Fatal("missing image fell back to a host process")
	}
}

func TestDockerSessionLeavesForeignContainerUntouched(t *testing.T) {
	c := realConfig(t)
	session, err := Open(context.Background(), c, "collision")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := session.control(context.Background(), "container", "create", "--name", session.name, "--label", ownerLabel+"=foreign", c.Image, "/codex", "child")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = session.control(context.Background(), "container", "rm", "--force", id) })
	if _, err := Open(context.Background(), c, "collision"); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("foreign ownership accepted: %v", err)
	}
	if _, err := session.control(context.Background(), "container", "inspect", "--format", "{{.Id}}", id); err != nil {
		t.Fatal("foreign container was removed")
	}
}

func TestSessionSandboxCanceledBeforeFilesystemAccess(t *testing.T) {
	c := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, c, "cancelled"); err != context.Canceled {
		t.Fatalf("cancellation identity lost: %v", err)
	}
	if _, err := os.Stat(c.Root); !os.IsNotExist(err) {
		t.Fatal("cancelled open touched disk")
	}
}

func TestDockerSessionRejectsChangedDaemonBeforeOpeningData(t *testing.T) {
	c := realConfig(t)
	session, err := Open(context.Background(), c, "engine")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.root, "docker-engine"), []byte("another-daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), c, "engine"); err == nil || !strings.Contains(err.Error(), "different Docker daemon") {
		t.Fatalf("daemon switch accepted: %v", err)
	}
}

func TestDockerSessionPinsResolvedEndpoint(t *testing.T) {
	c := realConfig(t)
	session, err := Open(context.Background(), c, "fixed-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(strings.Join(session.env, "\n"), "DOCKER_HOST=unix:///") {
		t.Fatal("Docker endpoint was not pinned")
	}
	for _, entry := range session.env {
		if strings.HasPrefix(entry, "DOCKER_CONTEXT=") {
			t.Fatal("mutable context still overrides the pinned endpoint")
		}
	}
	t.Setenv("DOCKER_CONTEXT", "nonexistent-isolation-fixture-context")
	command, err := session.Command(context.Background(), "/codex", []string{"--version"}, []string{"HOME=/session/home"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := command.Output(); err != nil || strings.TrimSpace(string(out)) != "codex-cli 0.153.0" {
		t.Fatalf("session followed a changed context: %v", err)
	}
}
