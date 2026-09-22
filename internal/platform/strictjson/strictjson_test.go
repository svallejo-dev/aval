package strictjson

import (
	"strings"
	"testing"
)

func TestCheckDuplicateKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in string
		wantErr  string // "" means no error
	}{
		{name: "flat", in: `{"a":1,"b":2}`},
		{name: "same key in sibling objects", in: `[{"a":1},{"a":2}]`},
		{name: "same key at different depths", in: `{"a":{"a":{"a":1}}}`},
		{name: "empty containers", in: `{"a":{},"b":[],"c":[[]]}`},
		{name: "scalar", in: `1`},
		{name: "top-level duplicate", in: `{"version":2,"version":1}`, wantErr: `duplicate key "version"`},
		{name: "duplicate after a nested object", in: `{"a":{"x":1},"b":[1,{"y":2}],"a":3}`, wantErr: `duplicate key "a"`},
		{name: "nested duplicate", in: `{"list":[{"k":1,"k":2}]}`, wantErr: `duplicate key "k"`},
		{name: "escaped duplicate", in: `{"a":1,"a":2}`, wantErr: `duplicate key "a"`},
		{name: "malformed", in: `{"a":}`, wantErr: "syntax"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckDuplicateKeys([]byte(tt.in))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()

	type doc struct {
		A int `json:"a"`
	}
	tests := []struct {
		name, in string
		wantErr  bool
	}{
		{name: "valid", in: `{"a":1}`},
		{name: "trailing whitespace", in: "{\"a\":1}\n"},
		{name: "unknown field", in: `{"a":1,"b":2}`, wantErr: true},
		{name: "duplicate key", in: `{"a":1,"a":2}`, wantErr: true},
		{name: "second value", in: `{"a":1}{"a":2}`, wantErr: true},
		{name: "trailing garbage", in: `{"a":1} x`, wantErr: true},
		{name: "empty", in: ``, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var d doc
			if err := Decode([]byte(tt.in), &d); (err != nil) != tt.wantErr {
				t.Fatalf("Decode(%q) = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}
