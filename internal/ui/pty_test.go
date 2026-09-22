//go:build darwin || linux

package ui

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

// TestIsTerminalPTY checks the true case on the slave side of a real
// pseudo-terminal.
func TestIsTerminalPTY(t *testing.T) {
	t.Parallel()

	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close(); _ = master.Close() })
	if !IsTerminal(slave) {
		t.Errorf("IsTerminal(%s) = false, want true", slave.Name())
	}
}

// openTTY opens a terminal device without making it the controlling terminal.
// openPTY, per OS, opens /dev/ptmx with it, unlocks the new pair and opens
// the slave side.
func openTTY(name string) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0) //nolint:gosec // name is /dev/ptmx or the slave the kernel named
	if err != nil {
		return nil, fmt.Errorf("open terminal: %w", err)
	}
	return f, nil
}

func ioctl(f *os.File, req, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg); errno != 0 {
		return errno
	}
	return nil
}
