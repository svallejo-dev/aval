package actions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWriteStepSummaryAppends: the gate may write more than once into the same
// step's file, so a write never replaces what is there, and always ends on a
// line boundary.
func TestWriteStepSummaryAppends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{name: "one write", writes: []string{"## aval\n"}, want: "## aval\n"},
		{name: "several writes", writes: []string{"## aval\n", "| ORD-F01 | strong |\n"}, want: "## aval\n| ORD-F01 | strong |\n"},
		{name: "a missing newline is added", writes: []string{"sin salto", "siguiente paso\n"}, want: "sin salto\nsiguiente paso\n"},
		{name: "empty content writes nothing", writes: []string{"", "## aval\n", ""}, want: "## aval\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "summary.md")
			for _, content := range tt.writes {
				if err := WriteStepSummary(path, content); err != nil {
					t.Fatalf("WriteStepSummary(%q): %v", content, err)
				}
			}
			got, err := os.ReadFile(path) //nolint:gosec // a file of the test's own temporary directory
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWriteStepSummaryEmptyContentCreatesNothing: nothing to say, nothing to
// create.
func TestWriteStepSummaryEmptyContentCreatesNothing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "summary.md")
	if err := WriteStepSummary(path, ""); err != nil {
		t.Fatalf("WriteStepSummary(empty): %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat = %v, want the file not to exist", err)
	}
}

// noticeRE matches the truncation notice at the end of a summary, whatever
// nonce it carries.
var noticeRE = regexp.MustCompile(`\n_Cut here \(([0-9a-f]{16})\): [^\n]+_\n$`)

// TestWriteStepSummaryCap: over GitHub's budget the file keeps whole lines and
// says where it was cut, and the caller is told, because what got cut may be
// the verdict.
func TestWriteStepSummaryCap(t *testing.T) {
	t.Parallel()

	const line = "| ORD-F01 | strong | el resumen del gate |\n"
	content := strings.Repeat(line, MaxStepSummaryBytes/len(line)+100)
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := WriteStepSummary(path, content); !errors.Is(err, ErrSummaryTruncated) {
		t.Fatalf("WriteStepSummary = %v, want ErrSummaryTruncated", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // a file of the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxStepSummaryBytes {
		t.Errorf("summary is %d bytes, over the %d budget", len(got), MaxStepSummaryBytes)
	}
	notice := noticeRE.FindIndex(got)
	if notice == nil {
		t.Fatalf("summary does not end with the notice: %q", tail(string(got)))
	}
	kept := string(got[:notice[0]])
	if !strings.HasPrefix(content, kept) {
		t.Error("what was kept is not a prefix of the content")
	}
	if !strings.HasSuffix(kept, "\n") || strings.Count(kept, line) != len(kept)/len(line) {
		t.Errorf("the summary was cut mid-line: %q", tail(kept))
	}
	// A second write has no room left for a whole line.
	if err := WriteStepSummary(path, content); !errors.Is(err, ErrSummaryFull) {
		t.Errorf("second write = %v, want ErrSummaryFull", err)
	}
}

// TestWriteStepSummaryNoticeIsUnforgeable: the content is written by aval, but
// out of the pull request's own test names and spec text, so it must not be
// able to print the notice itself — to fake a cut, or to make a real one look
// like part of the report.
func TestWriteStepSummaryNoticeIsUnforgeable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "summary.md")
	forged := "| ORD-F01 | strong |\n" + fmt.Sprintf(noticeFormat, strings.Repeat("0", 16))
	if err := WriteStepSummary(path, forged); err != nil {
		t.Fatalf("WriteStepSummary: %v", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // a file of the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	// Content is never rewritten, so the forged line is there; what it cannot
	// do is match a real notice, because each one carries a fresh nonce.
	if string(got) != forged {
		t.Errorf("content was rewritten: %q", got)
	}
	seen := map[string]bool{strings.Repeat("0", 16): true}
	for range 3 {
		notice, err := truncationNotice()
		if err != nil {
			t.Fatal(err)
		}
		m := noticeRE.FindStringSubmatch(notice)
		if m == nil {
			t.Fatalf("notice %q does not match the shape the summary is read with", notice)
		}
		if seen[m[1]] {
			t.Errorf("nonce %q repeated: the notice would be forgeable", m[1])
		}
		seen[m[1]] = true
	}
}

// TestWriteStepSummaryRefusesSymlink: the pull request's code runs in this job
// and can repoint GITHUB_STEP_SUMMARY at an environment file, turning the
// gate's own report into environment injection.
func TestWriteStepSummaryRefusesSymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "github_env")
	if err := os.WriteFile(target, []byte("AVAL_MODE=observe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "summary.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := WriteStepSummary(link, "AVAL_MODE=enforce\n")
	if !errors.Is(err, ErrNotRegularFile) {
		t.Errorf("WriteStepSummary(symlink) = %v, want ErrNotRegularFile", err)
	}
	got, rerr := os.ReadFile(target) //nolint:gosec // a file of the test's own temporary directory
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "AVAL_MODE=observe\n" {
		t.Errorf("the symlink's target was written: %q", got)
	}
}

// TestWriteStepSummaryFull: a file with no room for a whole line is
// ErrSummaryFull, and nothing already written is touched.
func TestWriteStepSummaryFull(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		already int    // bytes already in the file
		content string // what the gate tries to append
	}{
		{name: "no room at all", already: MaxStepSummaryBytes, content: "## aval\n"},
		{name: "not even room for the notice", already: MaxStepSummaryBytes - 10, content: "## aval: el veredicto del gate\n"},
		{name: "no whole line fits", already: MaxStepSummaryBytes - 200, content: strings.Repeat("x", 500) + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "summary.md")
			before := strings.Repeat("a", tt.already-1) + "\n"
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			err := WriteStepSummary(path, tt.content)
			if !errors.Is(err, ErrSummaryFull) {
				t.Errorf("WriteStepSummary = %v, want ErrSummaryFull", err)
			}
			got, err := os.ReadFile(path) //nolint:gosec // a file of the test's own temporary directory
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != before {
				t.Errorf("the file changed: %d bytes, want the %d it had", len(got), len(before))
			}
		})
	}
}

// TestWriteStepSummaryNoPath: with no GITHUB_STEP_SUMMARY there is nothing to
// write to, and the caller can tell that apart from a broken file.
func TestWriteStepSummaryNoPath(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		WriteStepSummary("", "## aval\n"),
		Context{}.WriteStepSummary("## aval\n"),
		Detect(nil).WriteStepSummary("## aval\n"),
	} {
		if !errors.Is(err, ErrNoSummaryPath) {
			t.Errorf("err = %v, want ErrNoSummaryPath", err)
		}
	}
}

// TestWriteStepSummaryUnwritable: a path that cannot be written is an error
// that names the path.
func TestWriteStepSummaryUnwritable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	readOnly := filepath.Join(dir, "read-only.md")
	if err := os.WriteFile(readOnly, []byte("## aval\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		skip bool
	}{
		{name: "the parent does not exist", path: filepath.Join(dir, "no-such-dir", "summary.md")},
		{name: "the path is a directory", path: dir},
		{name: "the file is read-only", path: readOnly, skip: os.Geteuid() == 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.skip {
				t.Skip("root ignores file permissions")
			}
			err := WriteStepSummary(tt.path, "## aval\n")
			if err == nil {
				t.Fatalf("WriteStepSummary(%q) = nil, want an error", tt.path)
			}
			if !strings.Contains(err.Error(), tt.path) {
				t.Errorf("err = %v, want it to name %q", err, tt.path)
			}
			if errors.Is(err, ErrNoSummaryPath) || errors.Is(err, ErrSummaryFull) {
				t.Errorf("err = %v, want an I/O error, not one of the sentinels", err)
			}
		})
	}
}

// TestContextWriteStepSummary: the context writes where the environment said.
func TestContextWriteStepSummary(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "summary.md")
	env := runEnv(t, "pull_request_opened.json")
	env[EnvStepSummary] = path
	c := Detect(lookup(env))
	if err := c.WriteStepSummary("## aval\n"); err != nil {
		t.Fatalf("WriteStepSummary: %v", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // a file of the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "## aval\n" {
		t.Errorf("summary = %q", got)
	}
}

// tail is the last 120 bytes of s, for a readable failure.
func tail(s string) string {
	if len(s) <= 120 {
		return s
	}
	return "…" + s[len(s)-120:]
}
