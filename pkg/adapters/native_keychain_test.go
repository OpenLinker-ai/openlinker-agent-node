package adapters

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Only an explicitly named temporary keychain and synthetic item are touched.
// Never query, unlock, or read the operator's login keychain.
func syntheticKeychainProbe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return ""
	}
	path := filepath.Join(t.TempDir(), "fixture.keychain-db")
	service := "openlinker-synthetic-sandbox-fixture"
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/security", args...).CombinedOutput()
	}
	if out, err := run("create-keychain", "-p", "synthetic-only-password", path); err != nil {
		t.Fatalf("create private fixture keychain: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := run("delete-keychain", path); err != nil {
			t.Errorf("remove private fixture keychain: %v: %s", err, out)
		}
	})
	if out, err := run("add-generic-password", "-a", "fixture", "-s", service, "-w", "synthetic-keychain-canary", "-T", "/usr/bin/security", path); err != nil {
		t.Fatalf("fixture keychain item: %v: %s", err, out)
	}
	if out, err := run("find-generic-password", "-a", "fixture", "-s", service, "-w", path); err != nil || strings.TrimSpace(string(out)) != "synthetic-keychain-canary" {
		t.Fatal("keychain positive control failed", err)
	}
	return "/usr/bin/security find-generic-password -a fixture -s " + service + " -w " + jsonString(path) + "\n"
}
