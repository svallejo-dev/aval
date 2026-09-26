package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/platform/gitenv"
)

// gitRepo is a git repository built for one test, out of the developer's git
// configuration and out of any repository variable a git hook exported.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", args...) //nolint:gosec // a test helper with the arguments its caller wrote
	cmd.Dir = r.dir
	cmd.Env = append(gitenv.Clean(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=aval", "GIT_AUTHOR_EMAIL=aval@example.com",
		"GIT_COMMITTER_NAME=aval", "GIT_COMMITTER_EMAIL=aval@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files and commits them, and returns the new commit's SHA.
func (r *gitRepo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	writeFiles(r.t, r.dir, files)
	r.git("add", "--all")
	r.git("commit", "--quiet", "--message", msg)
	return r.git("rev-parse", "HEAD")
}

// history builds the shape every range test needs: a base commit on main and a
// head commit on the branch work, with main left where it was.
func (r *gitRepo) history() (base, head string) {
	r.t.Helper()
	base = r.commit("chore: base", map[string]string{"a.txt": "a\n"})
	r.git("checkout", "--quiet", "-b", "work")
	head = r.commit("feat: work", map[string]string{"b.txt": "b\n"})
	return base, head
}

func TestResolveRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// setup prepares the refs the case needs; it runs after history() and
		// may move the head, in which case it returns the new one.
		setup    func(r *gitRepo, base, head string) string
		flags    func(base, head string) rangeFlags
		wantBase func(base, head string) string
		wantCode int
		wantErr  string // substring of the error message
	}{
		{
			name:     "explicit base and head",
			flags:    func(base, head string) rangeFlags { return rangeFlags{base: base, head: head} },
			wantBase: func(base, _ string) string { return base },
		},
		{
			name:     "a short base resolves to the full SHA",
			flags:    func(base, _ string) rangeFlags { return rangeFlags{base: base[:8]} },
			wantBase: func(base, _ string) string { return base },
		},
		{
			name: "the default base is the merge base with origin/main",
			setup: func(r *gitRepo, base, _ string) string {
				r.git("update-ref", "refs/remotes/origin/main", base)
				return ""
			},
			wantBase: func(base, _ string) string { return base },
		},
		{
			name: "origin/main wins over the upstream and over main",
			setup: func(r *gitRepo, base, head string) string {
				r.git("update-ref", "refs/remotes/origin/main", head)
				r.git("update-ref", "refs/remotes/up/main", base)
				r.git("config", "branch.work.remote", "up")
				r.git("config", "branch.work.merge", "refs/heads/main")
				return ""
			},
			wantBase: func(_, head string) string { return head },
		},
		{
			name: "without origin/main the configured upstream decides",
			setup: func(r *gitRepo, base, _ string) string {
				r.git("update-ref", "refs/remotes/up/main", base)
				r.git("config", "branch.work.remote", "up")
				r.git("config", "branch.work.merge", "refs/heads/main")
				return ""
			},
			wantBase: func(base, _ string) string { return base },
		},
		{
			name:     "without a remote the local main decides",
			wantBase: func(base, _ string) string { return base },
		},
		{
			name:     "an unknown head is a usage error",
			flags:    func(_, _ string) rangeFlags { return rangeFlags{head: "nope"} },
			wantCode: ExitUsage,
			wantErr:  `--head "nope" names no commit`,
		},
		{
			name:     "an unknown base is a usage error",
			flags:    func(_, _ string) rangeFlags { return rangeFlags{base: "nope"} },
			wantCode: ExitUsage,
			wantErr:  `--base "nope" names no commit`,
		},
		{
			name:     "a base that looks like a flag is a usage error",
			flags:    func(_, _ string) rangeFlags { return rangeFlags{base: "--upload-pack=touch"} },
			wantCode: ExitUsage,
			wantErr:  `--base "--upload-pack=touch" names no commit`,
		},
		{
			// An orphan branch shares no history with main, so no candidate
			// yields a merge base and the error has to name all three.
			name: "no candidate resolves, and the error says which were tried",
			setup: func(r *gitRepo, _, _ string) string {
				r.git("checkout", "--quiet", "--orphan", "lonely")
				return r.commit("feat: alone", map[string]string{"c.txt": "c\n"})
			},
			flags:    func(_, _ string) rangeFlags { return rangeFlags{} },
			wantCode: ExitUsage,
			wantErr:  `"origin/main", "@{upstream}", "main"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newGitRepo(t)
			base, head := r.history()
			if tt.setup != nil {
				if moved := tt.setup(r, base, head); moved != "" {
					head = moved
				}
			}
			var f rangeFlags
			if tt.flags != nil {
				f = tt.flags(base, head)
			}
			root, g, err := openRepo(t.Context(), r.dir)
			if err != nil {
				t.Fatalf("openRepo: %v", err)
			}
			if root != mustEvalSymlinks(t, r.dir) {
				t.Errorf("root = %q, want %q", root, r.dir)
			}

			gotBase, gotHead, err := resolveRange(t.Context(), g, f, defaultBaseRefs)
			if tt.wantCode != 0 {
				assertExitCode(t, err, tt.wantCode, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("resolveRange: %v", err)
			}
			if want := tt.wantBase(base, head); gotBase != want {
				t.Errorf("base = %s, want %s", gotBase, want)
			}
			if gotHead != head {
				t.Errorf("head = %s, want %s", gotHead, head)
			}
		})
	}
}

// TestResolveRangeEmptyRepository checks the message a repository with no
// commits gets: HEAD resolves to nothing, and "no such revision" would not say
// why.
func TestResolveRangeEmptyRepository(t *testing.T) {
	t.Parallel()
	r := newGitRepo(t)
	_, g, err := openRepo(t.Context(), r.dir)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	_, _, err = resolveRange(t.Context(), g, rangeFlags{}, defaultBaseRefs)
	assertExitCode(t, err, ExitUsage, "this repository has no commits yet")
}

func TestOpenRepo(t *testing.T) {
	t.Parallel()

	t.Run("a directory that is not there is a usage error", func(t *testing.T) {
		t.Parallel()
		_, _, err := openRepo(t.Context(), filepath.Join(t.TempDir(), "nope"))
		assertExitCode(t, err, ExitUsage, "--dir:")
	})

	t.Run("a directory outside a working tree is a usage error", func(t *testing.T) {
		t.Parallel()
		_, _, err := openRepo(t.Context(), t.TempDir())
		assertExitCode(t, err, ExitUsage, "find the repository root of")
	})

	t.Run("a directory below the root resolves to the root", func(t *testing.T) {
		t.Parallel()
		r := newGitRepo(t)
		r.commit("chore: base", map[string]string{"sub/a.txt": "a\n"})
		root, _, err := openRepo(t.Context(), filepath.Join(r.dir, "sub"))
		if err != nil {
			t.Fatalf("openRepo: %v", err)
		}
		if root != mustEvalSymlinks(t, r.dir) {
			t.Errorf("root = %q, want %q", root, r.dir)
		}
	})
}

// TestOpenRepoWithoutGit checks that a missing git is exit 3 (ADR-0005 §6).
// It is not parallel: it empties PATH.
func TestOpenRepoWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, _, err := openRepo(context.Background(), t.TempDir())
	assertExitCode(t, err, ExitTool, "git 2.40 or newer is required")
}

func TestRepoFullName(t *testing.T) {
	t.Parallel()
	for remote, want := range map[string]string{
		"https://github.com/svallejo-dev/aval.git":   "svallejo-dev/aval",
		"https://github.com/svallejo-dev/aval":       "svallejo-dev/aval",
		"https://github.com/svallejo-dev/aval/":      "svallejo-dev/aval",
		"git@github.com:svallejo-dev/aval.git":       "svallejo-dev/aval",
		"git@github.com:svallejo-dev/aval":           "svallejo-dev/aval",
		"ssh://git@github.com/svallejo-dev/aval.git": "svallejo-dev/aval",
		"https://ghe.example.com/team/svc.git\n":     "team/svc",
		"/home/me/repos/aval":                        "", // a local path names no GitHub repository
		"../aval":                                    "",
		"https://github.com/":                        "",
		"https://github.com":                         "",
		"":                                           "",
	} {
		if got := repoFullName(remote); got != want {
			t.Errorf("repoFullName(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestOriginRepo(t *testing.T) {
	t.Parallel()
	r := newGitRepo(t)
	r.commit("chore: base", map[string]string{"a.txt": "a\n"})
	_, g, err := openRepo(t.Context(), r.dir)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	if got := originRepo(t.Context(), g); got != "" {
		t.Errorf("originRepo without a remote = %q, want it empty", got)
	}
	r.git("remote", "add", "origin", "git@github.com:svallejo-dev/aval.git")
	if got := originRepo(t.Context(), g); got != "svallejo-dev/aval" {
		t.Errorf("originRepo = %q, want svallejo-dev/aval", got)
	}
}

// assertExitCode checks that err carries code and mentions want.
func assertExitCode(t *testing.T, err error, code int, want string) {
	t.Helper()
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v (%T), want an *ExitError with code %d", err, err, code)
	}
	if ee.Code != code {
		t.Errorf("exit code = %d, want %d (error %v)", ee.Code, code, err)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

// mustEvalSymlinks resolves dir the way git does, so that a comparison with
// git's own answer holds on a macOS temporary directory, where /var is a link
// to /private/var.
func mustEvalSymlinks(t *testing.T, dir string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
