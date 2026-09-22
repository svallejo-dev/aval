package ui

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

func openPTY() (master, slave *os.File, err error) {
	if master, err = openTTY("/dev/ptmx"); err != nil {
		return nil, nil, err
	}
	var name [128]byte
	if err = ioctl(master, syscall.TIOCPTYGRANT, 0); err == nil {
		if err = ioctl(master, syscall.TIOCPTYUNLK, 0); err == nil {
			err = ioctl(master, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))) //nolint:gosec // the ioctl writes the slave's name into name
		}
	}
	if err == nil {
		slave, err = openTTY(string(bytes.TrimRight(name[:], "\x00")))
	}
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
