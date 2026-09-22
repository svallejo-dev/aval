package baseline

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const goldenPath = "testdata/baseline.golden.json"

func sample() Baseline {
	return Baseline{Version: Version, Failing: []Test{
		{Package: "example.com/orders/internal/order", Test: "TestTotals/legacy_rounding"},
		{Package: "example.com/orders/internal/api", Test: "TestHandlers"},
	}}
}

// TestGolden: the written form is sorted, so an unchanged baseline never diffs.
func TestGolden(t *testing.T) {
	t.Parallel()

	got, err := json.MarshalIndent(sample(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("baseline JSON changed; if intended, run go test -update and review the diff\ngot:\n%s", got)
	}
	parsed, err := Parse(bytes.NewReader(want))
	if err != nil {
		t.Fatalf("Parse(golden): %v", err)
	}
	if again, _ := json.MarshalIndent(parsed, "", "  "); !bytes.Equal(append(again, '\n'), want) {
		t.Errorf("golden does not round-trip:\n%s", again)
	}
}

func TestEmptyIsArray(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(Baseline{Version: Version})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"version":1,"failing":[]}` {
		t.Errorf("got %s, want an empty array, never null", raw)
	}
	if err := (Baseline{Version: Version}).Validate(); err != nil {
		t.Errorf("empty baseline: %v", err)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "valid", in: `{"version":1,"failing":[{"package":"p","test":"TestX"}]}`},
		{name: "empty list", in: `{"version":1,"failing":[]}`},
		{name: "example and fuzz tests", in: `{"version":1,"failing":[{"package":"p","test":"ExampleX"},{"package":"p","test":"FuzzY"}]}`},
		{name: "trailing whitespace", in: "{\"version\":1,\"failing\":[]}\n\n"},
		{name: "empty input", in: ``, wantErr: true},
		{name: "null", in: `null`, wantErr: true},
		{name: "other version", in: `{"version":2,"failing":[]}`, wantErr: true},
		{name: "float version", in: `{"version":1.0,"failing":[]}`, wantErr: true},
		{name: "missing failing", in: `{"version":1}`, wantErr: true},
		{name: "null failing", in: `{"version":1,"failing":null}`, wantErr: true},
		{name: "unknown field", in: `{"version":1,"failing":[],"trusted":true}`, wantErr: true},
		{name: "unknown test field", in: `{"version":1,"failing":[{"package":"p","test":"TestX","why":"flaky"}]}`, wantErr: true},
		{name: "empty package", in: `{"version":1,"failing":[{"package":"","test":"TestX"}]}`, wantErr: true},
		{name: "not a test name", in: `{"version":1,"failing":[{"package":"p","test":"helper"}]}`, wantErr: true},
		{name: "whitespace in name", in: `{"version":1,"failing":[{"package":"p","test":"TestX foo"}]}`, wantErr: true},
		{name: "duplicate test", in: `{"version":1,"failing":[{"package":"p","test":"TestX"},{"package":"p","test":"TestX"}]}`, wantErr: true},
		{name: "second document", in: `{"version":1,"failing":[]}{"version":1,"failing":[]}`, wantErr: true},
		{name: "trailing garbage", in: `{"version":1,"failing":[]} x`, wantErr: true},
		{name: "too large", in: `{"version":1,"failing":[]}` + strings.Repeat(" ", maxBytes), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(strings.NewReader(tt.in))
			switch {
			case tt.wantErr && !errors.Is(err, ErrInvalid):
				t.Fatalf("Parse = %v, want ErrInvalid", err)
			case !tt.wantErr && err != nil:
				t.Fatalf("Parse: unexpected error: %v", err)
			}
		})
	}
}

func TestCovers(t *testing.T) {
	t.Parallel()

	b := Baseline{Version: Version, Failing: []Test{{Package: "p", Test: "TestX"}, {Package: "p", Test: "TestY/legacy"}}}
	tests := []struct {
		pkg, test string
		want      bool
	}{
		{"p", "TestX", true},
		{"p", "TestX/any_subtest", true},      // a failing parent covers its subtests
		{"p", "TestX/a/b", true},              // at any depth
		{"p", "TestXY", false},                // a name prefix is not a parent
		{"p", "TestY", false},                 // a failing subtest does not cover its parent
		{"p", "TestY/legacy", true},           // exact subtest
		{"p", "TestY/legacy_rounding", false}, // prefix of a sibling
		{"q", "TestX", false},                 // another package
		{"p/sub", "TestX", false},             // a subpackage is another package
	}
	for _, tt := range tests {
		if got := b.Covers(tt.pkg, tt.test); got != tt.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", tt.pkg, tt.test, got, tt.want)
		}
	}
	if (Baseline{}).Covers("p", "TestX") {
		t.Error("the zero baseline covers nothing")
	}
}

// TestValidateMatchesParse: the Go value and the schema agree in both
// directions, so what aval writes is always what it reads back.
func TestValidateMatchesParse(t *testing.T) {
	t.Parallel()

	if err := sample().Validate(); err != nil {
		t.Errorf("sample: %v", err)
	}
	for name, b := range map[string]Baseline{
		"zero version": {Failing: []Test{{Package: "p", Test: "TestX"}}},
		"duplicate":    {Version: Version, Failing: []Test{{Package: "p", Test: "TestX"}, {Package: "p", Test: "TestX"}}},
		"bad name":     {Version: Version, Failing: []Test{{Package: "p", Test: "x"}}},
	} {
		if err := b.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
}
