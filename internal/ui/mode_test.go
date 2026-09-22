package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	var (
		tui       = Settings{Mode: ModeTUI, Color: true, Animation: true, Prompt: true}
		plain     = Settings{Mode: ModePlain}
		jsonMode  = Settings{Mode: ModeJSON}
		noColor   = Settings{Mode: ModeTUI, Animation: true, Prompt: true}
		noMotion  = Settings{Mode: ModeTUI, Color: true, Prompt: true}
		noPrompts = Settings{Mode: ModeTUI, Color: true, Animation: true}
	)
	// tty puts in on a terminal, both ways.
	tty := func(in Input) Input {
		in.OutputTerminal, in.StdinTerminal = true, true
		return in
	}
	tests := []struct {
		name string
		in   Input
		env  map[string]string
		want Settings
	}{
		{name: "terminal", in: tty(Input{}), want: tui},
		{name: "nil getenv is an empty environment", in: tty(Input{Getenv: nil}), want: tui},
		{name: "output not a terminal", in: Input{StdinTerminal: true}, want: plain},
		{name: "json on a terminal", in: tty(Input{JSON: true}), want: jsonMode},
		{name: "json without a terminal", in: Input{JSON: true}, want: jsonMode},
		{name: "json wins over plain", in: tty(Input{JSON: true, Plain: true}), want: jsonMode},
		{name: "json wins over CI", in: tty(Input{JSON: true}), env: map[string]string{"CI": "true"}, want: jsonMode},
		{name: "plain on a terminal", in: tty(Input{Plain: true}), want: plain},
		{name: "CI=true", in: tty(Input{}), env: map[string]string{"CI": "true"}, want: plain},
		{name: "CI=1", in: tty(Input{}), env: map[string]string{"CI": "1"}, want: plain},
		{name: "CI=false", in: tty(Input{}), env: map[string]string{"CI": "false"}, want: tui},
		{name: "CI not a bool", in: tty(Input{}), env: map[string]string{"CI": "woodpecker"}, want: tui},
		{name: "TERM=dumb", in: tty(Input{}), env: map[string]string{"TERM": "dumb"}, want: plain},
		{name: "TERM=xterm", in: tty(Input{}), env: map[string]string{"TERM": "xterm-256color"}, want: tui},
		{name: "NO_COLOR", in: tty(Input{}), env: map[string]string{"NO_COLOR": "1"}, want: noColor},
		{name: "empty NO_COLOR is unset", in: tty(Input{}), env: map[string]string{"NO_COLOR": ""}, want: tui},
		{name: "NO_COLOR in plain", in: Input{}, env: map[string]string{"NO_COLOR": "1"}, want: plain},
		{name: "no-animation", in: tty(Input{NoAnimation: true}), want: noMotion},
		{name: "AVAL_REDUCED_MOTION", in: tty(Input{}), env: map[string]string{"AVAL_REDUCED_MOTION": "1"}, want: noMotion},
		{name: "yes", in: tty(Input{Yes: true}), want: noPrompts},
		{name: "never prompt without a terminal on stdin", in: Input{OutputTerminal: true}, want: noPrompts},
		{name: "never prompt without a terminal on the output", in: Input{StdinTerminal: true}, want: plain},
		{name: "never prompt in CI", in: tty(Input{}), env: map[string]string{"CI": "true"}, want: plain},
		{
			name: "everything off",
			in:   tty(Input{NoAnimation: true, Yes: true}),
			env:  map[string]string{"NO_COLOR": "1", "AVAL_REDUCED_MOTION": "1"},
			want: Settings{Mode: ModeTUI},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := tt.in
			if tt.env != nil {
				in.Getenv = func(key string) string { return tt.env[key] }
			}
			if got := Resolve(in); got != tt.want {
				t.Errorf("Resolve(%+v) with env %v = %+v, want %+v", tt.in, tt.env, got, tt.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	t.Parallel()

	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	for name, stream := range map[string]any{"buffer": &bytes.Buffer{}, "regular file": f, os.DevNull: devNull} {
		if IsTerminal(stream) {
			t.Errorf("IsTerminal(%s) = true, want false", name)
		}
	}
}

func TestModeString(t *testing.T) {
	t.Parallel()

	for m, want := range map[Mode]string{ModeTUI: "tui", ModePlain: "plain", ModeJSON: "json", Mode(9): "Mode(9)"} {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", m, got, want)
		}
	}
}
