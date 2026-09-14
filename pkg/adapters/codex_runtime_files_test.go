package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexRuntimeGrantsOnlyValidatedCode(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".codex")
	dir := filepath.Join(config, "tmp", "arg0", "codex-arg0-fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(t.TempDir(), "client")
	if err := os.WriteFile(code, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "codex-linux-sandbox")
	if err := os.Symlink(code, alias); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{":minimal": "read", "/private-session": "write"}
	overrides := map[string]json.RawMessage{}
	if err := codexRuntimeFileGrants([]string{"HOME=" + home}, base, overrides); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(overrides["permissions.openlinker_session.filesystem"], &got); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(code)
	want := map[string]string{":minimal": "read", "/private-session": "write", dir: "read", resolved: "read"}
	if !reflect.DeepEqual(got, want) || len(base) != 2 {
		t.Fatalf("grant widened or base mutated: %#v / %#v", got, base)
	}
	// Unexpected files, including auth/history, must never be exposed by the
	// directory mount. The app-server still owns the directory's lifetime.
	for _, name := range []string{"auth.json", "history.jsonl", ".lock"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := codexRuntimeFileGrants([]string{"CODEX_HOME=" + config}, base, map[string]json.RawMessage{}); err == nil {
			t.Fatalf("accepted unexpected helper data %s", name)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alias, []byte("not an alias"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := codexRuntimeFileGrants([]string{"HOME=" + home}, base, map[string]json.RawMessage{}); err == nil {
		t.Fatal("accepted non-symlink helper")
	}
}
