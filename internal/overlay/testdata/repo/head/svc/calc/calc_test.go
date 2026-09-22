package calc

import "testing"

func TestSpec(t *testing.T) {
	t.Run("ORD-F01 adds two numbers", func(t *testing.T) {
		if got := Add(2, 3); got != 5 {
			t.Errorf("Add(2, 3) = %d, want 5", got)
		}
	})
}

// TestSuite nests its IDs a level deeper, as suite runners do.
func TestSuite(t *testing.T) {
	t.Run("TestAdd", func(t *testing.T) {
		t.Run("ORD-F08 adds many", func(t *testing.T) {
			if got := Add(Add(1, 2), 3); got != 6 {
				t.Errorf("1 + 2 + 3 = %d, want 6", got)
			}
		})
	})
	t.Run("TestOther", func(t *testing.T) {
		t.Error("a sibling of the selected test ran")
	})
}
