package pricing

// Calculate applies a percentage discount.
func Calculate(base Money, percent int) Money {
	if percent <= 0 {
		return base
	}
	return base.Plus(base.Percent(-percent))
}
