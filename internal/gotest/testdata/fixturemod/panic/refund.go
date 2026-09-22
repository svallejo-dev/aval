// Package panic is a test2json fixture: a subtest panics and takes the whole
// test binary down with it, so later subtests never run.
package panic

import "fmt"

// Refund returns the amount to give back for a charge. It has a deliberate
// bug: it panics on a negative amount instead of returning an error.
func Refund(amount int) int {
	if amount < 0 {
		panic(fmt.Sprintf("refund: negative amount %d", amount))
	}
	return amount
}
