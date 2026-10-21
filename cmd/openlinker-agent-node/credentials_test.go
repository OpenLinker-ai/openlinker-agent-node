package main

import (
	"context"
	"encoding/json"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeCredentialPrepareBinaryPreservesIdentityWithoutProviderStartup(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "agent-node")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build independent Node: %v\n%s", err, out)
	}
	execute := func(args []string) ([]byte, bool) {
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = append(os.Environ(), "OPENLINKER_NODE_ID=11111111-1111-4111-8111-111111111111", "OPENLINKER_AGENT_ID=22222222-2222-4222-8222-222222222222", "OPENLINKER_URL="+server.URL, "OPENLINKER_AGENT_NODE_ADAPTER=invalid-must-not-initialize", "OPENLINKER_AGENT_NODE_CAPACITY=invalid-must-not-parse")
		out, err := command.CombinedOutput()
		return out, err != nil
	}
	dataDir := t.TempDir()
	store, err := openlinker.OpenFileRuntimeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	identity := store.Identity()
	args := []string{"runtime-credential", "prepare", "--data-dir", dataDir, "--node-id", "11111111-1111-4111-8111-111111111111", "--agent-id", "22222222-2222-4222-8222-222222222222", "--worker-id", identity.WorkerID}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := append([]string(nil), args...)
	wrong[5] = "33333333-3333-4333-8333-333333333333"
	if out, failed := execute(wrong); !failed {
		t.Fatalf("wrong configured Node accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "runtime-credential.json")); !os.IsNotExist(err) {
		t.Fatal("wrong Node wrote credentials")
	}
	var prepared openlinker.PreparedRuntimeCredential
	out, failed := execute(args)
	if failed || json.Unmarshal(out, &prepared) != nil {
		t.Fatalf("prepare failed: %s", out)
	}
	if prepared.WorkerID != identity.WorkerID || prepared.SessionEpoch != identity.SessionEpoch || prepared.NodeID != args[5] || prepared.AgentID != args[7] || len(prepared.PublicKeyThumbprint) != 64 || strings.Contains(string(out), "PRIVATE KEY") {
		t.Fatalf("unexpected public preparation receipt: %s", out)
	}
	out, failed = execute(args)
	var again openlinker.PreparedRuntimeCredential
	if failed || json.Unmarshal(out, &again) != nil || again.PublicKeyThumbprint != prepared.PublicKeyThumbprint || again.SessionEpoch != identity.SessionEpoch {
		t.Fatalf("preparation is not stable: %s", out)
	}
	store, err = openlinker.OpenFileRuntimeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Identity().WorkerID != identity.WorkerID || store.Identity().SessionEpoch != identity.SessionEpoch+1 {
		t.Fatal("preparation modified persisted Worker epoch")
	}
	if out, failed = execute(args); !failed {
		t.Fatalf("running Runtime store accepted: %s", out)
	}
	if out, failed = execute(append(args, "unexpected")); !failed {
		t.Fatalf("unexpected argument accepted: %s", out)
	}
	if requests.Load() != 0 {
		t.Fatal("offline preparation contacted a service")
	}
}
