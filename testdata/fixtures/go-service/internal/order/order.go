package order

import "github.com/acme/goservice/internal/pricing"

// Entity is embedded by every persisted type.
type Entity struct {
	ID string
}

func (e Entity) Key() string { return "order:" + e.ID }

type Line struct {
	Price pricing.Money
	Qty   int
}

type Order struct {
	Entity
	Coupon string
	lines  []Line
}

// Service totals orders.
type Service interface {
	Total(o *Order) pricing.Money
}
