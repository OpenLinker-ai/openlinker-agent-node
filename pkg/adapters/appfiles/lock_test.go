//go:build unix || windows

package appfiles

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockExclusiveAcrossProcessesAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caller-selected.lock")
	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, executable, "-test.run=^TestAppFileLockChild$")
	probe.Env = []string{"OPENLINKER_APPFILES_LOCK_HELPER=probe", "OPENLINKER_APPFILES_LOCK_PATH=" + path}
	if err := probe.Run(); err == nil {
		t.Fatal("second process acquired an already-held lock")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 {
			t.Fatalf("unexpected competing-process failure: %v", err)
		}
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, executable, "-test.run=^TestAppFileLockChild$")
	child.Env = []string{"OPENLINKER_APPFILES_LOCK_HELPER=hold", "OPENLINKER_APPFILES_LOCK_PATH=" + path}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	child.Stderr = &diagnostics
	child.WaitDelay = time.Second
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	defer stdin.Close()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "LOCK_HELD\n" {
			t.Fatalf("child did not positively observe holding the lock: %q", line)
		}
	case <-ctx.Done():
		t.Fatal("child lock observation timed out")
	}
	if competing, err := AcquireLock(path); err == nil {
		_ = competing.Release()
		t.Fatal("parent acquired the lock while child was holding it")
	} else if err.Error() != "Agent state is already serving another Runtime Worker" {
		t.Fatalf("unexpected competing lock error: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("child release failed: %v; %s", err, diagnostics.String())
	}
	reopened, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Release(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Release(); err != nil {
		t.Fatal(err)
	}
	if err := (*Lock)(nil).Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAppFileLockChild(t *testing.T) {
	mode := os.Getenv("OPENLINKER_APPFILES_LOCK_HELPER")
	if mode == "" {
		return
	}
	lock, err := AcquireLock(os.Getenv("OPENLINKER_APPFILES_LOCK_PATH"))
	if err != nil {
		if mode == "probe" && err.Error() == "Agent state is already serving another Runtime Worker" {
			os.Exit(23)
		}
		t.Fatal(err)
	}
	if mode == "hold" {
		fmt.Fprintln(os.Stdout, "LOCK_HELD")
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
