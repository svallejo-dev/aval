// Package b is the part of the overlay fixture whose new test does not
// build: it calls PartialRefund, which the change has not added yet.
package b

// Refund returns the amount to give back for a cancelled order.
func Refund(total int) int { return total }
