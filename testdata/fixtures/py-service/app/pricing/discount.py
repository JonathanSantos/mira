from .money import Money


def calculate(base: Money, percent: int) -> Money:
    """Applies a percentage discount."""
    if percent <= 0:
        return base
    return base.plus(base.percent(-percent))
