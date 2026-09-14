package com.acme.order;

import java.util.List;
import com.acme.pricing.Money;

public class Order {
    private final String id;
    private final List<Money> lines;

    public Order(String id, List<Money> lines) {
        this.id = id;
        this.lines = lines;
    }

    public String id() {
        return id;
    }

    public List<Money> lines() {
        return lines;
    }
}
