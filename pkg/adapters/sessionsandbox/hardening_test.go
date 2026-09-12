package sessionsandbox

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemporaryRootValidation(t *testing.T) {
	c := configForTest(t)
	for _, root := range []string{"relative", "/", "/tmp/*", "/tmp/bad\npath"} {
		c.TempRoot = root
		if c.Validate() == nil {
			t.Fatalf("accepted temporary root %q", root)
		}
	}
	if (Config{Mode: "off", TempRoot: "/tmp/private"}).Validate() == nil {
		t.Fatal("ignored temporary root while mode is off")
	}
}

func TestNativeSandboxConfiguredTemporaryRoot(t *testing.T) {
	c := realConfig(t)
	root, err := os.MkdirTemp("/tmp", "olt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	c.TempRoot = root
	s, err := Open(context.Background(), c, Scope("configured-temp"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s.temp) != resolved {
		t.Fatal("configured temporary storage was ignored")
	}
	if out, err := runShell(t, s, `printf temporary > "$TMPDIR/probe"; cat "$TMPDIR/probe"`); err != nil || out != "temporary" {
		t.Fatalf("private configured temp is unusable: %v %q", err, out)
	}
	s.config.ReadPaths = []string{root}
	if _, err := s.Command(context.Background(), "/bin/true", nil, nil); err == nil {
		t.Fatal("read grant exposed the parent of other invocations' temporary data")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invocation left data in configured temporary storage")
	}

	link := filepath.Join(root, "linked")
	if err := os.Symlink(c.Root, link); err != nil {
		t.Fatal(err)
	}
	c.TempRoot = link
	if _, err := Open(context.Background(), c, Scope("bad-link")); err == nil {
		t.Fatal("symlink temporary root accepted")
	}
	c.TempRoot = filepath.Join(root, strings.Repeat("x", 50))
	if _, err := Open(context.Background(), c, Scope("too-long")); err == nil || !strings.Contains(err.Error(), "40 bytes") {
		t.Fatalf("socket path length was not rejected clearly: %v", err)
	}
}

func TestNativeSandboxExplicitAddressDenyContract(t *testing.T) {
	s, err := Open(context.Background(), realConfig(t), Scope("address-policy"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	policy, err := s.settings("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	values := policy["network"].(map[string]any)["deniedResolvedAddresses"].([]string)
	var prefixes []netip.Prefix
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	blocked := func(value string) bool {
		address := netip.MustParseAddr(value)
		for _, prefix := range prefixes {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	for _, address := range []string{"0.0.0.0", "127.1.2.3", "169.254.169.254", "169.254.1.1", "::", "::1", "fe80::1", "febf::1",
		"10.1.2.3", "172.31.0.1", "192.168.0.1", "100.100.100.200", "fc00::1", "168.63.129.16", "192.0.0.192", "224.0.0.1", "ff02::1"} {
		if !blocked(address) {
			t.Fatalf("reserved destination %s missing from explicit deny policy", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if blocked(address) {
			t.Fatalf("public destination %s incorrectly denied", address)
		}
	}
}
