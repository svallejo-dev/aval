package hook

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// step changes the repository at dir.
type step = func(t *testing.T, dir string)

func TestCheckVerified(t *testing.T) {
	t.Parallel()
	files := func(files map[string]string) step {
		return func(t *testing.T, dir string) { writeFiles(t, dir, files) }
	}
	edit := files(map[string]string{"a.go": "package a // edited\n"})
	untracked := files(map[string]string{"x_test.go": "package a\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { t.Fatal() }\n"})
	commit := func(t *testing.T, dir string) { gitT(t, dir, "commit", "-q", "-a", "--allow-empty", "-m", "next") }
	push := func(t *testing.T, dir string) { gitT(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD") }
	// gitlink points mod, a submodule that .gitmodules tells git diff to
	// ignore, at the commit whose ID repeats c.
	ignored := files(map[string]string{".gitmodules": "[submodule \"mod\"]\n\tpath = mod\n\turl = ./mod\n\tignore = all\n"})
	gitlink := func(c string) step {
		return func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "mod"), 0o750); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat(c, 40)+",mod")
		}
	}
	tests := []struct {
		name      string
		in        input
		steps     []step // run in order on a fresh repository
		wantBlock bool
	}{
		{name: "missing status, clean tree, pushed HEAD", steps: []step{push}},
		{name: "missing status, clean tree, unpushed HEAD", wantBlock: true},
		{name: "missing status, clean tree, HEAD behind the remote", steps: []step{commit, push, func(t *testing.T, dir string) {
			gitT(t, dir, "reset", "-q", "--hard", "HEAD~1")
		}}},
		{name: "missing status, edit committed but not pushed", steps: []step{push, edit, commit}, wantBlock: true},
		{name: "missing status, edited tree", steps: []step{edit}, wantBlock: true},
		{name: "missing status, untracked file", steps: []step{untracked}, wantBlock: true},
		{name: "passing status", steps: []step{edit, passed}},
		{name: "passing status, from a subdirectory", in: input{Cwd: "sub"}, steps: []step{edit, passed}},
		{name: "failing status", steps: []step{edit, failed}, wantBlock: true},
		{name: "failing status, clean tree, pushed HEAD", steps: []step{push, failed}, wantBlock: true},
		{name: "stale after an edit", steps: []step{passed, edit}, wantBlock: true},
		{name: "stale after an untracked file", steps: []step{passed, untracked}, wantBlock: true},
		{name: "stale after a commit", steps: []step{edit, passed, commit}, wantBlock: true},
		{name: "stale after a submodule change .gitmodules ignores", steps: []step{ignored, gitlink("1"), commit, passed, gitlink("2")}, wantBlock: true},
		{name: "fresh after staging", steps: []step{edit, passed, func(t *testing.T, dir string) { gitT(t, dir, "add", "a.go") }}},
		{name: "fresh after aval's own output", steps: []step{edit, passed, files(map[string]string{".aval/evidence/x.json": "{}"})}},
		{name: "stale after another untracked .aval file", steps: []step{edit, passed, files(map[string]string{".aval/baseline.json": "{}"})}, wantBlock: true},
		{name: "corrupt status", steps: []step{edit, files(map[string]string{StatusFile: "{"})}, wantBlock: true},
		{name: "status of another schema version", steps: []step{edit, files(map[string]string{StatusFile: `{"schemaVersion":2}`})}, wantBlock: true},
		{name: "stop hook already active", in: input{StopHookActive: true}, steps: []step{edit}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newRepo(t, map[string]string{"a.go": "package a\n", "sub/b.go": "package b\n"})
			for _, do := range tt.steps {
				do(t, dir)
			}
			in := tt.in
			in.Cwd = filepath.Join(dir, in.Cwd)
			got := checkVerified(context.Background(), in)
			if want := (&response{Decision: "block", Reason: verifyReason}); tt.wantBlock && (got == nil || *got != *want) {
				t.Errorf("checkVerified() = %+v, want %+v", got, want)
			} else if !tt.wantBlock && got != nil {
				t.Errorf("checkVerified() = %+v, want nil", got)
			}
		})
	}
}

// TestCurrentKeyIgnoresGitEnv sets what a git hook exports, as when one
// runs aval verify, so it cannot run in parallel.
func TestCurrentKeyIgnoresGitEnv(t *testing.T) {
	dir := newRepo(t, map[string]string{"a.go": "package a\n"})
	other := newRepo(t, map[string]string{"b.go": "package b\n"})
	root, key, err := CurrentKey(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	if gotRoot, got, err := CurrentKey(context.Background(), dir); err != nil || gotRoot != root || got != key {
		t.Errorf("CurrentKey under GIT_DIR = %s, %+v, %v; want %s, %+v", gotRoot, got, err, root, key)
	}
}

func passed(t *testing.T, dir string) { writeStatus(t, dir, true) }
func failed(t *testing.T, dir string) { writeStatus(t, dir, false) }

// writeStatus leaves the status that a verify of dir's working tree writes.
func writeStatus(t *testing.T, dir string, ok bool) {
	t.Helper()
	root, key, err := CurrentKey(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteStatus(root, Status{Key: key, Passed: ok, VerifiedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestStatusFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := ReadStatus(root); !errors.Is(err, ErrNoStatus) {
		t.Errorf("ReadStatus() of a missing file: error %v, want ErrNoStatus", err)
	}
	want := Status{
		Key:        Key{Head: "0123456789abcdef0123456789abcdef01234567", Diff: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		Passed:     true,
		VerifiedAt: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
	}
	for range 2 { // the second write replaces the file
		if err := WriteStatus(root, want); err != nil {
			t.Fatal(err)
		}
	}
	const golden = `{
  "schemaVersion": 1,
  "head": "0123456789abcdef0123456789abcdef01234567",
  "diff": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "passed": true,
  "verifiedAt": "2026-09-22T10:00:00Z"
}
`
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(StatusFile))) //nolint:gosec // a temporary directory
	if err != nil || string(data) != golden {
		t.Errorf("status file: %v\n%s\nwant:\n%s", err, data, golden)
	}
	want.SchemaVersion = StatusVersion
	if got, err := ReadStatus(root); err != nil || got != want {
		t.Errorf("ReadStatus() = %+v, %v; want %+v", got, err, want)
	}

	// Only a regular file is read: opening a FIFO would hang.
	for name, create := range map[string]func(string) error{
		"symlink": func(p string) error { return os.Symlink("/dev/zero", p) },
		"fifo":    func(p string) error { return exec.Command("mkfifo", p).Run() }, //nolint:gosec // a temporary path
	} {
		root := t.TempDir()
		p := filepath.Join(root, filepath.FromSlash(StatusFile))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := create(p); err != nil {
			t.Logf("no %s here: %v", name, err)
			continue
		}
		if _, err := ReadStatus(root); !errors.Is(err, ErrNoStatus) {
			t.Errorf("ReadStatus() of a %s: error %v, want ErrNoStatus", name, err)
		}
	}
}
