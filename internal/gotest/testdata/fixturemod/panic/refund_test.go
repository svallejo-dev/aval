package panic

import "testing"

// TestRefund panics in its second ID subtest; the third one never runs.
func TestRefund(t *testing.T) {
	t.Run("ORD-F20 refunds the full amount", func(t *testing.T) {
		if got := Refund(5); got != 5 {
			t.Errorf("Refund(5) = %d, want 5", got)
		}
	})
	t.Run("ORD-F21 rejects a negative amount", func(t *testing.T) {
		if got := Refund(-1); got != 0 {
			t.Errorf("Refund(-1) = %d, want 0", got)
		}
	})
	t.Run("ORD-F22 refunds zero", func(t *testing.T) {
		if got := Refund(0); got != 0 {
			t.Errorf("Refund(0) = %d, want 0", got)
		}
	})
}

// TestRefundLater never runs either: the binary is gone by then.
func TestRefundLater(t *testing.T) {
	t.Run("ORD-F23 refunds twice", func(t *testing.T) {
		if got := Refund(1) + Refund(1); got != 2 {
			t.Errorf("two refunds of 1 = %d, want 2", got)
		}
	})
}
