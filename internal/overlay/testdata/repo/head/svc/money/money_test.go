package money

import "testing"

func TestSpec(t *testing.T) {
	t.Run("ORD-F02 doubles", func(t *testing.T) {
		if got := Double(2); got != 4 {
			t.Errorf("Double(2) = %d, want 4", got)
		}
	})
}
