// Package d is the part of the overlay fixture whose new test imports a
// package the change has not created yet.
package d

// Message returns the notification text for an order.
func Message(order string) string { return "order " + order + " shipped" }
