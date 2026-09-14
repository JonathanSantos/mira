package com.acme.order;

import com.acme.pricing.*;

// OrderService e Order vêm da mesma package, sem import; Money vem do wildcard.
public class OrderController {
    private final OrderService service;

    public OrderController(OrderService service) {
        this.service = service;
    }

    public Money show(Order order) {
        return service.total(order);
    }
}
