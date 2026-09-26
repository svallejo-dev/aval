//go:build unix

package verify

import "syscall"

// noFollow makes open fail when the last component of the path is a symlink, so
// a test of head that plants one where the base lint configuration goes cannot
// have aval write through it.
const noFollow = syscall.O_NOFOLLOW
