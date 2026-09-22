//go:build !unix

package gotest

import "os/exec"

// ownProcessGroup keeps exec's default where there are no process groups:
// cancelling kills go itself, and a test binary it started may outlive it.
func ownProcessGroup(*exec.Cmd) (kill func()) { return func() {} }
