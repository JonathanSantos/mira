package com.acme.order;

import com.acme.pricing.Money;

public interface OrderService {
    Money total(Order order);
}
