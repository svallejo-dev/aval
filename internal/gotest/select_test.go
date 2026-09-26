package gotest

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestRuns checks the grouping: one process per top-level test, in the order
// the top-level tests first appear, with the duplicates of a name folded into
// one alternative. It came here from internal/verify, which built the same
// runs of ADR-0005 §2.3 beside internal/overlay.
func TestRuns(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	got := Runs(f01, []string{"TestSuite/TestX/ORD-F01_one", "TestSuite/TestX/ORD-F01_one#01", "TestOther/ORD-F01"})
	want := []Selection{
		{Test: "TestSuite", Pattern: `^TestSuite$/^TestX$/^ORD-F01([_#]|$)`},
		{Test: "TestOther", Pattern: `^TestOther$/^ORD-F01([_#]|$)`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Runs = %+v, want %+v", got, want)
	}
	if got := Runs(f01, nil); got != nil {
		t.Errorf("Runs without names = %+v, want none", got)
	}
}

// TestRunsPattern checks each group's pattern, as internal/overlay checked it
// before the builder moved here: one alternative per name, repeats left out,
// every level escaped and anchored.
func TestRunsPattern(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"as RunPattern selects it", []string{"TestOrder/ORD-F01_rejects"}, RunPattern("TestOrder", f01)},
		{"duplicates once", []string{"TestOrder/ORD-F01_x", "TestOrder/ORD-F01_x#01", "TestOrder/ORD-F01"}, `^TestOrder$/^ORD-F01([_#]|$)`},
		{
			"deeper levels, suites and fake levels from a / in a name",
			[]string{"TestSuite/TestX/ORD-F01_x", "TestSuite/happy_path/ORD-F01_in/out"},
			`^TestSuite$/^TestX$/^ORD-F01([_#]|$)|^TestSuite$/^happy_path$/^ORD-F01([_#]|$)`,
		},
		{
			"bound by attr: the exact name, quoted",
			[]string{"TestOrder/dup_(sku)#01", "TestOrder/ORD-F02_carries_another_ID"},
			`^TestOrder$/^dup_\(sku\)#01$|^TestOrder$/^ORD-F02_carries_another_ID$`,
		},
		{
			// ORD-F010 is another obligation, not a longer spelling of
			// ORD-F01: nothing binds this test to the ID asked for, so it is
			// selected by its exact name.
			"an ID that starts with the one asked for",
			[]string{"TestOrder/ORD-F010_x"},
			`^TestOrder$/^ORD-F010_x$`,
		},
		{
			"levels of their own: unicode kept, metacharacters escaped",
			[]string{"TestPedido/año_(2,9×)/ORD-F01_x", "TestPedido/cobró_2,9×"},
			`^TestPedido$/^año_\(2,9×\)$/^ORD-F01([_#]|$)|^TestPedido$/^cobró_2,9×$`,
		},
	}
	for _, tt := range tests {
		got := Runs(f01, tt.names)
		if len(got) != 1 || got[0].Pattern != tt.want {
			t.Errorf("%s: Runs = %+v, want one selection with pattern %q", tt.name, got, tt.want)
		}
	}
}

// TestSelectorMatchesLevels reads the pattern the way go test does: each
// "/"-separated part matched against one level of a test name. A duplicate
// suffix has to run, an ID that another ID starts must not, and a level's
// regexp metacharacters have to stand for themselves.
func TestSelectorMatchesLevels(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	idLevels := Selector(f01, "TestSuite/año_(2,9×)/ORD-F01_x")
	for _, tt := range []struct {
		levels []string
		want   bool
	}{
		{[]string{"TestSuite", "año_(2,9×)", "ORD-F01_x"}, true},
		{[]string{"TestSuite", "año_(2,9×)", "ORD-F01"}, true},          // the bare ID
		{[]string{"TestSuite", "año_(2,9×)", "ORD-F01#01"}, true},       // a duplicate
		{[]string{"TestSuite", "año_(2,9×)", "ORD-F01_x#02"}, true},     // both
		{[]string{"TestSuite", "año_(2,9×)", "ORD-F010_x"}, false},      // another obligation
		{[]string{"TestSuite", "año_(2,9×)", "ORD-F01x"}, false},        // no separator
		{[]string{"TestSuite", "año_(2,9×)", "xORD-F01"}, false},        // not at the start
		{[]string{"TestSuite", "año_2,9×", "ORD-F01_x"}, false},         // the parentheses are literal
		{[]string{"TestSuite", "añoX(2,9×)", "ORD-F01_x"}, false},       // and so is the underscore
		{[]string{"TestSuiteNested", "año_(2,9×)", "ORD-F01_x"}, false}, // the top level is anchored
	} {
		if got := matchesLevels(t, idLevels, tt.levels...); got != tt.want {
			t.Errorf("%s selects %q = %v, want %v", idLevels, tt.levels, got, tt.want)
		}
	}

	// A test bound only by an aval.req attr is selected by its exact name, so
	// its metacharacters must not widen the selection either.
	attr := Selector(f01, "TestOrder/dup.sku")
	if want := `^TestOrder$/^dup\.sku$`; attr != want {
		t.Errorf("Selector of an attr-bound test = %q, want %q", attr, want)
	}
	for levels, want := range map[string]bool{"dup.sku": true, "dupXsku": false, "dup.sku_2": false} {
		if got := matchesLevels(t, attr, "TestOrder", levels); got != want {
			t.Errorf("%s selects %q = %v, want %v", attr, levels, got, want)
		}
	}
}

// matchesLevels reports whether pattern selects the test whose name levels
// are levels, as go test's -run does: one part of the pattern per level.
func matchesLevels(t *testing.T, pattern string, levels ...string) bool {
	t.Helper()
	parts := strings.Split(pattern, "/")
	if len(parts) != len(levels) {
		t.Fatalf("pattern %q has %d levels, want %d", pattern, len(parts), len(levels))
	}
	for i, p := range parts {
		re, err := regexp.Compile(p)
		if err != nil {
			t.Fatalf("compile %q: %v", p, err)
		}
		if !re.MatchString(levels[i]) {
			return false
		}
	}
	return true
}
