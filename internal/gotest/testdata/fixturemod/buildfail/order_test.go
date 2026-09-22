package buildfail

import "testing"

// TestTotal does not compile on purpose: Subtotal does not exist.
func TestTotal(t *testing.T) {
	t.Run("ORD-F10 sums prices", func(t *testing.T) {
		if got := Subtotal([]int{1, 2}); got != 3 {
			t.Errorf("Subtotal() = %d, want 3", got)
		}
	})
}
