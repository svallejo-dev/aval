//go:build unix

package actions

import "syscall"

// noFollow makes open fail when the last component of the path is a symlink, so
// a GITHUB_STEP_SUMMARY repointed at another file — GITHUB_ENV, say — cannot be
// written through.
const noFollow = syscall.O_NOFOLLOW
