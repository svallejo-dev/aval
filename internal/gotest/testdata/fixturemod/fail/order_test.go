package fail

import (
	"errors"
	"testing"
)

// TestOrder has a failing ID subtest with a multi-line message next to a
// passing one.
func TestOrder(t *testing.T) {
	t.Run("ORD-F01 rejects duplicates", func(t *testing.T) {
		var o Order
		if err := o.Add("a"); err != nil {
			t.Fatalf("first Add(%q): %v", "a", err)
		}
		if err := o.Add("a"); !errors.Is(err, ErrDuplicate) {
			t.Errorf("second Add(%q):\n got: %v\nwant: %v", "a", err, ErrDuplicate)
		}
	})
	t.Run("ORD-F02 accepts distinct skus", func(t *testing.T) {
		var o Order
		for _, sku := range []string{"a", "b"} {
			if err := o.Add(sku); err != nil {
				t.Fatalf("Add(%q): %v", sku, err)
			}
		}
	})
}

// TestNoID fails without binding to any obligation.
func TestNoID(t *testing.T) {
	var o Order
	if got, want := o.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}
