package envelope

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Envelope
		want []string // substrings of the output
	}{
		{
			name: "success stamps the schema and never writes null errors",
			in:   Envelope{SchemaVersion: 7, Command: "version", OK: true, Data: map[string]string{"go": "go1.27.1"}},
			want: []string{`"schemaVersion": 1`, `"command": "version"`, `"ok": true`, `"go": "go1.27.1"`, `"errors": []`},
		},
		{
			name: "failure keeps its issues and omits an empty hint",
			in:   Envelope{Command: "gate", Errors: []Issue{{Code: "failed", Message: "no evidence"}}},
			want: []string{`"ok": false`, `"data": null`, `"code": "failed"`, `"message": "no evidence"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := Write(&out, tt.in); err != nil {
				t.Fatalf("Write: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output = %s, want it to contain %s", out.String(), w)
				}
			}
			if strings.Contains(out.String(), "hint") {
				t.Errorf("output = %s, want no hint when it is empty", out.String())
			}
		})
	}
}

func TestWriteFailure(t *testing.T) {
	t.Parallel()

	errBroken := errors.New("broken pipe")
	err := Write(writerFunc(func([]byte) (int, error) { return 0, errBroken }), Envelope{Command: "version"})
	if !errors.Is(err, errBroken) {
		t.Errorf("Write to a broken writer = %v, want %v", err, errBroken)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
