package rapidfail

import (
	"testing"

	"pgregory.net/rapid"
)

const limit = 3

// counterMachine drives a Counter with random Inc/Dec sequences.
type counterMachine struct {
	c *Counter
}

func (m *counterMachine) Inc(*rapid.T) { m.c.Inc() }
func (m *counterMachine) Dec(*rapid.T) { m.c.Dec() }

// Check is the invariant rapid runs after every action.
func (m *counterMachine) Check(t *rapid.T) {
	if v := m.c.Value(); v < 0 || v > limit {
		t.Fatalf("Value() = %d, want within [0, %d]", v, limit)
	}
}

func TestCounter(t *testing.T) {
	t.Run("ORD-I01 counter stays within bounds", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			t.Repeat(rapid.StateMachineActions(&counterMachine{c: New(limit)}))
		})
	})
}
