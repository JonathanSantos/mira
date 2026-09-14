package com.acme.report;

import static com.acme.pricing.Discount.calculate;
import com.acme.pricing.Money;
import com.acme.order.Order;
import com.acme.order.DefaultOrderService;

public class ReportService {
    private final DefaultOrderService orders = new DefaultOrderService(10, DefaultOrderService.Mode.FAST);

    public Money discounted(Money base) {
        return calculate(base, 10);
    }

    public Money forOrder(Order order) {
        return orders.total(order);
    }
}
