package ui

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/svallejo-dev/aval/internal/envelope"
)

func TestTable(t *testing.T) {
	t.Parallel()
	lines := Table([]string{"ID", "STATUS", "TESTS"}, [][]Span{
		{{Text: "ORD-F01"}, {Text: "traced", Tone: ToneSuccess}, {Text: "a_test.go:1"}},
		{{Text: "ORD-F100"}, {Text: "✗ unverified", Tone: ToneError}, {Text: "-"}},
	})
	var out bytes.Buffer
	if err := NewPrinter(Settings{Mode: ModePlain}, &out, &out).Print(Result{Lines: lines}); err != nil {
		t.Fatal(err)
	}
	want := "ID        STATUS        TESTS\n" +
		"ORD-F01   traced        a_test.go:1\n" +
		"ORD-F100  ✗ unverified  -\n"
	if out.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", out.String(), want)
	}
}

// TestPrintIssues checks a result that fails: ok false with its data in json
// mode, its lines on stdout and its issues on stderr otherwise.
func TestPrintIssues(t *testing.T) {
	t.Parallel()
	r := Result{
		Command: "trace",
		Data:    map[string]int{"rows": 2},
		Lines:   []Line{{{Text: "table"}}},
		Issues:  []envelope.Issue{{Code: "failed", Message: "trace failed: 1 orphan test"}},
	}

	var stdout, stderr bytes.Buffer
	if err := NewPrinter(Settings{Mode: ModeJSON}, &stdout, &stderr).Print(r); err != nil {
		t.Fatal(err)
	}
	var e struct {
		OK     bool             `json:"ok"`
		Data   map[string]int   `json:"data"`
		Errors []envelope.Issue `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &e); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	if e.OK || e.Data["rows"] != 2 || len(e.Errors) != 1 || e.Errors[0] != r.Issues[0] || stderr.Len() != 0 {
		t.Errorf("json envelope = %+v, stderr %q; want ok false, the data and the issue", e, stderr.String())
	}

	stdout.Reset()
	if err := NewPrinter(Settings{Mode: ModePlain}, &stdout, &stderr).Print(r); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "table\n" || stderr.String() != "aval: trace failed: 1 orphan test\n" {
		t.Errorf("plain: stdout %q, stderr %q; want the lines, then the issue on stderr", stdout.String(), stderr.String())
	}
}
