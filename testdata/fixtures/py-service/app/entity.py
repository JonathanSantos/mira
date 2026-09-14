class Entity:
    id: str = ""

    def key(self) -> str:
        return "order:" + self.id
