package a

import "testing"

func TestReserve(t *testing.T) {
	t.Run("ORD-F40 reserves available stock", func(t *testing.T) {
		if left, ok := Reserve(3, 2); !ok || left != 1 {
			t.Errorf("Reserve(3, 2) = %d, %v; want 1, true", left, ok)
		}
	})
}
