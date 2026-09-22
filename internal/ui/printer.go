package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
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
}

// Printer writes results and failures in the mode its settings resolved.
// It only writes static output; live views belong to Bubble Tea programs.
type Printer struct {
	stdout   io.Writer
	stderr   io.Writer
	theme    theme
	settings Settings
	width    int
}

// Option configures a Printer.
type Option func(*Printer)

// WithWidth wraps tui output at n columns. The default, 0, never wraps: static
// output does not know the terminal width.
func WithWidth(n int) Option {
	return func(p *Printer) { p.width = n }
}

// NewPrinter returns a printer that writes results to stdout and, outside
// json mode, failures to stderr.
func NewPrinter(s Settings, stdout, stderr io.Writer, opts ...Option) *Printer {
	p := &Printer{stdout: stdout, stderr: stderr, theme: newTheme(s.Color), settings: s}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Print writes r: its envelope in json mode, its lines otherwise.
func (p *Printer) Print(r Result) error {
	if p.settings.Mode == ModeJSON {
		return writeEnvelope(p.stdout, envelope{Command: r.Command, OK: true, Data: r.Data})
	}
	return p.writeLines(p.stdout, r.Lines)
}

// PrintError reports that command failed: with an error envelope on stdout in
// json mode, with "aval: <message>" on stderr otherwise. It is the last thing
// an invocation writes, so it has nowhere to report its own failure; if the
// envelope cannot be written it falls back to stderr.
func (p *Printer) PrintError(command string, is Issue) {
	if p.settings.Mode == ModeJSON {
		if writeEnvelope(p.stdout, envelope{Command: command, Errors: []Issue{is}}) == nil {
			return
		}
	}
	lines := []Line{{{Text: "aval:", Tone: ToneError}, {Text: " " + is.Message}}}
	if is.Hint != "" {
		lines = append(lines, Line{{Text: "hint: " + is.Hint, Tone: ToneMuted}})
	}
	_ = p.writeLines(p.stderr, lines)
}

// writeLines writes lines styled by the theme in tui mode and as bare text
// otherwise. Plain lines are never wrapped, so logs stay greppable.
func (p *Printer) writeLines(w io.Writer, lines []Line) error {
	tui := p.settings.Mode == ModeTUI
	var b, line strings.Builder
	for _, l := range lines {
		line.Reset()
		for _, s := range l {
			if tui {
				line.WriteString(p.theme.render(s))
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
