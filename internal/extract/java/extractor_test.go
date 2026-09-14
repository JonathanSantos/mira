package java

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

func run(t *testing.T, src string) extract.Result {
	t.Helper()
	tree, err := parser.New().Parse(lang.Java, []byte(src))
	require.NoError(t, err)
	require.False(t, tree.HasError, "parse error in fixture")
	return New().Extract(tree)
}

func symbol(t *testing.T, res extract.Result, qualified string) extract.Symbol {
	t.Helper()
	for _, s := range res.Symbols {
		if s.QualifiedName == qualified {
			return s
		}
	}
	require.Failf(t, "symbol not found", "%s in %+v", qualified, res.Symbols)
	return extract.Symbol{}
}

func refsOf(res extract.Result, name string) []extract.Ref {
	var out []extract.Ref
	for _, r := range res.Refs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

const service = `package com.acme.order;

import java.util.List;
import com.acme.pricing.Discount;
import com.acme.pricing.*;
import static com.acme.pricing.Discount.calculate;

@Service
public class DefaultOrderService implements OrderService {
    private final Discount discount;
    public static final int MAX = 10;

    public DefaultOrderService(Discount discount) {
        this.discount = discount;
    }

    @Override
    public Money total(List<Item> items) {
        Money m = Discount.calculate(items);
        int x = calculate(items);
        OrderService svc = new DefaultOrderService(discount);
        svc.total(items);
        this.discount.apply(m);
        var fresh = new Money(x);
        fresh.amount();
        items.forEach(i -> svc.total(i));
        return new Money(x);
    }

    public enum Status { OPEN, CLOSED; public boolean open() { return this == OPEN; } }

    public record Line(String sku, int qty) {}

    interface Inner { void run(); }

    private static class Helper extends BaseHelper<Money> {
        void help() {}
    }
}
`

func TestSymbols(t *testing.T) {
	res := run(t, service)
	assert.Equal(t, "com.acme.order", res.Package)

	tests := []struct {
		qualified string
		want      extract.Symbol
	}{
		{"com.acme.order.DefaultOrderService", extract.Symbol{Name: "DefaultOrderService", QualifiedName: "com.acme.order.DefaultOrderService",
			Kind: "class", Signature: "public class DefaultOrderService implements OrderService", Exported: true}},
		{"com.acme.order.DefaultOrderService.total", extract.Symbol{Name: "total", QualifiedName: "com.acme.order.DefaultOrderService.total",
			Kind: "method", Container: "DefaultOrderService", Signature: "public Money total(List<Item> items)", Exported: true}},
		{"com.acme.order.DefaultOrderService.DefaultOrderService", extract.Symbol{Name: "DefaultOrderService",
			QualifiedName: "com.acme.order.DefaultOrderService.DefaultOrderService", Kind: "constructor", Container: "DefaultOrderService",
			Signature: "public DefaultOrderService(Discount discount)", Exported: true}},
		{"com.acme.order.DefaultOrderService.discount", extract.Symbol{Name: "discount", QualifiedName: "com.acme.order.DefaultOrderService.discount",
			Kind: "field", Container: "DefaultOrderService", Signature: "private final Discount discount", Exported: false}},
		{"com.acme.order.DefaultOrderService.MAX", extract.Symbol{Name: "MAX", QualifiedName: "com.acme.order.DefaultOrderService.MAX",
			Kind: "field", Container: "DefaultOrderService", Signature: "public static final int MAX", Exported: true}},
		{"com.acme.order.DefaultOrderService.Status", extract.Symbol{Name: "Status", QualifiedName: "com.acme.order.DefaultOrderService.Status",
			Kind: "enum", Container: "DefaultOrderService", Signature: "public enum Status", Exported: true}},
		{"com.acme.order.DefaultOrderService.Status.open", extract.Symbol{Name: "open", QualifiedName: "com.acme.order.DefaultOrderService.Status.open",
			Kind: "method", Container: "Status", Signature: "public boolean open()", Exported: true}},
		{"com.acme.order.DefaultOrderService.Line", extract.Symbol{Name: "Line", QualifiedName: "com.acme.order.DefaultOrderService.Line",
			Kind: "record", Container: "DefaultOrderService", Signature: "public record Line(String sku, int qty)", Exported: true}},
		{"com.acme.order.DefaultOrderService.Inner", extract.Symbol{Name: "Inner", QualifiedName: "com.acme.order.DefaultOrderService.Inner",
			Kind: "interface", Container: "DefaultOrderService", Signature: "interface Inner", Exported: false}},
		{"com.acme.order.DefaultOrderService.Inner.run", extract.Symbol{Name: "run", QualifiedName: "com.acme.order.DefaultOrderService.Inner.run",
			Kind: "method", Container: "Inner", Signature: "void run()", Exported: true}},
		{"com.acme.order.DefaultOrderService.Helper", extract.Symbol{Name: "Helper", QualifiedName: "com.acme.order.DefaultOrderService.Helper",
			Kind: "class", Container: "DefaultOrderService", Signature: "private static class Helper extends BaseHelper<Money>", Exported: false}},
	}
	for _, tt := range tests {
		t.Run(tt.qualified, func(t *testing.T) {
			got := symbol(t, res, tt.qualified)
			got.StartLine, got.EndLine, got.StartByte, got.EndByte, got.NameLine, got.Annotations = 0, 0, 0, 0, 0, nil
			assert.Equal(t, tt.want, got)
		})
	}
	total := symbol(t, res, "com.acme.order.DefaultOrderService.total")
	assert.Equal(t, 17, total.StartLine, "range starts at the annotation")
	assert.Equal(t, 28, total.EndLine)
	assert.Equal(t, 18, total.NameLine, "name line is where the identifier sits")
	assert.Equal(t, []string{"@Override"}, total.Annotations)
	assert.Equal(t, []string{"@Service"}, symbol(t, res, "com.acme.order.DefaultOrderService").Annotations)
	assert.Nil(t, symbol(t, res, "com.acme.order.DefaultOrderService.MAX").Annotations)
}

func TestFieldRefsAndAnnotationArgs(t *testing.T) {
	src := `package com.acme;
import jakarta.persistence.Column;
public class Owner {
    @Column(name = "phone") @NotBlank private String telephone;
    private String other;
    public String getTelephone() { return this.telephone; }
    public void setTelephone(String telephone) { this.telephone = telephone; other = telephone; }
    public String label(Owner o) { return o.telephone + telephone; }
}
`
	res := run(t, src)
	assert.Equal(t, []string{`@Column(name = "phone")`, "@NotBlank"}, symbol(t, res, "com.acme.Owner.telephone").Annotations)

	var props []extract.Ref
	for _, r := range refsOf(res, "telephone") {
		if r.Kind == extract.RefProperty {
			props = append(props, r)
		}
	}
	// this.telephone (getter), this.telephone (setter), o.telephone, bare telephone (label)
	require.Len(t, props, 4)
	assert.Equal(t, "this", props[0].Receiver)
	assert.Equal(t, "Owner", props[0].ReceiverType)
	assert.Equal(t, "o", props[2].Receiver)
	assert.Equal(t, "Owner", props[2].ReceiverType)
	assert.Equal(t, "this", props[3].Receiver, "bare field name inside a method is this.field")
	// `other = telephone` in the setter: `telephone` is the parameter, not the field.
	assert.Len(t, refsOf(res, "other"), 1)
}

func TestImports(t *testing.T) {
	res := run(t, service)
	want := []extract.Import{
		{Kind: "java", Module: "java.util.List", ImportedName: "List", LocalName: "List", Line: 3},
		{Kind: "java", Module: "com.acme.pricing.Discount", ImportedName: "Discount", LocalName: "Discount", Line: 4},
		{Kind: "java", Module: "com.acme.pricing", ImportedName: "*", LocalName: "*", IsWildcard: true, Line: 5},
		{Kind: "java_static", Module: "com.acme.pricing.Discount.calculate", ImportedName: "calculate", LocalName: "calculate", Line: 6},
	}
	assert.Equal(t, want, res.Imports)
}

func TestCallArity(t *testing.T) {
	src := "package a;\nclass A {\n  void m(int x, int y) {}\n  void run(A o) { m(1, 2); o.m(1, 2); new A(); String s = String.valueOf(1); }\n}\n"
	res := run(t, src)
	got := map[string]int{}
	for _, r := range res.Refs {
		if r.Kind == extract.RefMethod || r.Kind == extract.RefNew {
			got[r.Name] = r.Arity
		}
	}
	assert.Equal(t, map[string]int{"m": 2, "A": 0, "valueOf": 1}, got)
}

func TestRefs(t *testing.T) {
	res := run(t, service)
	byKind := func(name, kind string) []extract.Ref {
		var out []extract.Ref
		for _, r := range refsOf(res, name) {
			if r.Kind == kind {
				out = append(out, r)
			}
		}
		return out
	}

	// Discount.calculate(items): static call on a type name.
	calls := byKind("calculate", extract.RefMethod)
	require.Len(t, calls, 2)
	assert.Equal(t, "Discount", calls[0].Receiver)
	assert.Equal(t, "Discount", calls[0].ReceiverType)
	// calculate(items): unqualified (static import or own method).
	assert.Equal(t, "", calls[1].Receiver)
	assert.Equal(t, "", calls[1].ReceiverType)

	// svc.total(items): receiver type comes from the local declaration.
	totals := byKind("total", extract.RefMethod)
	require.Len(t, totals, 2)
	assert.Equal(t, "svc", totals[0].Receiver)
	assert.Equal(t, "OrderService", totals[0].ReceiverType)
	assert.Equal(t, "OrderService", totals[1].ReceiverType, "lambda body sees enclosing locals")

	// this.discount.apply(m): field type.
	apply := byKind("apply", extract.RefMethod)
	require.Len(t, apply, 1)
	assert.Equal(t, "Discount", apply[0].ReceiverType)

	// var fresh = new Money(x): inferred from the constructor call.
	amount := byKind("amount", extract.RefMethod)
	require.Len(t, amount, 1)
	assert.Equal(t, "Money", amount[0].ReceiverType)

	// items.forEach: List is not indexed, but the type is still recorded.
	forEach := byKind("forEach", extract.RefMethod)
	require.Len(t, forEach, 1)
	assert.Equal(t, "List", forEach[0].ReceiverType)

	assert.Len(t, byKind("DefaultOrderService", extract.RefNew), 1)
	assert.Len(t, byKind("Money", extract.RefNew), 2)
	assert.NotEmpty(t, byKind("Money", extract.RefType))
	assert.Len(t, byKind("OrderService", extract.RefImplements), 1)
	assert.Len(t, byKind("BaseHelper", extract.RefExtends), 1)
	assert.Len(t, byKind("Service", extract.RefAnnotation), 1)
	assert.Len(t, byKind("Override", extract.RefAnnotation), 1)
	assert.Len(t, byKind("List", extract.RefImport), 1)

	// Annotations attach to the annotated symbol, calls to the enclosing method.
	class := indexOf(res, "com.acme.order.DefaultOrderService")
	method := indexOf(res, "com.acme.order.DefaultOrderService.total")
	assert.Equal(t, class, byKind("Service", extract.RefAnnotation)[0].Container)
	assert.Equal(t, method, byKind("Override", extract.RefAnnotation)[0].Container)
	assert.Equal(t, method, calls[0].Container)
	assert.Equal(t, class, byKind("OrderService", extract.RefImplements)[0].Container)
}

func indexOf(res extract.Result, qualified string) int {
	for i, s := range res.Symbols {
		if s.QualifiedName == qualified {
			return i
		}
	}
	return -1
}

func TestNestedTypeAndAnnotationDeclarations(t *testing.T) {
	src := `package com.acme;
public class Outer {
    @interface Marker {}
    static class Inner {}
    Outer.Inner make() { return new Outer.Inner(); }
    @Marker void run() {}
}
`
	res := run(t, src)
	assert.Equal(t, "annotation", symbol(t, res, "com.acme.Outer.Marker").Kind)
	assert.Equal(t, "class", symbol(t, res, "com.acme.Outer.Inner").Kind)
	// Outer.Inner references only Inner as a type, both in the return type and in `new`.
	assert.Len(t, refsOf(res, "Inner"), 2)
	assert.Empty(t, refsOf(res, "Outer"))
	assert.Equal(t, extract.RefNew, refsOf(res, "Inner")[1].Kind)
	assert.Equal(t, extract.RefAnnotation, refsOf(res, "Marker")[0].Kind)
}
