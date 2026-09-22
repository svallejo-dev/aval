// Package a is the part of the overlay fixture that builds and passes.
package a

// Reserve returns the stock left after reserving n units, or false.
func Reserve(stock, n int) (int, bool) {
	if n > stock {
		return stock, false
	}
	return stock - n, true
}
