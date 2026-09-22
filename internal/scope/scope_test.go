package scope

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/manifest"
)

func TestMain(m *testing.M) {
	// A git hook exports these; they would point the fixtures' git commands
	// at the hooked repository instead of their temporary one.
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"} {
		if err := os.Unsetenv(v); err != nil {
			panic(err)
		}
	}
	goleak.VerifyTestMain(m)
}

var testPaths = manifest.Paths{
	DX:   []string{"Makefile", "tools/**", "**/testdata/**", "internal/devtools/**", "docs/*.md"},
	Feat: []string{"internal/**", "cmd/**", "**/*.go", "internal/order/**", "docs/api*"},
	Seam: []string{"go.mod", "go.sum", "**/*.proto"},
}

const (
	dx    = evidence.FamilyDX
	feat  = evidence.FamilyFeat
	seam  = evidence.FamilySeam
	mixed = evidence.FamilyMixed
	other = evidence.FamilyOther
)

var dxFeat = []evidence.Family{dx, feat}

func TestClassifyPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		files    []string
		want     evidence.Family
		families []evidence.Family
	}{
		{"dx only", []string{"Makefile"}, dx, nil},
		{"feat only", []string{"cmd/aval/main.go"}, feat, nil},
		{"dx and feat", []string{"Makefile", "internal/order/order.go"}, mixed, dxFeat},
		{"seam alone", []string{"go.mod", "go.sum"}, seam, nil},
		{"seam wins over a longer feat glob", []string{"internal/api/v1.proto"}, seam, nil},
		{"dx with seam and other", []string{"Makefile", "go.mod", "README.md"}, dx, nil},
		{"feat with seam", []string{"go.sum", "internal/order/order.go"}, feat, nil},
		{"seam with other", []string{"go.mod", "README.md"}, seam, nil},
		{"longest glob wins for dx", []string{"internal/devtools/lint.go"}, dx, nil},
		{"longest glob wins for feat", []string{"internal/order/testdata/golden.json"}, feat, nil},
		{"shorter feat glob loses", []string{"internal/testdata/x.json"}, dx, nil},
		{"tie goes to dx", []string{"docs/api.md"}, dx, nil},
		{"no family", []string{"README.md", "LICENSE"}, other, nil},
		{"no files", nil, other, nil},
		{"spaces and unicode", []string{"internal/my dir/ñandú é.go"}, feat, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, families := ClassifyPaths(testPaths, tt.files)
			if got != tt.want || !reflect.DeepEqual(families, tt.families) {
				t.Errorf("ClassifyPaths(%q) = %s %v, want %s %v", tt.files, got, families, tt.want, tt.families)
			}
		})
	}
}

func TestTouchesSeam(t *testing.T) {
	t.Parallel()

	tests := []struct {
		paths []string
		want  bool
	}{
		{[]string{"go.mod", "internal/order/order.go"}, true},
		{[]string{"internal/api/v1.proto"}, true},
		{[]string{"internal/order/order.go", "README.md"}, false},
		{[]string{}, false},
	}
	for _, tt := range tests {
		if got := TouchesSeam(testPaths, evidence.Commit{Paths: tt.paths}); got != tt.want {
			t.Errorf("TouchesSeam(%q) = %t, want %t", tt.paths, got, tt.want)
		}
	}
}

func TestClassifyHistory(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	root := r.commit("root", map[string]string{
		"Makefile": "all:\n", "internal/order/order.go": "package order\n",
		"tools/gen.go": "package tools\n", "go.mod": "module example.com/shop\n", "README.md": "shop\n",
	})

	steps := []struct {
		name  string
		files map[string]string
		mv    []string // old and new path, moved with git mv
		want  evidence.Commit
		seam  bool
	}{
		{name: "dx", files: map[string]string{"Makefile": "all: lint\n"},
			want: evidence.Commit{Family: dx, Paths: []string{"Makefile"}}},
		{name: "feat", files: map[string]string{"internal/order/order.go": "package order // v2\n"},
			want: evidence.Commit{Family: feat, Paths: []string{"internal/order/order.go"}}},
		{name: "mixed", files: map[string]string{"Makefile": "all: test\n", "internal/order/order.go": "package order // v3\n"},
			want: evidence.Commit{Family: mixed, Families: dxFeat, Paths: []string{"Makefile", "internal/order/order.go"}}},
		{name: "seam", files: map[string]string{"go.mod": "module example.com/shop\n\ngo 1.27\n"},
			want: evidence.Commit{Family: seam, Paths: []string{"go.mod"}}, seam: true},
		{name: "feat and seam", files: map[string]string{"go.mod": "module example.com/shop\n", "internal/order/order.go": "package order\n"},
			want: evidence.Commit{Family: feat, Paths: []string{"go.mod", "internal/order/order.go"}}, seam: true},
		{name: "other", files: map[string]string{"README.md": "the shop\n"},
			want: evidence.Commit{Family: other, Paths: []string{"README.md"}}},
		{name: "rename across families", mv: []string{"tools/gen.go", "internal/gen.go"},
			want: evidence.Commit{Family: mixed, Families: dxFeat, Paths: []string{"internal/gen.go", "tools/gen.go"}}},
		{name: "spaces and unicode", files: map[string]string{"internal/my dir/ñandú é.go": "package mydir\n"},
			want: evidence.Commit{Family: feat, Paths: []string{"internal/my dir/ñandú é.go"}}},
		{name: "empty", want: evidence.Commit{Family: other, Paths: []string{}}},
	}
	want := make([]evidence.Commit, len(steps))
	for i, s := range steps {
		if s.mv != nil {
			r.git(append([]string{"mv"}, s.mv...)...)
		}
		want[i] = s.want
		want[i].SHA = r.commit(s.name, s.files)
	}

	got, err := Classify(t.Context(), r.dir, root, "HEAD", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Classify:\n got %+v\nwant %+v", got, want)
	}
	for i, s := range steps {
		if seamTouched := TouchesSeam(testPaths, got[i]); seamTouched != s.seam {
			t.Errorf("%s: TouchesSeam = %t, want %t", s.name, seamTouched, s.seam)
		}
	}

	all, err := Classify(t.Context(), r.dir, "", "HEAD", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := evidence.Commit{SHA: root, Family: mixed, Families: dxFeat,
		Paths: []string{"Makefile", "README.md", "go.mod", "internal/order/order.go", "tools/gen.go"}}
	if len(all) != len(want)+1 || !reflect.DeepEqual(all[0], wantRoot) || !reflect.DeepEqual(all[1:], want) {
		t.Errorf("Classify from the root:\n got %+v\nwant %+v then the range", all, wantRoot)
	}
}

func TestClassifyMerges(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	order := "package order\n\nfunc A() {}\n\nfunc B() {}\n\nfunc C() {}\n\nfunc D() {}\n"
	r.commit("root", map[string]string{"Makefile": "all:\n", "internal/order/order.go": order, "README.md": "shop\n"})

	r.git("switch", "-q", "-c", "topic")
	featSHA := r.commit("feat", map[string]string{"internal/order/order.go": strings.Replace(order, "A()", "A2()", 1)})
	r.onMain("main dx", map[string]string{"Makefile": "all: lint\n"})
	r.git("merge", "-q", "--no-ff", "--no-edit", "main")
	clean := r.git("rev-parse", "HEAD")

	r.onMain("main docs", map[string]string{"README.md": "the shop\n"})
	r.git("merge", "-q", "--no-ff", "--no-commit", "main")
	evil := r.commit("evil merge", map[string]string{"tools/gen.go": "package tools\n"})

	base := r.onMain("main feat", map[string]string{"internal/order/order.go": strings.Replace(order, "D()", "D2()", 1)})
	r.git("merge", "-q", "--no-ff", "--no-edit", "main")
	sameFile := r.git("rev-parse", "HEAD")

	got, err := Classify(t.Context(), r.dir, base, "HEAD", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	want := []evidence.Commit{
		{SHA: featSHA, Family: feat, Paths: []string{"internal/order/order.go"}},
		// Main changed other files: the merge adds nothing of its own.
		{SHA: clean, Family: other, Paths: []string{}},
		// Only what the merge changed itself: README.md comes from main as is.
		{SHA: evil, Family: dx, Paths: []string{"tools/gen.go"}},
		// Both sides changed order.go; git merged it cleanly, yet it differs
		// from both parents, so the combined diff lists it.
		{SHA: sameFile, Family: feat, Paths: []string{"internal/order/order.go"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Classify:\n got %+v\nwant %+v", got, want)
	}
}

func TestClassifyErrors(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	head := r.commit("root", map[string]string{"Makefile": "all:\n"})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name       string
		ctx        context.Context
		base, head string
		is         error
		msg        string
	}{
		{name: "empty head", ctx: t.Context(), base: head, is: ErrInvalidRange},
		{name: "option as base", ctx: t.Context(), base: "--output=x", head: head, is: ErrInvalidRange},
		{name: "option as head", ctx: t.Context(), head: "-n1", is: ErrInvalidRange},
		{name: "unknown revision", ctx: t.Context(), base: "nope", head: head, msg: "git rev-list"},
		{name: "canceled", ctx: canceled, head: head, is: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Classify(tt.ctx, r.dir, tt.base, tt.head, testPaths)
			if err == nil {
				t.Fatalf("Classify = %+v, want an error", got)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("Classify error = %v, want %v", err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("Classify error = %v, want it to mention %q", err, tt.msg)
			}
		})
	}
}

// repo is a throwaway git repository whose git commands ignore the host's
// configuration. Classify itself runs with the host's, as it would for real.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

// git runs git in r and returns its trimmed stdout.
func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", args...) //nolint:gosec // no shell: fixed arguments from the tests
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=aval", "GIT_AUTHOR_EMAIL=aval@example.com",
		"GIT_COMMITTER_NAME=aval", "GIT_COMMITTER_EMAIL=aval@example.com")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, exitErr.Stderr)
		}
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files, stages everything and commits it, even when nothing
// changed, and returns the new commit's SHA.
func (r *repo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// onMain commits files on main, returns to the previous branch and returns
// the commit's SHA.
func (r *repo) onMain(msg string, files map[string]string) string {
	r.t.Helper()
	r.git("switch", "-q", "main")
	sha := r.commit(msg, files)
	r.git("switch", "-q", "-")
	return sha
}
