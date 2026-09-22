// Package buildfail is a test2json fixture: the package itself compiles, but
// its test file does not.
package buildfail

// Total returns the sum of prices.
func Total(prices []int) int {
	var sum int
	for _, p := range prices {
		sum += p
	}
	return sum
}
