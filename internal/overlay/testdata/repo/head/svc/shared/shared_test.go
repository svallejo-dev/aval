package shared

import "testing"

func TestLevel(t *testing.T) {
	t.Run("ORD-F19 raises the level while f runs", func(t *testing.T) {
		Raise(func() {
			if got := Level(); got != 1 {
				t.Errorf("inside: Level() = %d, want 1", got)
			}
		})
		if got := Level(); got != 0 {
			t.Errorf("after: Level() = %d, want 0", got)
		}
	})
	t.Run("ORD-F18 starts at level zero", func(t *testing.T) {
		if got := Level(); got != 0 {
			t.Errorf("Level() = %d, want 0", got)
		}
	})
}
