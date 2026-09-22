package pass

import (
	"errors"
	"slices"
	"testing"
)

// TestOrder binds table cases to obligations through their names. It covers
// repeated names (go test appends #01), a bare ID, a "/" inside a name (which
// creates a fake level) and a colon right after the ID.
func TestOrder(t *testing.T) {
	tests := []struct {
		name    string
		skus    []string
		wantErr error
	}{
		{name: "ORD-F01 rejects duplicates", skus: []string{"a", "a"}, wantErr: ErrDuplicate},
		{name: "ORD-F01 rejects duplicates", skus: []string{"b", "b"}, wantErr: ErrDuplicate},
		{name: "ORD-F02 accepts distinct skus", skus: []string{"a", "b"}},
		{name: "ORD-N01", skus: []string{"a"}},
		{name: "ORD-N01", skus: []string{"b"}},
		{name: "ORD-F03 keeps in/out order", skus: []string{"in", "out"}},
		{name: "ORD-F04: colon after the ID", skus: []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o Order
			var err error
			for _, sku := range tt.skus {
				if err = o.Add(sku); err != nil {
					break
				}
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("adding %q: got error %v, want %v", tt.skus, err, tt.wantErr)
			}
		})
	}
}

// TestOrderNested nests plain subtests under an ID subtest, and an ID subtest
// under a plain one.
func TestOrderNested(t *testing.T) {
	t.Run("ORD-F05 lists skus in insertion order", func(t *testing.T) {
		t.Run("empty order", func(t *testing.T) {
			var o Order
			if got := o.SKUs(); len(got) != 0 {
				t.Errorf("SKUs() = %q, want none", got)
			}
		})
		t.Run("two skus", func(t *testing.T) {
			var o Order
			for _, sku := range []string{"b", "a"} {
				if err := o.Add(sku); err != nil {
					t.Fatalf("Add(%q): %v", sku, err)
				}
			}
			if got, want := o.SKUs(), []string{"b", "a"}; !slices.Equal(got, want) {
				t.Errorf("SKUs() = %q, want %q", got, want)
			}
		})
	})
	t.Run("happy path", func(t *testing.T) {
		t.Run("ORD-F06 accepts a single sku", func(t *testing.T) {
			var o Order
			if err := o.Add("a"); err != nil {
				t.Errorf("Add(%q): %v", "a", err)
			}
		})
	})
}

// TestOrderAttr binds through t.Attr instead of the subtest name. The same
// key is set twice to show that every call is reported.
func TestOrderAttr(t *testing.T) {
	t.Run("duplicate sku is rejected", func(t *testing.T) {
		t.Attr("aval.req", "ORD-F01")
		t.Attr("aval.req", "ORD-F02")
		var o Order
		if err := o.Add("a"); err != nil {
			t.Fatalf("Add(%q): %v", "a", err)
		}
		if err := o.Add("a"); !errors.Is(err, ErrDuplicate) {
			t.Errorf("second Add(%q) = %v, want %v", "a", err, ErrDuplicate)
		}
	})
}

// TestOrderSkip has an ID subtest that skips itself.
func TestOrderSkip(t *testing.T) {
	t.Run("ORD-F07 survives a restart", func(t *testing.T) {
		t.Skip("needs a database")
	})
}
