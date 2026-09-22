package ui

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

func openPTY() (master, slave *os.File, err error) {
	if master, err = openTTY("/dev/ptmx"); err != nil {
		return nil, nil, err
	}
	var unlock int32
	var n uint32
	if err = ioctl(master, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err == nil { //nolint:gosec // the ioctl reads the lock flag
		err = ioctl(master, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))) //nolint:gosec // the ioctl writes the pty number into n
	}
	if err == nil {
		slave, err = openTTY("/dev/pts/" + strconv.FormatUint(uint64(n), 10))
	}
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
