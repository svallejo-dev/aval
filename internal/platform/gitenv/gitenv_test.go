package gitenv

import (
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	t.Parallel()
	env := []string{"PATH=/bin", "GIT_DIR=/r/.git", "GIT_INDEX_FILE=/r/.git/index", "GIT_AUTHOR_NAME=a", "GIT_DIRX=1"}
	want := []string{"PATH=/bin", "GIT_AUTHOR_NAME=a", "GIT_DIRX=1"}
	if got := Clean(env); !reflect.DeepEqual(got, want) {
		t.Errorf("Clean = %q, want %q", got, want)
	}
	if len(env) != 5 || env[1] != "GIT_DIR=/r/.git" {
		t.Errorf("Clean changed its argument: %q", env)
	}
}

// TestLocalMatchesGit checks the list against the installed git, which
// may know more variables than the one it was copied from.
func TestLocalMatchesGit(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Skipf("git rev-parse --local-env-vars: %v", err)
	}
	for v := range strings.FieldsSeq(string(out)) {
		if !slices.Contains(local, v) {
			t.Errorf("git lists %s, which Clean keeps", v)
		}
	}
}
