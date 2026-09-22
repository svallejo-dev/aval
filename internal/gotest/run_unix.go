//go:build unix

package gotest

import (
	"fmt"
	"os/exec"
	"syscall"
)

// ownProcessGroup starts go in a process group of its own and makes the
// command's cancellation interrupt the whole group, so the test binaries go
// test started stop with it instead of outliving it. The returned kill ends
// whatever in the group ignored the interrupt.
func ownProcessGroup(cmd *exec.Cmd) (kill func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
			return fmt.Errorf("interrupt go test: %w", err)
		}
		return nil
	}
	return func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}
