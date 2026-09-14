from dataclasses import dataclass


@dataclass
class Money:
    cents: int = 0

    def plus(self, other: "Money") -> "Money":
        return Money(self.cents + other.cents)

    def percent(self, p: int) -> "Money":
        return Money(self.cents * p // 100)


ZERO = Money()
