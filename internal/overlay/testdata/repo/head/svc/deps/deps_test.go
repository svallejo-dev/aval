package deps

import (
	"testing"

	"example.com/dep"
)

func TestDep(t *testing.T) {
	t.Run("ORD-F13 uses the new module", func(t *testing.T) {
		if dep.Answer() != 42 {
			t.Error("wrong answer")
		}
	})
}
