package legacy

import "testing"

func TestGreet(t *testing.T) {
	t.Run("ORD-F03 greets", func(t *testing.T) {
		if got := Greet("ann"); got != "hello ann" {
			t.Errorf("Greet = %q", got)
		}
	})
	t.Run("ORD-F04 greets by name", func(t *testing.T) {
		if got := Greet("bob"); got != "hello bob" {
			t.Errorf("Greet = %q", got)
		}
	})
}
