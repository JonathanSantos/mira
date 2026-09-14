package com.acme.legacy;

public class Money {
    private final long cents;

    public Money(long cents) {
        this.cents = cents;
    }

    public long getCents() {
        return cents;
    }
}
