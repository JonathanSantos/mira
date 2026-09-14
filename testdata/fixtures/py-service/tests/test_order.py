import pytest

from app.order import Order, Service


def test_total():
    svc = Service(10)
    order = Order()
    assert svc.total(order).cents == 0
    assert order.key() == "order:"
    with pytest.raises(ValueError):
        order.first()
