package com.acme.order;

import java.util.List;
import com.acme.pricing.Discount;
import com.acme.pricing.Money;

public class DefaultOrderService implements OrderService {
    public enum Mode { FAST, SAFE }

    private final int percent;
    private final Mode mode;
    private final Cache cache = new Cache();

    public DefaultOrderService(int percent, Mode mode) {
        this.percent = percent;
        this.mode = mode;
    }

    @Override
    public Money total(Order order) {
        Money cached = cache.get(order.id());
        if (cached != null) {
            return cached;
        }
        Money sum = Money.ZERO;
        List<Money> lines = order.lines();
        for (Money line : lines) {
            sum = sum.plus(line);
        }
        Money result = Discount.calculate(sum, percent);
        cache.put(order.id(), result);
        return result;
    }

    private static class Cache {
        private final java.util.Map<String, Money> values = new java.util.HashMap<>();

        Money get(String id) {
            return values.get(id);
        }

        void put(String id, Money value) {
            values.put(id, value);
        }
    }
}
