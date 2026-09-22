package ui

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/envelope"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// goldenWidth is the fixed terminal width of the tui golden files.
const goldenWidth = 40

var (
	sampleResult = Result{
		Command: "sample",
		Data: struct {
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		}{Passed: 2, Failed: 1},
		Lines: []Line{
			{{Text: "Verification", Tone: ToneTitle}},
			{{Text: "✓", Tone: ToneSuccess}, {Text: " unit tests passed"}},
			{{Text: "!", Tone: ToneWarning}, {Text: " coverage is below its target"}},
			{{Text: "✗", Tone: ToneError}, {Text: " the gate blocked the change: obligation OBL-7 has no executed evidence"}},
			{{Text: "3 verifiers in 1.2s", Tone: ToneMuted}},
		},
	}
	sampleIssue = envelope.Issue{Code: "tool", Message: "openspec 1.12.0 found, want 1.13.1", Hint: "install openspec 1.13.1"}
)

// TestPrinterGolden pins each mode's output. The tui cases keep Color false:
// Lip Gloss v2 styles render without reading the environment, so the theme
// alone decides the escape sequences, and a colorless theme makes the files
// deterministic on any machine. Run `go test ./internal/ui -update` after an
// intended change.
func TestPrinterGolden(t *testing.T) {
	t.Parallel()

	for _, mode := range []Mode{ModePlain, ModeTUI, ModeJSON} {
		t.Run("result_"+mode.String(), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			p := NewPrinter(Settings{Mode: mode}, &stdout, &stderr, WithWidth(goldenWidth))
			if err := p.Print(sampleResult); err != nil {
				t.Fatalf("Print: %v", err)
			}
			assertGolden(t, stdout.String())
			assertEmpty(t, "stderr", stderr.String())
		})
		t.Run("error_"+mode.String(), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			p := NewPrinter(Settings{Mode: mode}, &stdout, &stderr, WithWidth(goldenWidth))
			p.PrintError("sample", sampleIssue)
			got, other, otherName := stderr.String(), stdout.String(), "stdout"
			if mode == ModeJSON {
				got, other, otherName = stdout.String(), stderr.String(), "stderr"
			}
			assertGolden(t, got)
			assertEmpty(t, otherName, other)
		})
	}
}

func TestPrinterColor(t *testing.T) {
	t.Parallel()

	const green = "\x1b[32m"
	for _, color := range []bool{true, false} {
		var stdout bytes.Buffer
		p := NewPrinter(Settings{Mode: ModeTUI, Color: color}, &stdout, &bytes.Buffer{})
		if err := p.Print(Result{Lines: []Line{{{Text: "ok", Tone: ToneSuccess}}}}); err != nil {
			t.Fatalf("Print: %v", err)
		}
		if got := strings.Contains(stdout.String(), green); got != color {
			t.Errorf("Color %v: output %q contains green = %v, want %v", color, stdout.String(), got, color)
		}
	}
}

func TestPrinterWriteFailure(t *testing.T) {
	t.Parallel()

	var broken brokenWriter
	if err := NewPrinter(Settings{Mode: ModePlain}, broken, broken).Print(sampleResult); !errors.Is(err, errBroken) {
		t.Errorf("Print to a broken writer = %v, want %v", err, errBroken)
	}

	var stderr bytes.Buffer
	NewPrinter(Settings{Mode: ModeJSON}, broken, &stderr).PrintError("sample", sampleIssue)
	if want := "aval: " + sampleIssue.Message; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want the fallback %q when the envelope cannot be written", stderr.String(), want)
	}
}

var errBroken = errors.New("broken pipe")

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errBroken }

// assertGolden compares got with testdata/<test name>.golden, or rewrites the
// file with -update.
func assertGolden(t *testing.T, got string) {
	t.Helper()
	name := filepath.Base(t.Name()) + ".golden"
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (run with -update if intended)\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func assertEmpty(t *testing.T, stream, got string) {
	t.Helper()
	if got != "" {
		t.Errorf("%s = %q, want it empty", stream, got)
	}
}
