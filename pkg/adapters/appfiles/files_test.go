package appfiles

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodeStrictJSONPreservesAppCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, raw, wantError string
		want                 int
	}{
		{"valid", `{"value":2}`, "", 2},
		{"omitted", `{}`, "", 1},
		{"null remains allowed", `null`, "", 1},
		{"duplicate last value remains allowed", `{"value":2,"value":3}`, "", 3},
		{"unknown field", `{"other":2}`, `json: unknown field "other"`, 1},
		{"trailing value", `{"value":2} {}`, "unexpected trailing JSON value", 2},
		{"invalid type", `{"value":"two"}`, "json: cannot unmarshal string", 1},
		{"malformed", `{"value":`, "unexpected EOF", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := struct {
				Value int `json:"value"`
			}{Value: 1}
			err := DecodeStrictJSON([]byte(test.raw), &target)
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error=%v, want %q", err, test.wantError)
			}
			if target.Value != test.want {
				t.Fatalf("decoded value=%d, want %d", target.Value, test.want)
			}
		})
	}
}

func TestPrivateWritesKeepAppFormatReplaceAndFailureCleanup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "config.json")
	if err := WritePrivateJSON(path, struct {
		Value int `json:"value"`
	}{Value: 2}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "{\n  \"value\": 2\n}\n" {
		t.Fatalf("changed JSON file format: %q, %v", raw, err)
	}
	if runtime.GOOS != "windows" {
		for target, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("private mode mismatch for %s: %v", filepath.Base(target), err)
			}
		}
	}
	if err := WritePrivateFile(path, []byte("replacement\n")); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateJSON(path, func() {}); err == nil {
		t.Fatal("unsupported JSON value unexpectedly succeeded")
	}
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != "replacement\n" {
		t.Fatal("failed JSON encoding changed the existing file")
	}
	if err := WritePrivateFile(dir, []byte("invalid")); err == nil || err.Error() != "destination is not a regular file" {
		t.Fatalf("directory destination was not rejected: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("unexpected files after replacement/failure: %v, %v", entries, err)
	}
}

func TestPrivateSecretBoundariesAndErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix private-mode semantics are not a new Windows DACL guarantee")
	}
	for _, test := range []struct {
		name, value, want, wantError string
		mode                         os.FileMode
	}{
		{"trim", " \nsynthetic-secret\t", "synthetic-secret", "", 0o600},
		{"maximum", strings.Repeat("x", 64<<10), strings.Repeat("x", 64<<10), "", 0o600},
		{"oversized", strings.Repeat("x", (64<<10)+1), "", "secret file size is invalid", 0o600},
		{"empty", "", "", "secret file size is invalid", 0o600},
		{"whitespace", " \n\t", "", "secret file is empty", 0o600},
		{"group permissions", "synthetic-secret", "", "secret file must not be accessible by group or other users", 0o640},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(path, []byte(test.value), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			value, err := ReadPrivateSecret(path)
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || err.Error() != test.wantError) {
				t.Fatalf("error=%v, want %q", err, test.wantError)
			}
			if value != test.want {
				t.Fatal("secret read returned a different value")
			}
		})
	}
	if _, err := ReadPrivateSecret(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-file error was not preserved: %v", err)
	}
	if _, err := ReadPrivateSecret(t.TempDir()); err == nil || err.Error() != "secret path must be a regular file and not a symlink" {
		t.Fatalf("directory secret was not rejected: %v", err)
	}
}
