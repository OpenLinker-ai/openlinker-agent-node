package adapters

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

// Real mount namespace regression: the helper cannot see the parent's /tmp,
// but mounts the same private HOME inode at a different path. Production
// admission must wait, then acquire when the parent releases its lock.
func TestHostAuthPermitWithPrivateTmp(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_NATIVE_CODEX_BIN") == "" {
		t.Skip("real Linux namespace acceptance job")
	}
	c := isolationConfig(t, "codex")
	c.sandbox = &sessionsandbox.Session{}
	home := t.TempDir()
	c.Env = []string{"HOME=" + home}
	release, err := acquireHostAuthPermit(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	sentinel, err := os.CreateTemp("/tmp", "node-private-tmp-probe-")
	if err != nil {
		t.Fatal(err)
	}
	sentinel.Close()
	defer os.Remove(sentinel.Name())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, "bwrap", "--unshare-user", "--unshare-pid", "--unshare-net", "--die-with-parent", "--tmpfs", "/", "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--ro-bind", "/etc", "/etc", "--tmpfs", "/tmp", "--bind", home, "/fixture-home", "--ro-bind", exe, "/fixture-test", "--proc", "/proc", "--dev", "/dev", "--chdir", "/fixture-home", "--", "/fixture-test", "-test.run=^TestHostAuthPermitChild$")
	child.Env = []string{"HOME=/fixture-home", "OPENLINKER_TEST_AUTH_GATE_PROVIDER=codex", "OPENLINKER_TEST_PRIVATE_TMP_SENTINEL=" + sentinel.Name()}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	reader := bufio.NewReader(out)
	for _, want := range []string{"WAITING\n", "HELD\n"} {
		line, err := reader.ReadString('\n')
		if err != nil || line != want {
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatalf("namespace helper = %q, want %q: %v; stderr=%s", line, want, err, stderr.String())
		}
		if want == "WAITING\n" {
			if err := release(); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Log("private /tmp verified; shared HOME inode serialized across different mount paths")
}
