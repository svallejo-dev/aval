// Package money gains Double at head.
package money

// Cents is an amount of money.
type Cents int

// Double doubles c.
func Double(c Cents) Cents { return 2 * c }
