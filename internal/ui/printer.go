package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/svallejo-dev/aval/internal/envelope"
)

// Tone is the role of a piece of text. The tui mode maps each tone to a style
// of its theme; the plain mode drops tones and keeps the text.
type Tone uint8

// Tones a command can give its text.
const (
	ToneNormal Tone = iota
	ToneTitle
	ToneSuccess
	ToneWarning
	ToneError
	ToneMuted
)

// Span is a run of text in a single tone.
type Span struct {
	Text string
	Tone Tone
}

// Line is one line of human-readable output.
type Line []Span

// Result is what a command hands the ui layer: the data agents read in json
// mode and the lines people read in tui and plain modes.
type Result struct {
	Command string // command name, e.g. "version"
	Data    any    // payload of the JSON envelope
	Lines   []Line // human-readable form
	// Issues are why the command failed although it has a result to show,
	// such as a trace with unverified obligations. In json mode they make the
	// envelope's ok false; otherwise they follow the lines, on stderr.
	Issues []envelope.Issue
}

// Printer writes results and failures in the mode its settings resolved.
// It only writes static output; live views belong to Bubble Tea programs.
type Printer struct {
	stdout   io.Writer
	stderr   io.Writer
	theme    theme
	errTheme theme // for the lines written to stderr
	settings Settings
	width    int
	errTUI   bool // style the lines written to stderr
}

// Option configures a Printer.
type Option func(*Printer)

// WithWidth wraps tui output at n columns. The default, 0, never wraps: static
// output does not know the terminal width.
func WithWidth(n int) Option {
	return func(p *Printer) { p.width = n }
}

// WithStderr styles the lines written to stderr, issues and errors, with s,
// the settings resolved against stderr, instead of with stdout's: each stream
// follows its own terminal, so `aval trace 2>err.log` at a terminal writes no
// escape sequences to the log (ADR-0003). The json envelope always follows
// the stdout settings.
func WithStderr(s Settings) Option {
	return func(p *Printer) { p.errTUI, p.errTheme = s.Mode == ModeTUI, newTheme(s.Color) }
}

// NewPrinter returns a printer that writes results to stdout and, outside
// json mode, failures to stderr.
func NewPrinter(s Settings, stdout, stderr io.Writer, opts ...Option) *Printer {
	th := newTheme(s.Color)
	p := &Printer{stdout: stdout, stderr: stderr, theme: th, errTheme: th, settings: s, errTUI: s.Mode == ModeTUI}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Print writes r: its envelope in json mode, its lines and then its issues
// otherwise.
func (p *Printer) Print(r Result) error {
	if p.settings.Mode == ModeJSON {
		e := envelope.Envelope{Command: r.Command, OK: len(r.Issues) == 0, Data: r.Data, Errors: r.Issues}
		if err := envelope.Write(p.stdout, e); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}
	if err := p.writeLines(p.stdout, r.Lines, p.settings.Mode == ModeTUI, p.theme); err != nil {
		return err
	}
	var lines []Line
	for _, is := range r.Issues {
		lines = append(lines, issueLines(is)...)
	}
	if len(lines) == 0 {
		return nil
	}
	return p.writeLines(p.stderr, lines, p.errTUI, p.errTheme)
}

// PrintError reports that command failed: with an error envelope on stdout in
// json mode, with "aval: <message>" on stderr otherwise. It is the last thing
// an invocation writes, so it has nowhere to report its own failure; if the
// envelope cannot be written it falls back to stderr.
func (p *Printer) PrintError(command string, is envelope.Issue) {
	if p.settings.Mode == ModeJSON {
		e := envelope.Envelope{Command: command, Errors: []envelope.Issue{is}}
		if envelope.Write(p.stdout, e) == nil {
			return
		}
	}
	_ = p.writeLines(p.stderr, issueLines(is), p.errTUI, p.errTheme)
}

// issueLines is how an issue reads outside json mode.
func issueLines(is envelope.Issue) []Line {
	lines := []Line{{{Text: "aval:", Tone: ToneError}, {Text: " " + is.Message}}}
	if is.Hint != "" {
		lines = append(lines, Line{{Text: "hint: " + is.Hint, Tone: ToneMuted}})
	}
	return lines
}

// writeLines writes lines styled by th when tui and as bare text otherwise.
// Plain lines are never wrapped, so logs stay greppable.
func (p *Printer) writeLines(w io.Writer, lines []Line, tui bool, th theme) error {
	var b, line strings.Builder
	for _, l := range lines {
		line.Reset()
		for _, s := range l {
			if tui {
				line.WriteString(th.render(s))
			} else {
				line.WriteString(s.Text)
			}
		}
		text := line.String()
		if tui && p.width > 0 {
			text = lipgloss.Wrap(text, p.width, "")
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// theme is the tui palette. It uses the terminal's 16 ANSI colors, so it
// follows the user's color scheme and never needs downsampling. Without color
// it keeps only bold and faint, as NO_COLOR asks.
type theme struct {
	title, success, warning, err, muted lipgloss.Style
}

func newTheme(color bool) theme {
	base := lipgloss.NewStyle()
	t := theme{
		title:   base.Bold(true),
		success: base,
		warning: base,
		err:     base.Bold(true),
		muted:   base.Faint(true),
	}
	if color {
		t.title = t.title.Foreground(lipgloss.Cyan)
		t.success = t.success.Foreground(lipgloss.Green)
		t.warning = t.warning.Foreground(lipgloss.Yellow)
		t.err = t.err.Foreground(lipgloss.Red)
	}
	return t
}

func (t theme) render(s Span) string {
	switch s.Tone {
	case ToneTitle:
		return t.title.Render(s.Text)
	case ToneSuccess:
		return t.success.Render(s.Text)
	case ToneWarning:
		return t.warning.Render(s.Text)
	case ToneError:
		return t.err.Render(s.Text)
	case ToneMuted:
		return t.muted.Render(s.Text)
	}
	return s.Text
}
