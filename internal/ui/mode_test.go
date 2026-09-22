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
	tests := []struct {
		name string
		in   Input
		env  map[string]string
		want Settings
	}{
		{name: "terminal", in: Input{IsTerminal: true}, want: tui},
		{name: "nil getenv is an empty environment", in: Input{IsTerminal: true, Getenv: nil}, want: tui},
		{name: "not a terminal", in: Input{}, want: plain},
		{name: "json on a terminal", in: Input{JSON: true, IsTerminal: true}, want: jsonMode},
		{name: "json without a terminal", in: Input{JSON: true}, want: jsonMode},
		{name: "json wins over plain", in: Input{JSON: true, Plain: true, IsTerminal: true}, want: jsonMode},
		{name: "json wins over CI", in: Input{JSON: true, IsTerminal: true}, env: map[string]string{"CI": "true"}, want: jsonMode},
		{name: "plain on a terminal", in: Input{Plain: true, IsTerminal: true}, want: plain},
		{name: "CI=true", in: Input{IsTerminal: true}, env: map[string]string{"CI": "true"}, want: plain},
		{name: "CI=1", in: Input{IsTerminal: true}, env: map[string]string{"CI": "1"}, want: plain},
		{name: "CI=false", in: Input{IsTerminal: true}, env: map[string]string{"CI": "false"}, want: tui},
		{name: "CI not a bool", in: Input{IsTerminal: true}, env: map[string]string{"CI": "woodpecker"}, want: tui},
		{name: "TERM=dumb", in: Input{IsTerminal: true}, env: map[string]string{"TERM": "dumb"}, want: plain},
		{name: "TERM=xterm", in: Input{IsTerminal: true}, env: map[string]string{"TERM": "xterm-256color"}, want: tui},
		{name: "NO_COLOR", in: Input{IsTerminal: true}, env: map[string]string{"NO_COLOR": "1"}, want: noColor},
		{name: "empty NO_COLOR is unset", in: Input{IsTerminal: true}, env: map[string]string{"NO_COLOR": ""}, want: tui},
		{name: "NO_COLOR in plain", in: Input{}, env: map[string]string{"NO_COLOR": "1"}, want: plain},
		{name: "no-animation", in: Input{NoAnimation: true, IsTerminal: true}, want: noMotion},
		{name: "AVAL_REDUCED_MOTION", in: Input{IsTerminal: true}, env: map[string]string{"AVAL_REDUCED_MOTION": "1"}, want: noMotion},
		{name: "yes", in: Input{Yes: true, IsTerminal: true}, want: noPrompts},
		{name: "never prompt without a terminal", in: Input{Yes: false}, want: plain},
		{name: "never prompt in CI", in: Input{IsTerminal: true}, env: map[string]string{"CI": "true"}, want: plain},
		{
			name: "everything off",
			in:   Input{NoAnimation: true, Yes: true, IsTerminal: true},
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

	if IsTerminal(&bytes.Buffer{}) {
		t.Error("IsTerminal(buffer) = true, want false")
	}
	if IsTerminal(f) {
		t.Error("IsTerminal(regular file) = true, want false")
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
