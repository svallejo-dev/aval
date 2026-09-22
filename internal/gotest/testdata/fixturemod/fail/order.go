// Package fail is a test2json fixture with failing tests, bound and unbound.
package fail

import "errors"

// ErrDuplicate is returned when a SKU is added to an order twice.
var ErrDuplicate = errors.New("duplicate sku")

// Order is meant to keep its SKUs unique. Add has a deliberate bug: it never
// returns ErrDuplicate.
type Order struct {
	skus []string
}

// Add appends sku to the order.
func (o *Order) Add(sku string) error {
	o.skus = append(o.skus, sku)
	return nil
}

// Len returns the number of SKUs in the order.
func (o *Order) Len() int { return len(o.skus) }
