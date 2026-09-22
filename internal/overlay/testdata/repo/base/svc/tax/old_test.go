package tax

import "testing"

func TestTax(t *testing.T) {
	t.Run("ORD-F05 takes a cut", func(t *testing.T) {
		if got := Rate(100); got != 10 {
			t.Errorf("Rate(100) = %d, want 10", got)
		}
	})
}
