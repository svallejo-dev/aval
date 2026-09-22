package reader

import (
	"os"
	"strings"
	"testing"
)

func TestShout(t *testing.T) {
	t.Run("ORD-F10 shouts the fixture", func(t *testing.T) {
		in, err := os.ReadFile("fixtures/in.txt")
		if err != nil {
			t.Fatal(err)
		}
		if got := Shout(strings.TrimSpace(string(in))); got != "HI" {
			t.Errorf("Shout = %q, want HI", got)
		}
	})
}
