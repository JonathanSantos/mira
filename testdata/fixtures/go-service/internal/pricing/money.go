package pricing

// Money is an amount in cents.
type Money struct {
	Cents int64
}

// Zero is the empty amount.
var Zero = Money{}

func (m Money) Plus(o Money) Money { return Money{Cents: m.Cents + o.Cents} }

func (m Money) Percent(p int) Money { return Money{Cents: m.Cents * int64(p) / 100} }
