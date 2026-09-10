package providersession

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreKeepsLegacyFormatAndExactKeyDomain(t *testing.T) {
	const key = "8433f70bde587dd3c1c61314631fa3e23f31b596cd472dafa3eb6ac55693b88c"
	if Key(" claude ", "workspace", " root-context ") != key || KeyHash("claude", "workspace", "root-context") != key[:24] {
		t.Fatal("pre-extraction key domain changed")
	}
	if Path("  /explicit/store  ", "ignored", "ignored") != "/explicit/store" {
		t.Fatal("configured path semantics changed")
	}
	path := filepath.Join(t.TempDir(), "session.json")
	legacy := []byte(`{"sessions":{"` + key + `":{"session_id":"legacy","session_key_hash":"` + key[:24] + `","workspace":"workspace"}}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	read := Read(path)
	if read.Sessions[key].SessionID != "legacy" || read.Sessions[key].ClientModeGeneration != 0 {
		t.Fatal(read)
	}
	if err := Update(path, func(store *Store) bool { delete(store.Sessions, key); return false }); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(legacy) {
		t.Fatal("read/no-op rewrote legacy file", err)
	}
	record := Record{SessionID: "next", SessionKeyHash: key[:24], Workspace: "workspace", ClientMode: "standard", ClientModeGeneration: 7, HistorySeen: []string{"seen"}, UpdatedAt: "2026-09-10T00:00:00Z"}
	if err := Update(path, func(store *Store) bool { store.Sessions[key] = record; return true }); err != nil {
		t.Fatal(err)
	}
	if got := Read(path).Sessions[key]; !reflect.DeepEqual(got, record) {
		t.Fatalf("record changed: %#v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Freeze wire field names independently of the public Record type.
	want := map[string]any{"sessions": map[string]any{key: map[string]any{"session_id": "next", "session_key_hash": key[:24], "workspace": "workspace", "client_mode": "standard", "client_mode_generation": float64(7), "history_seen": []any{"seen"}, "updated_at": "2026-09-10T00:00:00Z"}}}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("wire format changed: %s %v", raw, err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatal("lost owner-only mode", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".sessions-*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal("temporary session files remain")
	}
}

func TestStoreReadsUnsafeOrInvalidRecordsAsEmpty(t *testing.T) {
	for _, raw := range []string{"not-json", "null", `{"sessions":null}`, strings.Repeat("x", MaxStoreBytes+1)} {
		path := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if store := Read(path); store.Sessions == nil || len(store.Sessions) != 0 {
			t.Fatal("invalid file accepted")
		}
	}
	if runtime.GOOS == "windows" {
		return
	} // Existing Windows ownership semantics are not POSIX permissions.
	for _, mode := range []string{"symlink", "world-readable", "directory"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target, path := filepath.Join(dir, "target"), filepath.Join(dir, "store")
			original := []byte(`{"sessions":{"key":{"session_id":"protected"}}}`)
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "world-readable":
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if len(Read(path).Sessions) != 0 {
				t.Fatal("unsafe file accepted")
			}
			err := Update(path, func(store *Store) bool { store.Sessions["new"] = Record{SessionID: "new"}; return true })
			if err == nil || !strings.Contains(err.Error(), "owner-only regular non-symlink") {
				t.Fatal("unsafe replacement accepted", err)
			}
			preserved, err := os.ReadFile(target)
			if err != nil || string(preserved) != string(original) {
				t.Fatal("target overwritten", err)
			}
		})
	}
}

func TestStoreUpdateSerializesAndRejectsOversizeWithoutReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	var group sync.WaitGroup
	errors := make(chan error, 32)
	for index := range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			errors <- Update(path, func(store *Store) bool {
				store.Sessions[fmt.Sprint(index)] = Record{SessionID: fmt.Sprint(index)}
				return true
			})
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(Read(path).Sessions) != 32 {
		t.Fatal("lost a concurrent update")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = Update(path, func(store *Store) bool {
		store.Sessions["huge"] = Record{SessionID: strings.Repeat("x", MaxStoreBytes)}
		return true
	})
	if err == nil || err.Error() != "provider session store exceeds size limit" {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed update replaced prior state", err)
	}
}

func TestSessionLockSerializesOnlyMatchingScopeAndReleasesEntry(t *testing.T) {
	release := Lock("claude", "workspace", "same")
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	acquired, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); next := Lock("claude", "workspace", "same"); close(acquired); next() }()
	// Wait for the second caller to register, proving it actually contended.
	deadline := time.Now().Add(time.Second)
	key := Key("claude", "workspace", "same")
	for {
		sessionLocks.Lock()
		refs := sessionLocks.entries[key].refs
		sessionLocks.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("positive contention observation timed out")
		}
		runtime.Gosched()
	}
	select {
	case <-acquired:
		t.Fatal("same scope was not serialized")
	default:
	}
	other := Lock("codex", "workspace", "same")
	other()
	release()
	released = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waiter did not resume")
	}
	sessionLocks.Lock()
	_, retained := sessionLocks.entries[key]
	sessionLocks.Unlock()
	if retained {
		t.Fatal("released scope leaked lock entry")
	}
}
