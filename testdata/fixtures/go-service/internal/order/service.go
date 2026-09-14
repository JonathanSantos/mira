package order

import (
	"errors"

	"github.com/acme/goservice/internal/pricing"
)

type DefaultService struct {
	percent int
}

func NewService(percent int) *DefaultService { return &DefaultService{percent: percent} }

func (s *DefaultService) Total(o *Order) pricing.Money {
	sum := pricing.Zero
	for _, l := range o.Lines() {
		sum = sum.Plus(l.Price.Percent(100 * l.Qty))
	}
	if o.Coupon == "" {
		return sum
	}
	return pricing.Calculate(sum, s.percent)
}

func (o *Order) Lines() []Line { return o.lines }

func (o *Order) First() (Line, error) {
	if len(o.lines) == 0 {
		return Line{}, errors.New("empty order")
	}
	return o.lines[0], nil
}
