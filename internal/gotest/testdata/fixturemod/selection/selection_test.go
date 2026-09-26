// Package selection spreads one obligation's tests over two depths and two
// attr-bound siblings, so that the -run patterns gotest.Runs builds can be
// checked against a real go test: names of different depths need a process
// each, siblings of one depth share one.
package selection

import "testing"

// TestSpread binds ORD-F01 right under the Test, two levels down, and twice
// more through t.Attr on sibling subtests. ORD-F02 sits beside them, so a
// pattern that selects too much shows up.
func TestSpread(t *testing.T) {
	t.Run("ORD-F01 right under the Test", func(t *testing.T) {
		if Level() != 0 {
			t.Errorf("Level() = %d, want 0", Level())
		}
	})
	t.Run("nested", func(t *testing.T) {
		t.Run("ORD-F01 two levels down", func(t *testing.T) {
			if Level() != 0 {
				t.Errorf("Level() = %d, want 0", Level())
			}
		})
	})
	t.Run("first sibling", func(t *testing.T) {
		t.Attr("aval.req", "ORD-F01")
	})
	t.Run("second sibling", func(t *testing.T) {
		t.Attr("aval.req", "ORD-F01")
	})
	t.Run("ORD-F02 belongs to another obligation", func(t *testing.T) {
		if Level() != 0 {
			t.Errorf("Level() = %d, want 0", Level())
		}
	})
}
