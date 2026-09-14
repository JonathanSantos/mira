package com.acme.pricing;

public class Discount {
    private Discount() {}

    public static Money calculate(Money base, int percent) {
        if (percent <= 0) {
            return base;
        }
        return new Money(base.cents() - base.percent(percent).cents());
    }
}
