// Package c is the part of the overlay fixture without test files.
package c

// Balance returns the sum of entries.
func Balance(entries []int) int {
	var sum int
	for _, e := range entries {
		sum += e
	}
	return sum
}
