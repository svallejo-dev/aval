package gotest

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// TestRuns checks the grouping and each group's pattern: one process per
// (top-level test, ID), in the order the top-level tests first appear, the
// repeats of a name folded into one alternative, and every level escaped and
// anchored. It came here from internal/verify and internal/overlay, which
// built the same runs of ADR-0005 §2.3 twice.
func TestRuns(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	tests := []struct {
		name  string
		names []string
		want  []Selection
	}{
		{
			"as RunPattern selects it",
			[]string{"TestOrder/ORD-F01_rejects"},
			[]Selection{{Test: "TestOrder", Pattern: RunPattern("TestOrder", f01)}},
		},
		{
			"duplicates once",
			[]string{"TestOrder/ORD-F01_x", "TestOrder/ORD-F01_x#01", "TestOrder/ORD-F01"},
			[]Selection{{Test: "TestOrder", Pattern: `^TestOrder$/^ORD-F01([_#]|$)`}},
		},
		{
			"one process per top-level test, in order of first appearance",
			[]string{"TestSuite/TestX/ORD-F01_one", "TestSuite/TestX/ORD-F01_one#01", "TestOther/ORD-F01"},
			[]Selection{
				{Test: "TestSuite", Pattern: `^TestSuite$/^TestX$/^ORD-F01([_#]|$)`},
				{Test: "TestOther", Pattern: `^TestOther$/^ORD-F01([_#]|$)`},
			},
		},
		{
			// Two depths in one process: each alternative is a whole path, so
			// go test splits and matches them independently. Factoring
			// "^TestX$" out would leave the second alternative one level deep
			// and select nothing.
			"two depths under one Test, every alternative whole",
			[]string{"TestX/ORD-F01", "TestX/Sub/ORD-F01"},
			[]Selection{{Test: "TestX", Pattern: `^TestX$/^ORD-F01([_#]|$)|^TestX$/^Sub$/^ORD-F01([_#]|$)`}},
		},
		{
			"deeper levels, suites and fake levels from a / in a name",
			[]string{"TestSuite/TestX/ORD-F01_x", "TestSuite/happy_path/ORD-F01_in/out"},
			[]Selection{{
				Test:    "TestSuite",
				Pattern: `^TestSuite$/^TestX$/^ORD-F01([_#]|$)|^TestSuite$/^happy_path$/^ORD-F01([_#]|$)`,
			}},
		},
		{
			"bound by attr: the exact name, quoted",
			[]string{"TestOrder/dup_(sku)#01", "TestOrder/ORD-F02_carries_another_ID"},
			[]Selection{{
				Test:    "TestOrder",
				Pattern: `^TestOrder$/^dup_\(sku\)#01$|^TestOrder$/^ORD-F02_carries_another_ID$`,
			}},
		},
		{
			// ORD-F010 is another obligation, not a longer spelling of
			// ORD-F01: nothing binds this test to the ID asked for, so it is
			// selected by its exact name.
			"an ID that starts with the one asked for",
			[]string{"TestOrder/ORD-F010_x"},
			[]Selection{{Test: "TestOrder", Pattern: `^TestOrder$/^ORD-F010_x$`}},
		},
		{
			"levels of their own: unicode kept, metacharacters escaped",
			[]string{"TestPedido/año_(2,9×)/ORD-F01_x", "TestPedido/cobró_2,9×"},
			[]Selection{{
				Test:    "TestPedido",
				Pattern: `^TestPedido$/^año_\(2,9×\)$/^ORD-F01([_#]|$)|^TestPedido$/^cobró_2,9×$`,
			}},
		},
		{"no names, no runs", nil, nil},
	}
	for _, tt := range tests {
		if got := Runs(f01, tt.names); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Runs = %+v, want %+v", tt.name, got, tt.want)
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

// TestRunsSelectFixtureTests hands the patterns to a real go test: every name
// Runs grouped has to run, and nothing that belongs to another obligation.
// The fixture spreads ORD-F01 over two depths and two attr-bound siblings,
// which is what makes the alternatives worth checking — a name a pattern does
// not select is an obligation with no measured status. It also pins the rule
// that shapes them: go test alternates whole paths, so the shared prefix has
// to be repeated in each one.
func TestRunsSelectFixtureTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	t.Parallel()
	f01 := id(t, "ORD-F01")
	pkg := []string{"./selection"}

	full, err := Run(t.Context(), fixtureModule, Options{Packages: pkg, Count: 1, Env: fixtureEnv})
	if err != nil {
		t.Fatal(err)
	}
	names := ownerNames(full, f01)
	wantNames := []string{
		"TestSpread/ORD-F01_right_under_the_Test",
		"TestSpread/nested/ORD-F01_two_levels_down",
		"TestSpread/first_sibling",
		"TestSpread/second_sibling",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("the fixture's owners of ORD-F01 = %q, want %q", names, wantNames)
	}

	runs := Runs(f01, names)
	want := []Selection{{Test: "TestSpread", Pattern: `^TestSpread$/^ORD-F01([_#]|$)` +
		`|^TestSpread$/^nested$/^ORD-F01([_#]|$)|^TestSpread$/^first_sibling$|^TestSpread$/^second_sibling$`}}
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("Runs = %+v, want %+v", runs, want)
	}

	var ran []string
	for _, r := range runs {
		rep, err := Run(t.Context(), fixtureModule, Options{Packages: pkg, Run: r.Pattern, Count: 1, Env: fixtureEnv})
		if err != nil {
			t.Fatal(err)
		}
		if s := rep.Status(f01); s != evidence.Pass || rep.ExitCode != 0 {
			t.Errorf("%s: Status(ORD-F01) = %s, exit %d; want pass, 0", r.Pattern, s, rep.ExitCode)
		}
		for _, to := range rep.Tests {
			if strings.Contains(to.Name, "ORD-F02") {
				t.Errorf("%s ran %s, which belongs to another obligation", r.Pattern, to.Name)
			}
		}
		ran = append(ran, ownerNames(rep, f01)...)
	}
	slices.Sort(ran)
	if want := slices.Sorted(slices.Values(wantNames)); !reflect.DeepEqual(ran, want) {
		t.Errorf("the run selected %q, want every name of the obligation: %q", ran, want)
	}

	// The trap the whole-path alternatives avoid, as go test really behaves:
	// factoring the shared prefix out leaves the second alternative one level
	// deep, and testing.splitRegexp matches it against the top-level test's own
	// name, so second_sibling is never selected. If a future toolchain makes
	// this pattern work, this is where to find out.
	factored, err := Run(t.Context(), fixtureModule, Options{
		Packages: pkg, Run: `^TestSpread$/^first_sibling$|^second_sibling$`, Count: 1, Env: fixtureEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerNames(factored, f01); !reflect.DeepEqual(got, []string{"TestSpread/first_sibling"}) {
		t.Errorf("a factored prefix selected %q; alternatives may only be whole paths", got)
	}
}

// ownerNames returns the names of the tests that own id, in report order.
func ownerNames(r Report, id obligation.ID) []string {
	var names []string
	for _, o := range r.Obligations()[id] {
		names = append(names, o.Name)
	}
	return names
}
