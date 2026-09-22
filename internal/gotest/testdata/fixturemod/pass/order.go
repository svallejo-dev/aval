// Package pass is a test2json fixture whose tests all pass or skip.
//
// Its subtest names bind to obligation IDs in every way aval must recognise.
package pass

import "errors"

// ErrDuplicate is returned when a SKU is added to an order twice.
var ErrDuplicate = errors.New("duplicate sku")

// Order is a minimal aggregate that keeps its SKUs unique and in insertion order.
type Order struct {
	seen map[string]struct{}
	skus []string
}

// Add appends sku to the order, or returns ErrDuplicate if it is already there.
func (o *Order) Add(sku string) error {
	if _, ok := o.seen[sku]; ok {
		return ErrDuplicate
	}
	if o.seen == nil {
		o.seen = make(map[string]struct{})
	}
	o.seen[sku] = struct{}{}
	o.skus = append(o.skus, sku)
	return nil
}

// SKUs returns the SKUs in insertion order.
func (o *Order) SKUs() []string { return o.skus }
