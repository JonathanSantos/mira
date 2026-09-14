package main

import (
	"fmt"

	"github.com/acme/goservice/internal/order"
	"github.com/acme/goservice/internal/pricing"
)

func main() {
	svc := order.NewService(10)
	o := &order.Order{Coupon: "VIP"}
	total := svc.Total(o)
	first, err := o.First()
	fmt.Println(total.Cents, first.Price.Cents, err, o.Key(), pricing.Zero)
}
