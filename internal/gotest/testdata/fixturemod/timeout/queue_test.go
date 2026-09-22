package timeout

import "testing"

func TestQueue(t *testing.T) {
	t.Run("ORD-F08 drains the queue", func(*testing.T) {
		Drain()
	})
}
