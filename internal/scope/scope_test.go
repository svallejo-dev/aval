package scope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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
		{"feat with seam and other", []string{"go.mod", "internal/order/order.go", "README.md"}, feat, nil},
		{"seam with other", []string{"go.mod", "README.md"}, seam, nil},
		{"longest glob wins for dx", []string{"internal/devtools/lint.go"}, dx, nil},
		{"longest glob wins for feat", []string{"internal/order/testdata/golden.json"}, feat, nil},
		{"shorter feat glob loses", []string{"internal/testdata/x.json"}, dx, nil},
		{"tie goes to feat", []string{"docs/api.md"}, feat, nil},
		{"no family", []string{"README.md", "LICENSE"}, other, nil},
		{"no files", nil, other, nil},
		{"spaces and unicode", []string{"internal/my dir/ñandú é.go"}, feat, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, families := ClassifyPaths(testPaths, tt.files)
			checkFamilies(t, evidence.Commit{Family: got, Families: families})
			if got != tt.want || !reflect.DeepEqual(families, tt.families) {
				t.Errorf("ClassifyPaths(%q) = %s %v, want %s %v", tt.files, got, families, tt.want, tt.families)
			}
		})
	}
}

func TestClassifyPathsDirectoryGlobs(t *testing.T) {
	t.Parallel()
	dirs := manifest.Paths{DX: []string{"tools", "docs/"}}
	if got, _ := ClassifyPaths(dirs, []string{"tools/gen.go", "docs/a.md"}); got != other {
		t.Errorf("directory globs: got %s, want other: they match nothing under the directory", got)
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
	for _, c := range got {
		checkFamilies(t, c)
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
	order := func(a, b, d string) map[string]string {
		return map[string]string{"internal/order/order.go": "package order\n\nfunc " + a + "() {}\n\nfunc " + b + "() {}\n\nfunc C() {}\n\nfunc " + d + "() {}\n"}
	}
	root := r.commit("root", map[string]string{"Makefile": "all:\n", "README.md": "shop\n"})
	r.commit("root order", order("A", "B", "D"))
	var want []evidence.Commit
	add := func(sha string, family evidence.Family, families []evidence.Family, paths ...string) {
		want = append(want, evidence.Commit{SHA: sha, Family: family, Families: families, Paths: append([]string{}, paths...)})
	}

	r.git("switch", "-q", "-c", "topic")
	add(r.commit("feat", order("A2", "B", "D")), feat, nil, "internal/order/order.go")
	r.onMain("main dx", map[string]string{"Makefile": "all: lint\n"})
	add(r.merge("clean merge", "main"), other, nil)
	r.onMain("main feat", order("A", "B", "D2"))
	add(r.merge("same file merged cleanly", "main"), other, nil)

	r.onMain("main docs", map[string]string{"README.md": "the shop\n"})
	r.git("merge", "-q", "--no-ff", "--no-commit", "main")
	add(r.commit("evil merge", map[string]string{"tools/gen.go": "package tools\n"}), dx, nil, "tools/gen.go")

	add(r.commit("topic B", order("A2", "B1", "D2")), feat, nil, "internal/order/order.go")
	r.onMain("main B", order("A", "B2", "D2"))
	if _, err := r.run("merge", "-q", "--no-ff", "main"); err == nil {
		t.Fatal("merge of main B: want a conflict")
	}
	add(r.commit("resolve conflict", order("A2", "B3", "D2")), feat, nil, "internal/order/order.go")

	r.onMain("main Makefile", map[string]string{"Makefile": "all: test\n"})
	r.git("merge", "-q", "--no-ff", "--no-commit", "main")
	r.git("checkout", "HEAD", "--", "Makefile")
	add(r.commit("drop main's Makefile", nil), dx, nil, "Makefile")

	r.onMain("main readme", map[string]string{"README.md": "our shop\n"})
	r.git("merge", "-q", "--no-ff", "--no-commit", "main")
	r.git("mv", "tools/gen.go", "internal/gen.go")
	add(r.commit("evil rename", nil), mixed, dxFeat, "internal/gen.go", "tools/gen.go")

	base := r.onMain("main license", map[string]string{"LICENSE": "MIT\n"})
	r.git("switch", "-q", "-c", "side", root)
	add(r.commit("side", map[string]string{"NOTES": "notes\n"}), other, nil, "NOTES")
	r.git("switch", "-q", "topic")
	r.git("merge", "-q", "--no-ff", "--no-commit", "main", "side")
	octopus := r.commit("evil octopus", map[string]string{"cmd/tool/main.go": "package main\n"})
	add(octopus, feat, nil, "cmd/tool/main.go")

	got, err := Classify(t.Context(), r.dir, base, "HEAD", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		checkFamilies(t, c)
	}
	// The side commit may come anywhere before the octopus merge.
	bySHA := func(cs []evidence.Commit) map[string]evidence.Commit {
		m := make(map[string]evidence.Commit, len(cs))
		for _, c := range cs {
			m[c.SHA] = c
		}
		return m
	}
	if !reflect.DeepEqual(bySHA(got), bySHA(want)) || got[len(got)-1].SHA != octopus {
		t.Errorf("Classify:\n got %+v\nwant %+v, the octopus merge last", got, want)
	}
}

func TestClassifyIgnoresHeadFiles(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	makefile := func(all, lint string) map[string]string {
		return map[string]string{"Makefile": "all:\n\t" + all + "\n\nlint:\n\t" + lint + "\n"}
	}
	root := r.commit("root", makefile("true", "true"))
	r.commit("head files", map[string]string{
		".gitattributes": "Makefile merge=binary\n",
		".gitmodules":    "[submodule \"sub\"]\n\tpath = tools/sub\n\turl = ./sub\n\tignore = all\n",
	})
	var want []evidence.Commit
	gitlink := func(msg string, files map[string]string) string {
		// git add -A keeps a gitlink whose directory exists, even empty.
		if err := os.MkdirAll(filepath.Join(r.dir, "tools", "sub"), 0o750); err != nil {
			t.Fatal(err)
		}
		r.git("update-index", "--add", "--cacheinfo", "160000,"+r.git("rev-parse", "HEAD")+",tools/sub")
		return r.commit(msg, files)
	}
	want = append(want,
		evidence.Commit{SHA: gitlink("feat and gitlink", map[string]string{"internal/x.go": "package x\n"}),
			Family: mixed, Families: dxFeat, Paths: []string{"internal/x.go", "tools/sub"}},
		evidence.Commit{SHA: gitlink("gitlink only", nil), Family: dx, Paths: []string{"tools/sub"}},
	)

	// merge=binary would make the remerge conflict and keep the branch's
	// Makefile, hiding that the merge dropped main's change to it.
	r.git("switch", "-q", "-c", "topic")
	r.commit("topic Makefile", makefile("false", "true"))
	base := r.onMain("main Makefile", makefile("true", "false"))
	want = append(want, evidence.Commit{SHA: base, Family: dx, Paths: []string{"Makefile"}})
	if _, err := r.run("merge", "-q", "--no-ff", "--no-commit", "main"); err == nil {
		t.Fatal("merge under merge=binary: want a conflict")
	}
	r.git("checkout", "HEAD", "--", "Makefile")
	merge := r.commit("drop main's Makefile", map[string]string{"internal/x.go": "package x // v2\n"})

	got, err := Classify(t.Context(), r.dir, root, "main", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[1:], want) {
		t.Errorf("gitlinks under ignore = all, then main:\n got %+v\nwant %+v", got[1:], want)
	}
	got, err = Classify(t.Context(), r.dir, base, "topic", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got); n != 2 || got[1].SHA != merge || got[1].Family != mixed {
		t.Errorf("merge under merge=binary: got %+v, want it mixed", got)
	}
}

func TestClassifyIgnoresPlantedHistory(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	root := r.commit("root", map[string]string{"Makefile": "all:\n", "internal/order/order.go": "package order\n"})
	featSHA := r.commit("feat", map[string]string{"internal/order/order.go": "package order // v2\n"})
	r.git("switch", "-q", "--detach", root)
	dxSHA := r.commit("dx", map[string]string{"Makefile": "all: lint\n"})
	r.git("switch", "-q", "main")
	r.git("replace", featSHA, dxSHA)      // read the dx commit instead of feat
	graft := featSHA + " " + dxSHA + "\n" // give feat the dx commit as parent
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "info", "grafts"), []byte(graft), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Classify(t.Context(), r.dir, root, "main", testPaths)
	if err != nil {
		t.Fatal(err)
	}
	want := []evidence.Commit{{SHA: featSHA, Family: feat, Paths: []string{"internal/order/order.go"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Classify = %+v, want %+v", got, want)
	}
}

func TestCheckVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		out string
		ok  bool
	}{
		{"git version 2.50.1 (Apple Git-155)\n", true},
		{"git version 2.40.0\n", true},
		{"git version 2.45.1.windows.1\n", true},
		{"git version 3.0.0\n", true},
		{"git version 2.39.5\n", false},
		{"git version 1.99.0\n", false},
		{"git version\n", false},
		{"", false},
	}
	for _, tt := range tests {
		err := checkVersion(tt.out)
		if (err == nil) != tt.ok || err != nil && !errors.Is(err, ErrToolMissing) {
			t.Errorf("checkVersion(%q) = %v, want ok %t", tt.out, err, tt.ok)
		}
	}
}

// TestClassifyWithoutGit changes PATH, so it cannot run in parallel.
func TestClassifyWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Classify(t.Context(), t.TempDir(), "", "HEAD", testPaths)
	if !errors.Is(err, ErrToolMissing) || !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Classify without git: error %v, want ErrToolMissing and exec.ErrNotFound", err)
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

// checkFamilies asserts the bundle contract (v2 schema: const ["dx","feat"]):
// Families is exactly [dx feat], in that order, for a mixed commit and nil
// for any other.
func checkFamilies(t *testing.T, c evidence.Commit) {
	t.Helper()
	if c.Family == mixed && !slices.Equal(c.Families, []evidence.Family{evidence.FamilyDX, evidence.FamilyFeat}) ||
		c.Family != mixed && c.Families != nil {
		t.Errorf("commit %s: %s with Families %#v, want [dx feat] exactly when mixed and nil otherwise", c.SHA, c.Family, c.Families)
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

// git runs git in r and returns its trimmed stdout, failing the test if git fails.
func (r *repo) git(args ...string) string {
	r.t.Helper()
	out, err := r.run(args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// run runs git in r and returns its trimmed stdout.
func (r *repo) run(args ...string) (string, error) {
	cmd := exec.CommandContext(r.t.Context(), "git", args...) //nolint:gosec // no shell: fixed arguments from the tests
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=aval", "GIT_AUTHOR_EMAIL=aval@example.com",
		"GIT_COMMITTER_NAME=aval", "GIT_COMMITTER_EMAIL=aval@example.com")
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, exitErr.Stderr)
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
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

// merge merges branch into the current branch with a merge commit and
// returns its SHA.
func (r *repo) merge(msg, branch string) string {
	r.t.Helper()
	r.git("merge", "-q", "--no-ff", "-m", msg, branch)
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
