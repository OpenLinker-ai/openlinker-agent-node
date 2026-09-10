//go:build linux

package agentdelegation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestBrokerCrossUIDProviderBoundary(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise the production Runtime/Provider UID boundary")
	}
	directory, err := os.MkdirTemp("", "ol-delegation-uid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(directory, "broker")
	if err := os.Mkdir(root, 0o710); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, 10001, 10002); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, os.ModeSetgid|0o710); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(directory, "worker-secret")
	if err := os.WriteFile(secret, []byte("must remain Runtime private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(secret, 10001, 10001); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "helper")
	if err := os.WriteFile(binary, raw, 0o555); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	command := func(role string, uid uint32, socket string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestDelegationUIDHelper$")
		cmd.Env = []string{"OPENLINKER_UID_TEST=" + role, "BROKER_ROOT=" + root, "BROKER_SOCKET=" + socket, "WORKER_SECRET=" + secret}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		return cmd
	}
	server := command("broker", 10001, "")
	input, _ := server.StdinPipe()
	output, _ := server.StdoutPipe()
	var diagnostics bytes.Buffer
	server.Stderr = &diagnostics
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if err := server.Wait(); err != nil {
			t.Errorf("broker helper: %v %s", err, diagnostics.String())
		}
	})
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		t.Fatal("broker did not publish its socket")
	}
	socket := scanner.Text()
	if !strings.HasPrefix(socket, root+"/") {
		t.Fatal("unexpected broker socket")
	}
	for _, uid := range []uint32{10002, 10003} {
		result, err := command("provider", uid, socket).CombinedOutput()
		if uid == 10002 {
			if err != nil || !bytes.Contains(result, []byte("uid-child-proof")) {
				t.Fatalf("Provider could not complete delegation: %v %s", err, result)
			}
		} else if err == nil {
			t.Fatal("unrelated UID accessed the delegation socket")
		}
	}
}

func TestDelegationUIDHelper(t *testing.T) {
	role := os.Getenv("OPENLINKER_UID_TEST")
	if role == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if role == "broker" {
		broker, err := Start(ctx, os.Getenv("BROKER_ROOT"), parentID, []string{targetID}, Callbacks{
			CallAgent: func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
				return openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunRunning, DispatchState: openlinker.RuntimeDispatchPending}, nil
			},
			ReadRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
				return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunSuccess, DispatchState: openlinker.RuntimeDispatchTerminal}, Output: map[string]any{"summary": "uid-child-proof"}}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer broker.Close()
		fmt.Println(broker.Socket)
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if _, err := os.ReadFile(os.Getenv("WORKER_SECRET")); err == nil {
		t.Fatal("Provider read Runtime credentials")
	}
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for index, request := range []map[string]any{
		{"method": "initialize", "params": map[string]any{}},
		{"method": "tools/call", "params": map[string]any{"name": "delegate_agent", "arguments": map[string]any{"target_agent_id": targetID, "input": map[string]any{"text": "test"}, "request_key": "uid-test"}}},
		{"method": "tools/call", "params": map[string]any{"name": "wait_delegated_run", "arguments": map[string]any{"child_run_id": childID, "wait_ms": 100}}},
	} {
		request["jsonrpc"] = "2.0"
		request["id"] = index + 1
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	if err := Proxy(ctx, &input, os.Stdout, os.Getenv("BROKER_SOCKET")); err != nil {
		t.Fatal(err)
	}
}
