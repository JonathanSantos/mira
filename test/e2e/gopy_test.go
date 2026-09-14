package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoFixture(t *testing.T) {
	root := indexed(t, "go-service")
	var st status
	runJSON(t, root, &st, "status")
	assert.Equal(t, 6, st.ByLang["go"].Files)
	assert.Equal(t, 1, st.ByLang["text"].Files, "README.md; go.mod has no indexed extension")

	total := run(t, root, "resolve", "DefaultService.Total", "--include-skeleton")
	assert.Contains(t, total, "DefaultService.Total method internal/order/service.go:15-24")
	assert.Contains(t, total, "  returns (2):\n    21 return sum\n    23 return pricing.Calculate(sum, s.percent)\n")
	assert.Contains(t, total, "Order.Lines :26 (17)", "a method in the same file")
	assert.Contains(t, total, "Calculate internal/pricing/discount.go:4 (23)", "pricing.Calculate through the module import")
	assert.Contains(t, total, "Money.Plus internal/pricing/money.go:11 (18)", "sum := pricing.Zero: Zero is a Money, so sum is")

	refs := run(t, root, "refs", "Key", "--exclude-tests")
	assert.Contains(t, refs, "targets:\n  Entity.Key method internal/order/order.go:10-10")
	assert.Contains(t, refs, "15 [method] in main:10-16: fmt.Println(total.Cents, first.Price.Cents, err, o.Key(), pricing.Zero)", "o.Key() is promoted from the embedded Entity")

	first := run(t, root, "resolve", "Order.First", "--include-skeleton")
	assert.NotContains(t, first, "throws", "Go: no throws")
	assert.Contains(t, first, "  uses (external):\n    errors.New (30)", "a package call is shown as written")

	files := run(t, root, "files", "--dirs-only")
	assert.Contains(t, files, "cmd/app/ files=1")
	search := run(t, root, "search", "coupon")
	assert.Contains(t, search, "~ ", "sub-token of Coupon fields")
}

func TestPythonFixture(t *testing.T) {
	root := indexed(t, "py-service")
	var st status
	runJSON(t, root, &st, "status")
	assert.Equal(t, 7, st.ByLang["python"].Files)

	total := run(t, root, "resolve", "Service.total", "--include-skeleton")
	assert.Contains(t, total, "Service.total method app/order.py:30-37")
	assert.Contains(t, total, "  returns (2):\n    35 return total\n    37 return calculate(total, self.percent)\n")
	assert.Contains(t, total, "calculate app/pricing/discount.py:4 (37)", "from .pricing import calculate, re-exported by __init__")
	assert.Contains(t, total, "Money.plus app/pricing/money.py:8 (33)", "total = money_mod.ZERO: ZERO is a Money()")
	assert.Contains(t, total, "log.info (36)", "log = logging.getLogger(...) is external, shown as written")

	refs := run(t, root, "refs", "key")
	assert.Contains(t, refs, "targets:\n  Entity.key method app/entity.py:4-5")
	assert.Contains(t, refs, "10 [method] in test_total:6-12: assert order.key() == \"order:\"", "order = Order(): key is inherited from Entity")

	first := run(t, root, "resolve", "Order.first", "--include-skeleton")
	assert.Contains(t, first, "  throws (1):\n    22 raise ValueError(\"empty order\")\n")
	price := run(t, root, "resolve", "Service.first_price", "--include-skeleton")
	assert.Contains(t, price, "Line.price", "order.first() -> Line, and self.price in __init__ is a field")

	test := run(t, root, "refs", "total")
	assert.Contains(t, test, "tests/test_order.py (1)")
	assert.Contains(t, test, "in test_total:")
	ext := run(t, root, "refs", "raises")
	assert.Contains(t, ext, "external", "pytest is not in the repository")

	plus := run(t, root, "resolve", "Money.plus")
	require.NotContains(t, plus, "@dataclass", "class decorators are not on the method")
	money := run(t, root, "resolve", "Money", "--kind", "class")
	assert.Contains(t, money, "  @dataclass\n")
}
