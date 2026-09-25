//go:build !unix

package actions

// noFollow is zero where the platform has no O_NOFOLLOW. The Lstat before the
// open and the Stat after it are the only guard there, and GitHub's runners are
// Linux, macOS or Windows containers where aval's gate does not run.
const noFollow = 0
