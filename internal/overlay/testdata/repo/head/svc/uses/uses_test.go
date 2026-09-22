package uses

import (
	"testing"

	"example.com/svc/helper"
)

func TestUses(t *testing.T) {
	t.Run("ORD-F14 greets through the new package", func(t *testing.T) {
		if helper.Greeting() != "hi" {
			t.Error("wrong greeting")
		}
	})
}
