package sessionsandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func configForTest(t *testing.T) Config {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("requires non-root macOS/Linux")
	}
	return Config{Mode: "native", Root: filepath.Join(t.TempDir(), "private"), Namespace: "https://core.example", RuntimeBin: os.Getenv("OPENLINKER_TEST_NATIVE_SANDBOX_BIN")}
}

func realConfig(t *testing.T) Config {
	t.Helper()
	c := configForTest(t)
	if c.RuntimeBin == "" {
		t.Skip("set OPENLINKER_TEST_NATIVE_SANDBOX_BIN to run real OS enforcement tests")
	}
	return c
}

func TestNativeConfigurationRejectsWeakOrAmbiguousPolicy(t *testing.T) {
	c := configForTest(t)
	for _, change := range []func(*Config){
		func(c *Config) { c.Mode = "docker" }, func(c *Config) { c.Mode = "auto" }, func(c *Config) { c.Root = "relative" },
		func(c *Config) { c.Root = "/" }, func(c *Config) { c.Namespace = "" }, func(c *Config) { c.ReadPaths = []string{"/Users/*"} },
		func(c *Config) { c.AllowedDomains = []string{"*"} }, func(c *Config) { c.AllowedDomains = []string{"127.0.0.1"} },
		func(c *Config) { c.AllowedDomains = []string{"api.example:80"} }, func(c *Config) { c.AllowedDomains = []string{"api.local"} },
		func(c *Config) { c.AllowedDomains = []string{"api..example"} }, func(c *Config) { c.AllowedDomains = []string{"api.example/"} },
	} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted unsafe config: %+v", bad)
		}
	}
	c.AllowedDomains = []string{"api.openai.com", "api.anthropic.com:443"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if (Config{Mode: "off", Root: "/ignored"}).Validate() == nil {
		t.Fatal("ignored isolation options")
	}
}

func TestPrivateSessionStateRejectsSymlinksAndSharedPermissions(t *testing.T) {
	c := configForTest(t)
	p := t.TempDir()
	if err := os.Symlink(p, c.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRoot(c.Root); err == nil {
		t.Fatal("symlink root accepted")
	}
	_ = os.Remove(c.Root)
	if err := os.Mkdir(c.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRoot(c.Root); err == nil {
		t.Fatal("shared root accepted")
	}
	_ = os.Chmod(c.Root, 0o700)
	if _, err := privateRoot(c.Root); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRuntimeCannotSilentlyFallback(t *testing.T) {
	c := configForTest(t)
	c.RuntimeBin = filepath.Join(t.TempDir(), "missing-srt")
	if _, err := Open(context.Background(), c, Scope("a")); err == nil {
		t.Fatal("missing backend accepted")
	}
	if _, err := os.Stat(c.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing backend mutated persistent state")
	}
	// A generic executable that prints a version is not a pinned runtime.
	c.RuntimeBin = "/bin/echo"
	if _, err := Open(context.Background(), c, Scope("a")); err == nil {
		t.Fatal("fake runtime accepted")
	}
}

func TestNativeSandboxEnvironmentRejectsLoaderAndParentProxyChannels(t *testing.T) {
	s := &Session{data: "/session/data", temp: "/session/temp"}
	env := s.Environment([]string{"PATH=/usr/bin:/bin", "CODEX_API_KEY=synthetic-key", "HOME=/personal", "NODE_OPTIONS=--require /private/loader", "LD_PRELOAD=/private/loader", "HTTP_PROXY=http://localhost:1234", "HTTPS_PROXY=http://localhost:1234", "ALL_PROXY=socks5://localhost:1234", "SSH_AUTH_SOCK=/private/socket", "OPENLINKER_AGENT_TOKEN=synthetic-token"})
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"/personal", "/private", "NODE_OPTIONS", "LD_PRELOAD", "PROXY=", "SSH_AUTH_SOCK", "OPENLINKER_AGENT_TOKEN"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unsafe wrapper environment %s", forbidden)
		}
	}
	for _, required := range []string{"PATH=/usr/bin:/bin", "CODEX_API_KEY=synthetic-key", "HOME=/session/data/home", "TMPDIR=/session/temp"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("lost required environment %s", required)
		}
	}
}

func runShell(t *testing.T, s *Session, script string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := s.Command(ctx, "/bin/sh", append([]string{"-c", script, "fixture"}, args...), []string{"PATH=" + os.Getenv("PATH")})
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestNativeSandboxRealFilesPersistenceAndScope(t *testing.T) {
	c := realConfig(t)
	ctx := context.Background()
	a, err := Open(ctx, c, Scope("codex", "agent", "caller", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runShell(t, a, `printf secret-A > memory; cat memory`); err != nil || !strings.Contains(out, "secret-A") {
		t.Fatalf("own write/read: %v %s", err, out)
	}
	amemory := filepath.Join(a.Workspace(), "memory")
	if _, err := Open(ctx, c, Scope("codex", "agent", "caller", "a")); err == nil {
		t.Fatal("concurrent owner accepted")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(ctx, c, Scope("codex", "agent", "caller", "b"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	host := filepath.Join(t.TempDir(), "host-private-canary")
	if err := os.WriteFile(host, []byte("host-private"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{amemory, host, b.policy, b.Store(), "/var/run/docker.sock"} {
		if target == b.Store() {
			if err := os.WriteFile(target, []byte("mapping-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		before, beforeErr := os.ReadFile(target)
		out, err := runShell(t, b, `if cat "$1" >/dev/null 2>&1; then exit 90; fi
rm -f escape; ln -s "$1" escape
if cat escape >/dev/null 2>&1; then exit 92; fi
rm -f hardlink
if ln "$1" hardlink 2>/dev/null && cat hardlink >/dev/null 2>&1; then exit 94; fi
if /bin/sh -c 'cat "$1" >/dev/null 2>&1' child "$1"; then exit 93; fi
(printf overwrite > "$1") 2>/dev/null || :
printf blocked`, target)
		if err != nil || !strings.Contains(out, "blocked") {
			t.Fatalf("read/write/symlink/child boundary for %s: %v %s", target, err, out)
		}
		// Linux may permit a new file in a private namespace's synthetic parent
		// directory. Assert the real host file, not that harmless placeholder.
		after, afterErr := os.ReadFile(target)
		if string(before) != string(after) || (beforeErr == nil) != (afterErr == nil) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
			t.Fatalf("sandbox modified actual host/control file %s", target)
		}
	}
	a, err = Open(ctx, c, Scope("codex", "agent", "caller", "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if out, err := runShell(t, a, `cat memory`); err != nil || !strings.Contains(out, "secret-A") {
		t.Fatalf("A-B-A persistence: %v %s", err, out)
	}
	for _, parts := range [][]string{{"codex", "agent", "caller-2", "a"}, {"codex", "agent-2", "caller", "a"}, {"claude", "agent", "caller", "a"}} {
		other, err := Open(ctx, c, Scope(parts...))
		if err != nil {
			t.Fatal(err)
		}
		if other.Workspace() == a.Workspace() {
			t.Fatal("authority collision")
		}
		_ = other.Close()
	}
	otherCore := c
	otherCore.Namespace = "https://other-core.example"
	other, err := Open(ctx, otherCore, Scope("codex", "agent", "caller", "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Workspace() == a.Workspace() {
		t.Fatal("Core namespace collision")
	}
	if out, err := runShell(t, b, `test ! -e memory`); err != nil {
		t.Fatalf("B inherited A state: %v %s", err, out)
	}
}

func TestNativeSandboxCanceledContextNeverStartsClient(t *testing.T) {
	c := configForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, c, Scope("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open ignored cancellation: %v", err)
	}
	if _, err := os.Stat(c.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled Open mutated state")
	}
}

func TestNativeSandboxRealNoHostSocketsOrNetworkBypass(t *testing.T) {
	c := realConfig(t)
	s, err := Open(context.Background(), c, Scope("network"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out, err := runShell(t, s, `/usr/bin/curl --version`); err != nil || !strings.Contains(out, "curl ") {
		t.Fatalf("network probe positive control failed: %v %s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 6\r\n\r\nLEAKED"))
			_ = c.Close()
		}
	}()
	url := "http://" + listener.Addr().String()
	for _, script := range []string{`/usr/bin/curl --fail --max-time 2 "$1"`, `/usr/bin/curl --fail --max-time 2 --noproxy '*' "$1"`} {
		out, err := runShell(t, s, script, url)
		if err == nil || strings.Contains(out, "LEAKED") {
			t.Fatalf("loopback/proxy bypass: %v %s", err, out)
		}
	}
}

func TestNativeSandboxRealArgumentQuotingAndPolicyTampering(t *testing.T) {
	s, err := Open(context.Background(), realConfig(t), Scope("quoting"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, arg := range []string{"a b", "'\"; $(touch INJECTED); `touch INJECTED`", "--settings=/tmp/evil"} {
		out, err := runShell(t, s, `printf '%s' "$1"; test ! -e INJECTED`, arg)
		if err != nil || !strings.Contains(out, arg) {
			t.Fatalf("argument changed: %v %q", err, out)
		}
	}
	c := s.config
	c.ReadPaths = []string{filepath.Dir(c.Root)}
	s.config = c
	if _, err := s.Command(context.Background(), "/bin/true", nil, nil); err == nil {
		t.Fatal("exposed state through read allowlist")
	}
}

func TestNativeSandboxRealStartupProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Probe(ctx, realConfig(t)); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSandboxRealCancellation(t *testing.T) {
	s, err := Open(context.Background(), realConfig(t), Scope("cancel"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := s.Command(ctx, "/bin/sh", []string{"-c", `(while :; do echo tick >> heartbeat; sleep 0.05; done) & wait`}, []string{"PATH=" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(s.Workspace(), "heartbeat")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			_ = cmd.Wait()
			t.Fatal("sandbox child never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled run succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop client")
	}
	before, _ := os.ReadFile(heartbeat)
	time.Sleep(150 * time.Millisecond)
	after, _ := os.ReadFile(heartbeat)
	if string(before) != string(after) {
		t.Fatal("ordinary descendant survived cancellation")
	}
}
