// Package gitenv keeps the variables that tie git to one repository out of
// the environments aval runs commands with.
//
// A git hook that runs aval exports some of them, GIT_DIR and
// GIT_INDEX_FILE among others. Passed on, they would point every git
// command aval or a test runs, wherever it runs, at the caller's repository
// and index.
package gitenv

import (
	"slices"
	"strings"
)

// local is what `git rev-parse --local-env-vars` prints with git 2.50.
var local = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// Clean returns a copy of env, a list of KEY=VALUE pairs such as
// os.Environ() returns, without the repository-local git variables.
func Clean(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(local, k)
	})
}
