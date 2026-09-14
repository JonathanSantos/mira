import logging
from .entity import Entity
from .pricing import Money, calculate
from app.pricing import money as money_mod

log = logging.getLogger(__name__)


class Line:
    def __init__(self, price: Money, qty: int):
        self.price = price
        self.qty = qty


class Order(Entity):
    def __init__(self, coupon: str = ""):
        self.coupon = coupon
        self.lines: list[Line] = []

    def first(self) -> Line:
        if not self.lines:
            raise ValueError("empty order")
        return self.lines[0]


class Service:
    def __init__(self, percent: int):
        self.percent = percent

    def total(self, order: Order) -> Money:
        total = money_mod.ZERO
        for line in order.lines:
            total = total.plus(line.price.percent(100 * line.qty))
        if not order.coupon:
            return total
        log.info("coupon %s", order.coupon)
        return calculate(total, self.percent)

    def first_price(self, order: Order) -> Money:
        return order.first().price
