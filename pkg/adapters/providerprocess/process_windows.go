//go:build windows

package providerprocess

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func Configure(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		killer := exec.Command("taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F") // #nosec G204 -- PID is process-owned.
		if err := killer.Run(); err != nil {
			if killErr := command.Process.Kill(); killErr != nil {
				return fmt.Errorf("terminate process tree: %w", err)
			}
		}
		return nil
	}
	command.WaitDelay = 2 * time.Second
}
