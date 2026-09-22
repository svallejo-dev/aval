package gotest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"go.uber.org/goleak"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

const fx = "example.com/fixturemod/"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS("testdata"), name+".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseFixture(t *testing.T, name string) Report {
	t.Helper()
	r, err := Parse(strings.NewReader(fixture(t, name)))
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return r
}

func id(t *testing.T, s string) obligation.ID {
	t.Helper()
	i, err := obligation.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// outcomes renders tests as "name=status", in report order.
func outcomes(r Report) []string {
	var out []string
	for _, t := range r.Tests {
		out = append(out, t.Name+"="+string(t.Status))
	}
	return out
}

// owners renders Report.Obligations as ID → owner names.
func owners(r Report) map[string][]string {
	m := map[string][]string{}
	for k, ts := range r.Obligations() {
		for _, t := range ts {
			m[k.String()] = append(m[k.String()], t.Name)
		}
	}
	return m
}

func TestParseFixtures(t *testing.T) {
	t.Parallel()
	const (
		pass, fail, skip, notRun = "=pass", "=fail", "=skipped", "=not_run"
		f01                      = "TestOrder/ORD-F01_rejects_duplicates"
	)
	suspicious := Warning{SuspiciousSegment, fx + "pass", "TestOrder/ORD-F04:_colon_after_the_ID", "ORD-F04:_colon_after_the_ID"}
	tests := []struct {
		fixture  string
		pkg      Package // everything but Output
		output   string  // exact package output
		tests    []string
		owners   map[string][]string
		status   map[string]evidence.Status // asked with the package; includes IDs no test carries
		warnings []Warning
	}{{
		fixture: "pass", pkg: Package{Name: fx + "pass", Status: evidence.Pass},
		output: "ok  \texample.com/fixturemod/pass\t0.000s\n",
		tests: []string{
			"TestOrder" + pass, f01 + pass, f01 + "#01" + pass, "TestOrder/ORD-F02_accepts_distinct_skus" + pass,
			"TestOrder/ORD-N01" + pass, "TestOrder/ORD-N01#01" + pass, "TestOrder/ORD-F03_keeps_in/out_order" + pass,
			"TestOrder/ORD-F04:_colon_after_the_ID" + pass, "TestOrderNested" + pass,
			"TestOrderNested/ORD-F05_lists_skus_in_insertion_order" + pass,
			"TestOrderNested/ORD-F05_lists_skus_in_insertion_order/empty_order" + pass,
			"TestOrderNested/ORD-F05_lists_skus_in_insertion_order/two_skus" + pass,
			"TestOrderNested/happy_path" + pass, "TestOrderNested/happy_path/ORD-F06_accepts_a_single_sku" + pass,
			"TestOrderAttr" + pass, "TestOrderAttr/duplicate_sku_is_rejected" + pass,
			"TestOrderSkip" + pass, "TestOrderSkip/ORD-F07_survives_a_restart" + skip,
		},
		owners: map[string][]string{
			"ORD-F01": {f01, f01 + "#01", "TestOrderAttr/duplicate_sku_is_rejected"},
			"ORD-F02": {"TestOrder/ORD-F02_accepts_distinct_skus", "TestOrderAttr/duplicate_sku_is_rejected"},
			"ORD-N01": {"TestOrder/ORD-N01", "TestOrder/ORD-N01#01"},
			"ORD-F03": {"TestOrder/ORD-F03_keeps_in/out_order"},
			"ORD-F05": {"TestOrderNested/ORD-F05_lists_skus_in_insertion_order"},
			"ORD-F06": {"TestOrderNested/happy_path/ORD-F06_accepts_a_single_sku"},
			"ORD-F07": {"TestOrderSkip/ORD-F07_survives_a_restart"},
		},
		status:   map[string]evidence.Status{"ORD-F01": evidence.Pass, "ORD-F07": evidence.Skipped, "ORD-F04": evidence.NotRun},
		warnings: []Warning{suspicious},
	}, {
		fixture: "fail", pkg: Package{Name: fx + "fail", Status: evidence.Fail},
		tests:  []string{"TestOrder" + fail, f01 + fail, "TestOrder/ORD-F02_accepts_distinct_skus" + pass, "TestNoID" + fail},
		owners: map[string][]string{"ORD-F01": {f01}, "ORD-F02": {"TestOrder/ORD-F02_accepts_distinct_skus"}},
		status: map[string]evidence.Status{"ORD-F01": evidence.Fail, "ORD-F02": evidence.Pass},
	}, {
		fixture: "rapid-pass", pkg: Package{Name: fx + "rapidpass", Status: evidence.Pass},
		output: "ok  \texample.com/fixturemod/rapidpass\t0.000s\n",
		tests:  []string{"TestCounter" + pass, "TestCounter/ORD-I01_counter_stays_within_bounds" + pass},
		owners: map[string][]string{"ORD-I01": {"TestCounter/ORD-I01_counter_stays_within_bounds"}},
		status: map[string]evidence.Status{"ORD-I01": evidence.Pass},
	}, {
		fixture: "rapid-fail", pkg: Package{Name: fx + "rapidfail", Status: evidence.Fail},
		tests:  []string{"TestCounter" + fail, "TestCounter/ORD-I01_counter_stays_within_bounds" + fail},
		owners: map[string][]string{"ORD-I01": {"TestCounter/ORD-I01_counter_stays_within_bounds"}},
		status: map[string]evidence.Status{"ORD-I01": evidence.Fail},
	}, {
		fixture: "leak", pkg: Package{Name: fx + "leak", Status: evidence.Fail, FailedOutsideTests: true},
		output: "goleak: Errors on successful test run: found unexpected goroutines:\n" +
			"[Goroutine N in state chan receive, with example.com/fixturemod/leak.Start.func1 on top of the stack:\n" +
			"example.com/fixturemod/leak.Start.func1()\n\t$FIXTUREMOD/leak/worker.go:9 +0x0\n" +
			"created by example.com/fixturemod/leak.Start in goroutine N\n\t$FIXTUREMOD/leak/worker.go:9 +0x0\n]\n",
		tests:  []string{"TestWorker" + pass, "TestWorker/ORD-O01_worker_stops_on_shutdown" + pass},
		owners: map[string][]string{"ORD-O01": {"TestWorker/ORD-O01_worker_stops_on_shutdown"}},
		status: map[string]evidence.Status{"ORD-O01": evidence.Pass},
	}, {
		fixture: "buildfail",
		pkg: Package{Name: fx + "buildfail", Status: evidence.BuildFail,
			FailedBuild: fx + "buildfail [example.com/fixturemod/buildfail.test]"},
		output: "# example.com/fixturemod/buildfail [example.com/fixturemod/buildfail.test]\n" +
			"buildfail/order_test.go:8:13: undefined: Subtotal\n",
		owners: map[string][]string{},
		status: map[string]evidence.Status{"ORD-F10": evidence.BuildFail},
	}, {
		fixture: "timeout", pkg: Package{Name: fx + "timeout", Status: evidence.Fail},
		tests:  []string{"TestQueue" + notRun, "TestQueue/ORD-F08_drains_the_queue" + notRun},
		owners: map[string][]string{"ORD-F08": {"TestQueue/ORD-F08_drains_the_queue"}},
		status: map[string]evidence.Status{"ORD-F08": evidence.NotRun},
	}, {
		fixture: "panic", pkg: Package{Name: fx + "panic", Status: evidence.Fail},
		tests: []string{"TestRefund" + fail, "TestRefund/ORD-F20_refunds_the_full_amount" + pass,
			"TestRefund/ORD-F21_rejects_a_negative_amount" + fail},
		owners: map[string][]string{
			"ORD-F20": {"TestRefund/ORD-F20_refunds_the_full_amount"},
			"ORD-F21": {"TestRefund/ORD-F21_rejects_a_negative_amount"},
		},
		status: map[string]evidence.Status{"ORD-F20": evidence.Pass, "ORD-F21": evidence.Fail,
			"ORD-F22": evidence.NotRun, "ORD-F23": evidence.NotRun},
	}, {
		fixture: "notests", pkg: Package{Name: fx + "notests", Status: evidence.Skipped, NoTestFiles: true},
		output: "?   \texample.com/fixturemod/notests\t[no test files]\n",
		owners: map[string][]string{},
		status: map[string]evidence.Status{"ORD-F01": evidence.NotRun},
	}, {
		fixture: "parallel", pkg: Package{Name: fx + "parallel", Status: evidence.Pass},
		output: "ok  \texample.com/fixturemod/parallel\t0.000s\n",
		tests:  []string{"TestDouble" + pass, "TestDouble/ORD-F30_doubles_one" + pass, "TestDouble/ORD-F31_doubles_zero" + pass},
		owners: map[string][]string{"ORD-F30": {"TestDouble/ORD-F30_doubles_one"}, "ORD-F31": {"TestDouble/ORD-F31_doubles_zero"}},
		status: map[string]evidence.Status{"ORD-F30": evidence.Pass, "ORD-F31": evidence.Pass},
	}, {
		fixture: "run-selection", pkg: Package{Name: fx + "pass", Status: evidence.Pass},
		output: "ok  \texample.com/fixturemod/pass\t0.000s\n",
		tests:  []string{"TestOrder" + pass, f01 + pass, f01 + "#01" + pass},
		owners: map[string][]string{"ORD-F01": {f01, f01 + "#01"}},
		status: map[string]evidence.Status{"ORD-F01": evidence.Pass, "ORD-F02": evidence.NotRun},
	}, {
		fixture: "run-selection-bare", pkg: Package{Name: fx + "pass", Status: evidence.Pass},
		output: "ok  \texample.com/fixturemod/pass\t0.000s\n",
		tests:  []string{"TestOrder" + pass, "TestOrder/ORD-N01" + pass},
		owners: map[string][]string{"ORD-N01": {"TestOrder/ORD-N01"}},
		status: map[string]evidence.Status{"ORD-N01": evidence.Pass},
	}, {
		fixture: "run-selection-miss", pkg: Package{Name: fx + "pass", Status: evidence.Pass, NoTestsToRun: true},
		output: "testing: warning: no tests to run\nok  \texample.com/fixturemod/pass\t0.000s [no tests to run]\n",
		tests:  []string{"TestOrder" + pass},
		owners: map[string][]string{},
		status: map[string]evidence.Status{"ORD-F99": evidence.NotRun},
	}}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			r := parseFixture(t, tt.fixture)
			if len(r.Packages) != 1 {
				t.Fatalf("Packages = %+v, want one", r.Packages)
			}
			got := r.Packages[0]
			if got.Output != tt.output || got.Truncated {
				t.Errorf("package output = %q (truncated %v), want %q", got.Output, got.Truncated, tt.output)
			}
			got.Output = ""
			if got != tt.pkg {
				t.Errorf("package = %+v, want %+v", got, tt.pkg)
			}
			if g := outcomes(r); !reflect.DeepEqual(g, tt.tests) {
				t.Errorf("tests:\n got %q\nwant %q", g, tt.tests)
			}
			if g := owners(r); !reflect.DeepEqual(g, tt.owners) {
				t.Errorf("Obligations() = %v, want %v", g, tt.owners)
			}
			for s, want := range tt.status {
				if g := r.Status(id(t, s), r.Packages[0].Name); g != want {
					t.Errorf("Status(%s) = %s, want %s", s, g, want)
				}
			}
			if !reflect.DeepEqual(r.Warnings, tt.warnings) {
				t.Errorf("Warnings = %+v, want %+v", r.Warnings, tt.warnings)
			}
			if r.Other != "" || r.ExitCode != 0 {
				t.Errorf("Other = %q, ExitCode = %d, want empty and 0", r.Other, r.ExitCode)
			}
		})
	}
}

// TestParseBindings pins every binding in pass.jsonl: inheritance by
// subtests, the fake level of "in/out", duplicates and attrs.
func TestParseBindings(t *testing.T) {
	t.Parallel()
	const f05 = "TestOrderNested/ORD-F05_lists_skus_in_insertion_order"
	const attr = "TestOrderAttr/duplicate_sku_is_rejected"
	want := map[string][]string{
		"TestOrder/ORD-F01_rejects_duplicates":    {"ORD-F01 by TestOrder/ORD-F01_rejects_duplicates"},
		"TestOrder/ORD-F01_rejects_duplicates#01": {"ORD-F01 by TestOrder/ORD-F01_rejects_duplicates#01"},
		"TestOrder/ORD-F02_accepts_distinct_skus": {"ORD-F02 by TestOrder/ORD-F02_accepts_distinct_skus"},
		"TestOrder/ORD-N01":                       {"ORD-N01 by TestOrder/ORD-N01"},
		"TestOrder/ORD-N01#01":                    {"ORD-N01 by TestOrder/ORD-N01#01"},
		"TestOrder/ORD-F03_keeps_in/out_order":    {"ORD-F03 by TestOrder/ORD-F03_keeps_in/out_order"},
		f05:                                       {"ORD-F05 by " + f05},
		f05 + "/empty_order":                      {"ORD-F05 by " + f05},
		f05 + "/two_skus":                         {"ORD-F05 by " + f05},
		"TestOrderNested/happy_path/ORD-F06_accepts_a_single_sku": {"ORD-F06 by TestOrderNested/happy_path/ORD-F06_accepts_a_single_sku"},
		attr: {"ORD-F01 by " + attr, "ORD-F02 by " + attr},
		"TestOrderSkip/ORD-F07_survives_a_restart": {"ORD-F07 by TestOrderSkip/ORD-F07_survives_a_restart"},
	}
	got := map[string][]string{}
	for _, tc := range parseFixture(t, "pass").Tests {
		for _, b := range tc.Bindings {
			got[tc.Name] = append(got[tc.Name], b.ID.String()+" by "+b.Owner)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bindings:\n got %q\nwant %q", got, want)
	}
}

func TestParseTestOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture, test, want string
		prefix              bool
	}{
		{"fail", "TestOrder/ORD-F01_rejects_duplicates",
			"    order_test.go:17: second Add(\"a\"):\n         got: <nil>\n        want: duplicate sku\n", false},
		{"fail", "TestNoID", "    order_test.go:34: Len() = 0, want 1\n", false},
		{"fail", "TestOrder", "", false},
		{"pass", "TestOrderSkip/ORD-F07_survives_a_restart", "    order_test.go:93: needs a database\n", false},
		{"pass", "TestOrderAttr/duplicate_sku_is_rejected", "", false},
		{"rapid-pass", "TestCounter/ORD-I01_counter_stays_within_bounds", "", false}, // dropped: it passed
		{"timeout", "TestQueue/ORD-F08_drains_the_queue",
			"panic: test timed out after 1s\n\trunning tests:\n\t\tTestQueue (0s)\n", true},
		{"panic", "TestRefund", "panic: refund: negative amount -1 [recovered, repanicked]\n", true},
		{"rapid-fail", "TestCounter/ORD-I01_counter_stays_within_bounds",
			"    counter_test.go:28: [rapid] failed after 1 tests: Value() = 4, want within [0, 3]\n" +
				"        To reproduce, specify -run=\"TestCounter/ORD-I01_counter_stays_within_bounds\" -rapid.seed=2\n", true},
	}
	for _, tt := range tests {
		found := false
		for _, tc := range parseFixture(t, tt.fixture).Tests {
			if tc.Name != tt.test {
				continue
			}
			found = true
			if ok := tc.Output == tt.want || tt.prefix && strings.HasPrefix(tc.Output, tt.want); !ok || tc.Truncated {
				t.Errorf("%s %s: Output = %q, want %q (prefix %v)", tt.fixture, tt.test, tc.Output, tt.want, tt.prefix)
			}
		}
		if !found {
			t.Errorf("%s: no test %s", tt.fixture, tt.test)
		}
	}
}

// stream writes events as a test2json stream.
func stream(evs ...event) string {
	var b strings.Builder
	for _, e := range evs {
		data, _ := json.Marshal(e)
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func mustParse(t *testing.T, s string) Report {
	t.Helper()
	r, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestStatusWorstOf checks the ranking in both orders, so neither "first
// wins" nor "last wins" passes.
func TestStatusWorstOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string // end action; "" never ends
		want evidence.Status
	}{
		{"pass", "pass", evidence.Pass},
		{"pass", "skip", evidence.Skipped}, {"skip", "pass", evidence.Skipped},
		{"skip", "", evidence.NotRun}, {"", "skip", evidence.NotRun},
		{"", "fail", evidence.Fail}, {"fail", "", evidence.Fail},
		{"fail", "pass", evidence.Fail}, {"pass", "fail", evidence.Fail},
	}
	if got := (Report{}).Status(id(t, "ORD-F01")); got != evidence.NotRun {
		t.Errorf("Report{}.Status = %s, want not_run", got)
	}
	for _, tt := range tests {
		var evs []event
		for i, action := range []string{tt.a, tt.b} {
			name := "TestX/ORD-F01" + []string{"", "#01"}[i]
			evs = append(evs, event{Action: "run", Package: "p", Test: name})
			if action != "" {
				evs = append(evs, event{Action: action, Package: "p", Test: name})
			}
		}
		r := mustParse(t, stream(append(evs, event{Action: "fail", Package: "p"})...))
		if got := r.Status(id(t, "ORD-F01")); got != tt.want {
			t.Errorf("Status(%q, %q) = %s, want %s", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseEdgeCases(t *testing.T) {
	t.Parallel()
	ev := func(action, pkg, test string) event { return event{Action: action, Package: pkg, Test: test} }
	long := "x" + strings.Repeat("é", MaxOutput) // MaxOutput falls mid-rune
	in := "go: downloading example.com/x v1.0.0\n\n" + stream(
		event{Action: "build-output", ImportPath: "example.com/dep", Output: "dep.go:1: syntax error\n"},
		event{Action: "build-output", ImportPath: "example.com/vet", Output: "vet: stray\n"},
		ev("start", "a", ""), ev("start", "b", ""),
		// -count=2: the second run of TestA never ends.
		ev("run", "a", "TestA"), ev("pass", "a", "TestA"), ev("run", "a", "TestA"),
		// -count=2 again: the failure and its output outlive the passing run.
		ev("run", "a", "TestC"), event{Action: "output", Package: "a", Test: "TestC", Output: "boom\n"},
		ev("fail", "a", "TestC"), ev("run", "a", "TestC"), ev("pass", "a", "TestC"),
		ev("run", "b", "TestB"),
		event{Action: "attr", Package: "b", Test: "TestB", Key: "aval.req", Value: "ORD-F02"},
		event{Action: "attr", Package: "b", Test: "TestB", Key: "aval.req", Value: "not-an-id"},
		event{Action: "attr", Package: "b", Test: "TestB", Key: "other", Value: "ORD-F09"},
		ev("run", "b", "TestB/ORD-F01_x"), ev("run", "b", "TestB/ORD-F01_x/ORD-F03_y"),
		event{Action: "output", Package: "b", Test: "TestB/ORD-F01_x/ORD-F03_y", Output: long},
		event{Action: "output", Package: "b", Test: "TestB/ORD-F01_x/ORD-F03_y", Output: "dropped\n"},
		ev("skip", "b", "TestB/ORD-F01_x/ORD-F03_y"), ev("pass", "b", "TestB/ORD-F01_x"), ev("pass", "b", "TestB"),
		event{Action: "fail", Package: "b", FailedBuild: "example.com/dep"},
		event{Action: "output", Output: "orphan\n"},
	) + "{not json\n" + `{"Foo":1}` + "\n"
	r := mustParse(t, in)

	wantPkgs := []Package{
		{Name: "a", Status: evidence.NotRun},
		{Name: "b", Status: evidence.BuildFail, FailedBuild: "example.com/dep", Output: "dep.go:1: syntax error\n"},
	}
	if !reflect.DeepEqual(r.Packages, wantPkgs) {
		t.Errorf("Packages = %+v, want %+v", r.Packages, wantPkgs)
	}
	if c := r.Tests[1]; c.Name != "TestC" || c.Status != evidence.Fail || c.Output != "boom\n" {
		t.Errorf("TestC = %s %s %q, want fail with its output", c.Name, c.Status, c.Output)
	}
	wantTests := []string{"TestA=not_run", "TestC=fail", "TestB=pass", "TestB/ORD-F01_x=pass", "TestB/ORD-F01_x/ORD-F03_y=skipped"}
	if g := outcomes(r); !reflect.DeepEqual(g, wantTests) {
		t.Errorf("tests = %q, want %q", g, wantTests)
	}
	wantOwners := map[string][]string{"ORD-F02": {"TestB"}, "ORD-F01": {"TestB/ORD-F01_x"}}
	if g := owners(r); !reflect.DeepEqual(g, wantOwners) {
		t.Errorf("Obligations() = %v, want %v", g, wantOwners)
	}
	// The attr is inherited, and the skipped subtest drags both IDs down
	// although go test passes its parents.
	leaf := r.Tests[4]
	wantLeaf := []Binding{{id(t, "ORD-F02"), "TestB"}, {id(t, "ORD-F01"), "TestB/ORD-F01_x"}}
	if !reflect.DeepEqual(leaf.Bindings, wantLeaf) {
		t.Errorf("leaf bindings = %+v, want %+v", leaf.Bindings, wantLeaf)
	}
	for _, s := range []string{"ORD-F01", "ORD-F02"} {
		if g := r.Status(id(t, s)); g != evidence.Skipped {
			t.Errorf("Status(%s) = %s, want skipped", s, g)
		}
	}
	// ORD-F09 is only an attr of another key: nothing binds it.
	if g := r.Status(id(t, "ORD-F09"), "a"); g != evidence.NotRun {
		t.Errorf("Status(ORD-F09, a) = %s, want not_run", g)
	}
	if g := r.Status(id(t, "ORD-F09"), "a", "b"); g != evidence.BuildFail {
		t.Errorf("Status(ORD-F09, a, b) = %s, want build_fail: b did not build", g)
	}
	if !leaf.Truncated || len(leaf.Output) != MaxOutput-1 || !utf8.ValidString(leaf.Output) {
		t.Errorf("leaf output: %d bytes, truncated %v, valid UTF-8 %v; want %d, true, true",
			len(leaf.Output), leaf.Truncated, utf8.ValidString(leaf.Output), MaxOutput-1)
	}
	wantWarnings := []Warning{
		{InvalidAttr, "b", "TestB", "not-an-id"},
		{NestedID, "b", "TestB/ORD-F01_x/ORD-F03_y", "ORD-F03_y"},
	}
	if !reflect.DeepEqual(r.Warnings, wantWarnings) {
		t.Errorf("Warnings = %+v, want %+v", r.Warnings, wantWarnings)
	}
	if want := "go: downloading example.com/x v1.0.0\norphan\n{not json\n{\"Foo\":1}\nvet: stray\n"; r.Other != want {
		t.Errorf("Other = %q, want %q", r.Other, want)
	}
}

// TestParseOverlay asks, as the gate does at the base of a change, about
// IDs whose tests live in packages that did or did not build.
func TestParseOverlay(t *testing.T) {
	t.Parallel()
	r := parseFixture(t, "overlay")
	a, b, c, d := fx+"overlay/a", fx+"overlay/b", fx+"overlay/c", fx+"overlay/d"
	var pkgs []string
	for _, p := range r.Packages {
		pkgs = append(pkgs, p.Name+"="+string(p.Status)+" "+p.FailedBuild)
	}
	wantPkgs := []string{
		d + "=build_fail " + fx + "overlay/mail",
		a + "=pass ",
		b + "=build_fail " + b + " [" + b + ".test]",
		c + "=skipped ",
	}
	if !reflect.DeepEqual(pkgs, wantPkgs) {
		t.Errorf("packages:\n got %q\nwant %q", pkgs, wantPkgs)
	}
	if r.Packages[2].Output != "# "+b+" ["+b+".test]\noverlay/b/refund_test.go:8:13: undefined: PartialRefund\n" ||
		!strings.Contains(r.Packages[0].Output, "cannot find module providing package "+fx+"overlay/mail") || r.Other != "" {
		t.Errorf("compiler output not with its package: b %q, d %q, other %q", r.Packages[2].Output, r.Packages[0].Output, r.Other)
	}
	tests := []struct {
		id   string
		pkgs []string
		want evidence.Status
	}{
		{"ORD-F40", []string{a}, evidence.Pass},
		{"ORD-F40", []string{b}, evidence.Pass}, // a bound test decides
		{"ORD-F41", []string{b}, evidence.BuildFail},
		{"ORD-F41", []string{a, b}, evidence.BuildFail},
		{"ORD-F41", nil, evidence.NotRun},
		{"ORD-F42", []string{c}, evidence.NotRun},
		{"ORD-F43", []string{d}, evidence.BuildFail},
		{"ORD-F99", []string{a}, evidence.NotRun}, // b and d failing elsewhere do not count
		{"ORD-F99", []string{c}, evidence.NotRun},
	}
	for _, tt := range tests {
		if got := r.Status(id(t, tt.id), tt.pkgs...); got != tt.want {
			t.Errorf("Status(%s, %q) = %s, want %s", tt.id, tt.pkgs, got, tt.want)
		}
	}
}

// TestParseSiblingBindings gives the parent enough bindings that appending
// to its slice in place would let one sibling overwrite the other's ID.
func TestParseSiblingBindings(t *testing.T) {
	t.Parallel()
	evs := []event{{Action: "run", Package: "p", Test: "TestP"}}
	for _, v := range []string{"ORD-F07", "ORD-F08", "ORD-F09"} {
		evs = append(evs, event{Action: "attr", Package: "p", Test: "TestP", Key: "aval.req", Value: v})
	}
	evs = append(evs, event{Action: "run", Package: "p", Test: "TestP/ORD-F01_a"},
		event{Action: "run", Package: "p", Test: "TestP/ORD-F02_b"})
	for _, tc := range mustParse(t, stream(evs...)).Tests[1:] {
		var got []string
		for _, b := range tc.Bindings {
			got = append(got, b.ID.String())
		}
		want := []string{"ORD-F07", "ORD-F08", "ORD-F09", tc.Name[len("TestP/"):len("TestP/ORD-F01")]}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s bindings = %q, want %q", tc.Name, got, want)
		}
	}
}

// TestParseOutputBudget checks that a report keeps at most MaxReportOutput
// bytes and that passing tests give theirs back.
func TestParseOutputBudget(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("x", MaxOutput)
	n := MaxReportOutput/MaxOutput + 1
	var evs []event
	for i := range 2 * n {
		name, end := fmt.Sprintf("TestPass%d", i), "pass"
		if i >= n {
			name, end = fmt.Sprintf("TestFail%d", i), "fail"
		}
		evs = append(evs, event{Action: "run", Package: "p", Test: name},
			event{Action: "output", Package: "p", Test: name, Output: full},
			event{Action: end, Package: "p", Test: name})
	}
	r := mustParse(t, stream(evs...))
	kept := 0
	for i, tc := range r.Tests {
		kept += len(tc.Output)
		switch {
		case i < n && (tc.Output != "" || tc.Truncated):
			t.Fatalf("%s passed but kept %d bytes", tc.Name, len(tc.Output))
		case i >= n && i < 2*n-1 && (tc.Output != full || tc.Truncated):
			t.Fatalf("%s kept %d bytes (truncated %v), want all %d", tc.Name, len(tc.Output), tc.Truncated, MaxOutput)
		case i == 2*n-1 && (tc.Output != "" || !tc.Truncated):
			t.Fatalf("%s kept %d bytes past the budget", tc.Name, len(tc.Output))
		}
	}
	if kept != MaxReportOutput {
		t.Errorf("report kept %d bytes, want %d", kept, MaxReportOutput)
	}
}

// TestParseNameWarnings covers segments that seem to name an ID but bind
// none. Each is reported once, not again for the subtest below it.
func TestParseNameWarnings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string // below TestX/
		kind WarningKind
		bad  string // the segment at fault
	}{
		{"ORD-F04:_x", SuspiciousSegment, "ORD-F04:_x"}, {"ORD-F04.1", SuspiciousSegment, "ORD-F04.1"},
		{"ORD-F01A_x", SuspiciousSegment, "ORD-F01A_x"}, {"ORD-F01#x", SuspiciousSegment, "ORD-F01#x"},
		{"ORD-F01_a/ORD-F02_b", NestedID, "ORD-F02_b"},
		{"ORD-F04_x", "", ""}, {"ORD-F04", "", ""}, {"ORD-F04#02", "", ""}, {"ORD-F01_a/ORD-F01_b", "", ""},
		{"ord-f04:_x", "", ""}, {"[ORD-F04]_x", "", ""}, {"ORD-X04:_x", "", ""}, {"happy_path", "", ""},
	}
	for _, tt := range tests {
		name := "TestX/" + tt.name
		r := mustParse(t, stream(event{Action: "run", Package: "p", Test: name},
			event{Action: "run", Package: "p", Test: name + "/child"}))
		var want []Warning
		if tt.kind != "" {
			want = []Warning{{tt.kind, "p", name, tt.bad}}
		}
		if !reflect.DeepEqual(r.Warnings, want) {
			t.Errorf("%s: Warnings = %+v, want %+v", tt.name, r.Warnings, want)
		}
	}
}

func TestParseReadErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	if _, err := Parse(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Errorf("failing reader: Parse error = %v, want it to wrap %v", err, boom)
	}
	if _, err := Parse(strings.NewReader(strings.Repeat("x", maxLine+1))); !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("long line: Parse error = %v, want it to wrap bufio.ErrTooLong", err)
	}
}
