package b

import "testing"

// TestRefund does not compile on purpose: PartialRefund does not exist yet.
func TestRefund(t *testing.T) {
	t.Run("ORD-F41 refunds part of an order", func(t *testing.T) {
		if got := PartialRefund(10, 4); got != 4 {
			t.Errorf("PartialRefund(10, 4) = %d, want 4", got)
		}
	})
}
