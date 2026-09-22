// Package rapidpass is a test2json fixture: a rapid state machine whose
// invariant holds.
package rapidpass

// Counter is a counter aggregate whose value stays within [0, ceiling].
type Counter struct {
	n, ceiling int
}

// New returns a Counter at zero that never goes above ceiling.
func New(ceiling int) *Counter { return &Counter{ceiling: ceiling} }

// Inc adds one unless the counter is at its maximum.
func (c *Counter) Inc() {
	if c.n < c.ceiling {
		c.n++
	}
}

// Dec subtracts one unless the counter is at zero.
func (c *Counter) Dec() {
	if c.n > 0 {
		c.n--
	}
}

// Value returns the current count.
func (c *Counter) Value() int { return c.n }
