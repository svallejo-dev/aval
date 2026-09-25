package codeowners

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/platform/gitenv"
)

// githubExample is the example file from GitHub's CODEOWNERS docs, trimmed to
// the lines whose comments state the behavior tested below.
const githubExample = `# This is a comment.
*       @global-owner1 @global-owner2
*.js    @js-owner #This is an inline comment.
*.go docs@example.com
*.txt @octo-org/octocats
/build/logs/ @doctocat
docs/* @docs-owner
apps/ @octocat
/scripts/ @doctocat @octocat
**/logs @logs-owner
/apps/github
`

func TestOwners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		file  string
		path  string
		want  []string
		empty bool // want no owners
	}{
		{name: "default owners", file: githubExample, path: "README.md", want: []string{"@global-owner1", "@global-owner2"}},
		{name: "last match wins", file: githubExample, path: "web/app.js", want: []string{"@js-owner"}},
		{name: "inline comment is not an owner", file: githubExample, path: "app.js", want: []string{"@js-owner"}},
		{name: "email only clears individual owners", file: githubExample, path: "cmd/main.go", empty: true},
		{name: "team only clears individual owners", file: githubExample, path: "notes.txt", empty: true},
		{name: "anchored directory owns its subtree", file: "/build/logs/ @doctocat\n", path: "build/logs/2026/run.txt", want: []string{"@doctocat"}},
		{name: "later double star beats the anchored directory", file: githubExample, path: "build/logs/run.txt", want: []string{"@logs-owner"}},
		{name: "anchored directory is not matched deeper", file: "/build/logs/ @doctocat\n", path: "x/build/logs/a", empty: true},
		{name: "docs/* owns direct files", file: githubExample, path: "docs/getting-started.md", want: []string{"@docs-owner"}},
		{name: "docs/* does not own nested files", file: githubExample, path: "docs/build-app/troubleshooting.md", want: []string{"@global-owner1", "@global-owner2"}},
		{name: "unanchored directory at any depth", file: githubExample, path: "src/apps/web/main.css", want: []string{"@octocat"}},
		{name: "directory pattern does not match a file", file: "apps/ @octocat\n", path: "src/apps", empty: true},
		{name: "double star directory", file: githubExample, path: "deeply/nested/logs/a.log", want: []string{"@logs-owner"}},
		{name: "entry without owners clears ownership", file: githubExample, path: "apps/github/x.rb", empty: true},
		{name: "several owners", file: githubExample, path: "scripts/deploy.sh", want: []string{"@doctocat", "@octocat"}},
		{name: "root policy", file: "* @all\n/aval.yaml @lead\n", path: "aval.yaml", want: []string{"@lead"}},
		{name: "anchored file is root only", file: "/aval.yaml @lead\n", path: "sub/aval.yaml", empty: true},
		{name: "unanchored file at any depth", file: "aval.yaml @lead\n", path: "sub/aval.yaml", want: []string{"@lead"}},
		{name: "middle slash anchors", file: "internal/*.go @go\n", path: "x/internal/a.go", empty: true},
		{name: "middle double star", file: "a/**/b @ab\n", path: "a/x/y/b/c", want: []string{"@ab"}},
		{name: "middle double star matches zero directories", file: "a/**/b @ab\n", path: "a/b", want: []string{"@ab"}},
		{name: "star does not cross a slash", file: "/docs*.md @s\n", path: "docs/x.md", empty: true},
		{name: "question mark does not cross a slash", file: "/a?b @q\n", path: "a/b", empty: true},
		{name: "double star directory does not own root files", file: "**/ @b\n", path: "aval.yaml", empty: true},
		{name: "double star directory owns nested files", file: "**/ @b\n", path: "sub/aval.yaml", want: []string{"@b"}},
		{name: "trailing double star", file: "a/** @a\n", path: "a/x/y", want: []string{"@a"}},
		{name: "question mark", file: "aval.y?ml @q\n", path: "aval.yaml", want: []string{"@q"}},
		{name: "case sensitive", file: "/AVAL.yaml @lead\n", path: "aval.yaml", empty: true},
		{name: "escaped space", file: `my\ file @s` + "\n", path: "my file", want: []string{"@s"}},
		{name: "hash inside a pattern", file: "c#/ @cs\n", path: "c#/a.cs", want: []string{"@cs"}},
		{name: "escaped hash inside a pattern", file: `docs/\#1.md @h` + "\n", path: "docs/#1.md", want: []string{"@h"}},
		{name: "crlf line endings", file: "* @a\r\n/aval.yaml @b\r\n", path: "aval.yaml", want: []string{"@b"}},
		{name: "leading slash in the path", file: "/aval.yaml @lead\n", path: "/aval.yaml", want: []string{"@lead"}},
		{name: "no match", file: "*.go @go\n", path: "aval.yaml", empty: true},
		// GitHub skips the lines below; the earlier line must still apply.
		{name: "escaped leading hash does not work", file: "* @a\n\\#aval.yaml @b\n", path: "#aval.yaml", want: []string{"@a"}},
		{name: "negation is skipped", file: "* @a\n!aval.yaml @b\n", path: "aval.yaml", want: []string{"@a"}},
		{name: "character range is skipped", file: "* @a\naval.[y]aml @b\n", path: "aval.yaml", want: []string{"@a"}},
		{name: "owner without @ is skipped", file: "* @a\naval.yaml lead\n", path: "aval.yaml", want: []string{"@a"}},
		{name: "empty segment is skipped", file: "* @a\na//aval.yaml @b\n", path: "a/aval.yaml", want: []string{"@a"}},
		{name: "triple star is skipped", file: "* @a\n*** @b\n", path: "aval.yaml", want: []string{"@a"}},
		{name: "double star inside a segment is skipped", file: "* @a\n**.yaml @b\n", path: "aval.yaml", want: []string{"@a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rules, err := Parse([]byte(tt.file))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := rules.Owners(tt.path)
			if tt.empty {
				if len(got) != 0 {
					t.Errorf("Owners(%q) = %v, want none", tt.path, got)
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Owners(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestOwnersReturnsACopy(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte("* @a\n"))
	if err != nil {
		t.Fatal(err)
	}
	rules.Owners("x")[0] = "@mallory"
	if got := rules.Owners("x"); !slices.Equal(got, []string{"@a"}) {
		t.Errorf("Owners after mutating a result = %v", got)
	}
}

func TestParseTooLarge(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(strings.Repeat("#", MaxSize)))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Parse(3 MB) = %v, want ErrTooLarge", err)
	}
	if _, err := Parse([]byte(strings.Repeat("#", MaxSize-1))); err != nil {
		t.Errorf("Parse(under 3 MB) = %v", err)
	}
}

func TestZeroRulesOwnNothing(t *testing.T) {
	t.Parallel()

	if got := (Rules{}).Owners("aval.yaml"); got != nil {
		t.Errorf("zero Rules.Owners = %v", got)
	}
}

// TestMatchesGitCheckIgnore compares which paths a pattern owns with which
// paths git ignores for the same pattern. Patterns ending in a bare * are
// left out: there GitHub departs from gitignore on purpose (docs/*).
func TestMatchesGitCheckIgnore(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	paths := []string{
		"aval.yaml", "sub/aval.yaml", "a/b", "a/x/b/c", "a/x/y", "ab", "axb", "apps/web/m.css", "src/apps/m.css",
		"docs/x.md", "docs/n/x.md", "build/logs/a", "x/build/logs/a", "logs/a", "c#/a.cs", "my file",
	}
	patterns := []string{
		"**", "**/", "*/", "aval.yaml", "/aval.yaml", "*.yaml", "sub/", "/sub/", "apps/", "/apps/", "a/**/b", "a/**/",
		"a/**", "**/b", "**/logs", "/build/logs/", "a?b", "a*b", "/a*", "docs/*.md", "docs/**/*.md", "c#/", `my\ file`,
	}
	dir := t.TempDir()
	for _, p := range paths {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(stdin string, args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // no shell: fixed arguments from the test
		cmd.Dir = dir
		cmd.Env = append(gitenv.Clean(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		return string(out), err
	}
	if _, err := git("", "init", "-q", "."); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, pattern := range patterns {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(pattern+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := git(strings.Join(paths, "\n"), "check-ignore", "--no-index", "--stdin")
		if exitErr, ok := errors.AsType[*exec.ExitError](err); err != nil && (!ok || exitErr.ExitCode() != 1) {
			t.Fatalf("git check-ignore %q: %v", pattern, err) // 1 means nothing ignored
		}
		ignored := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		rules, err := Parse([]byte(pattern + " @o\n"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			if owned, ign := rules.Owners(p) != nil, slices.Contains(ignored, p); owned != ign {
				t.Errorf("pattern %q, path %q: owned=%v, git ignores=%v", pattern, p, owned, ign)
			}
		}
	}
}
