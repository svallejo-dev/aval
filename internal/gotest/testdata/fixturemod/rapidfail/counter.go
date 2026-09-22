// Package rapidfail is a test2json fixture: a rapid state machine whose
// invariant breaks, so the failure and shrink output can be captured.
package rapidfail

// Counter is meant to stay within [0, ceiling]. Inc has a deliberate bug: it
// does not stop at the ceiling.
type Counter struct {
	n, ceiling int
}

// New returns a Counter at zero that should never go above ceiling.
func New(ceiling int) *Counter { return &Counter{ceiling: ceiling} }

// Inc adds one.
func (c *Counter) Inc() { c.n++ }

// Dec subtracts one unless the counter is at zero.
func (c *Counter) Dec() {
	if c.n > 0 {
		c.n--
	}
}

// Value returns the current count.
func (c *Counter) Value() int { return c.n }
