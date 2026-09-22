// Package ui decides how aval talks to whoever runs it (a person at a
// terminal, a CI log or an agent) and writes command output in that mode, so
// commands never know how their results are shown.
package ui

import (
	"io"
	"os"
	"strconv"
)

// Mode is how aval writes its output.
type Mode uint8

const (
	// ModeTUI styles output for a person at a terminal.
	ModeTUI Mode = iota
	// ModePlain writes unstyled text for CI logs, pipes and dumb terminals.
	ModePlain
	// ModeJSON writes the JSON envelope for agents and hooks.
	ModeJSON
)

func (m Mode) String() string {
	switch m {
	case ModeTUI:
		return "tui"
	case ModePlain:
		return "plain"
	case ModeJSON:
		return "json"
	}
	return "Mode(" + strconv.Itoa(int(m)) + ")"
}

// Input is everything output resolution depends on. Resolve reads nothing
// else, so it can be tested without a terminal or a real environment.
type Input struct {
	// Getenv looks up an environment variable; os.Getenv in production.
	// Nil means an empty environment.
	Getenv      func(key string) string
	JSON        bool // --json
	Plain       bool // --plain
	Yes         bool // --yes
	NoAnimation bool // --no-animation
	IsTerminal  bool // the output stream is a terminal
}

// Settings is the resolved output behaviour of one invocation.
type Settings struct {
	Mode      Mode
	Color     bool // styles may use color
	Animation bool // spinners and transitions may move
	Prompt    bool // aval may ask questions and wait for an answer
}

// Resolve decides the output mode and what it allows.
//
// The mode is json with --json; otherwise plain with --plain, when the output
// is not a terminal, when CI is true or when TERM=dumb; otherwise tui. Color,
// animation and prompting exist only in tui: NO_COLOR turns color off,
// --no-animation and AVAL_REDUCED_MOTION turn animation off, and --yes turns
// prompting off. Like NO_COLOR (https://no-color.org), a variable counts as
// set when it is present and not empty.
func Resolve(in Input) Settings {
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	ci, _ := strconv.ParseBool(getenv("CI"))

	s := Settings{Mode: ModeTUI}
	switch {
	case in.JSON:
		s.Mode = ModeJSON
	case in.Plain, !in.IsTerminal, ci, getenv("TERM") == "dumb":
		s.Mode = ModePlain
	}
	tui := s.Mode == ModeTUI
	s.Color = tui && getenv("NO_COLOR") == ""
	s.Animation = tui && !in.NoAnimation && getenv("AVAL_REDUCED_MOTION") == ""
	s.Prompt = tui && !in.Yes
	return s
}

// IsTerminal reports whether w is a terminal (a character device), such as
// os.Stdout when nothing redirects it. Buffers, pipes and files are not.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
