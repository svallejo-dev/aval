package casing

import "testing"

func TestLoud(t *testing.T) {
	t.Run("ORD-F12 is loud", func(t *testing.T) {
		if got := Loud("a"); got != "a" {
			t.Errorf("Loud(a) = %q, want a", got)
		}
	})
}
