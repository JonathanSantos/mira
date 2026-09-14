package com.acme.pricing;

public record Money(long cents) {
    public static final Money ZERO = new Money(0);

    public Money plus(Money other) {
        return new Money(cents + other.cents());
    }

    public Money percent(int percent) {
        return new Money(cents * percent / 100);
    }
}
