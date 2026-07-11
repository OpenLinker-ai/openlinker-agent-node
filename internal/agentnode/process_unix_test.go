//go:build unix

package agentnode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandAdapterCancellationTerminatesProcessGroup(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	adapter := CommandAdapter{
		Command: "/bin/sh",
		Args: []string{
			"-c",
			`sleep 60 & child=$!; printf '%s\n' "$child" > "$1"; wait "$child"`,
			"openlinker-runtime-test",
			pidPath,
		},
		Timeout: time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := adapter.Run(ctx, JSONMap{"task": "cancel process tree"}, RunContext{})
		result <- err
	}()

	var childPID int
	eventuallyForTest(t, 2*time.Second, func() bool {
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && childPID > 0
	}, "command child PID")
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled command returned no error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled command did not return")
	}
	eventuallyForTest(t, 3*time.Second, func() bool {
		err := syscall.Kill(childPID, 0)
		return errors.Is(err, syscall.ESRCH)
	}, "command child process to exit with its process group")
}
