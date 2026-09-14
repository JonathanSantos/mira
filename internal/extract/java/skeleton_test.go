package java

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

func skeletonOf(t *testing.T, src, qualified string) (extract.Skeleton, bool) {
	t.Helper()
	tree, err := parser.New().Parse(lang.Java, []byte(src))
	require.NoError(t, err)
	require.False(t, tree.HasError, "parse error in fixture")
	sym := symbol(t, New().Extract(tree), qualified)
	return Skeleton(tree.Root().NamedDescendantForByteRange(sym.StartByte, sym.EndByte))
}

func TestSkeletonMethod(t *testing.T) {
	src := `package com.acme;

import java.io.IOException;

public class Svc {
    private final Repo repo;

    @PostMapping("/x")
    public String process(Order order, BindingResult result) throws IOException, IllegalStateException {
        Money total = Money.ZERO;
        if (result.hasErrors()) {
            return "form";
        }
        for (Line line : order.lines()) {
            total = total.plus(line.price());
        }
        order.lines().forEach(line -> {
            if (line.isFree()) {
                return;
            }
            throw new IllegalArgumentException("free line");
        });
        try {
            repo.save(order);
        } catch (RuntimeException e) {
            throw new IOException("save failed", e);
        }
        int count = order.lines().size();
        return count > 0 ? "redirect:/orders" : "empty";
    }

    abstract void nothing();
}
`
	sk, ok := skeletonOf(t, src, "com.acme.Svc.process")
	require.True(t, ok)
	assert.Equal(t, 23, sk.Lines, "the span starts at the annotation")
	assert.Equal(t, []string{"IOException", "IllegalStateException"}, sk.DeclaredThrows)
	assert.Equal(t, 1, sk.Nested, "the lambda")
	assert.Equal(t, 4, sk.Branches, "if, if in lambda, catch, ternary")
	assert.Equal(t, 1, sk.Loops)
	assert.Equal(t, []extract.Point{
		{Line: 12, Text: `return "form";`},
		{Line: 29, Text: `return count > 0 ? "redirect:/orders" : "empty";`},
	}, sk.Returns, "the lambda's return is not the method's")
	assert.Equal(t, []extract.Point{{Line: 26, Text: `throw new IOException("save failed", e);`}}, sk.Throws)
	assert.Equal(t, []extract.Point{{Line: 10, Text: "total"}, {Line: 28, Text: "count"}}, sk.Locals)

	abstract, ok := skeletonOf(t, src, "com.acme.Svc.nothing")
	require.True(t, ok)
	assert.Empty(t, abstract.Returns)
	assert.Equal(t, 1, abstract.Lines)
}

func TestSkeletonLocalsRange(t *testing.T) {
	src := "package a;\nclass A {\n  void run() {\n    Runnable r = new Runnable() {\n      public void run() {}\n    };\n    int n = 1;\n  }\n}\n"
	sk, ok := skeletonOf(t, src, "a.A.run")
	require.True(t, ok)
	assert.Equal(t, []extract.Point{{Line: 4, EndLine: 6, Text: "r"}, {Line: 7, Text: "n"}}, sk.Locals)
	assert.Equal(t, 1, sk.Nested, "the anonymous class body")
}

func TestSkeletonOnlyForMethods(t *testing.T) {
	src := "package com.acme;\npublic class Svc {\n    private final Repo repo = null;\n    public Svc(Repo repo) { this.repo = repo; }\n}\n"
	_, ok := skeletonOf(t, src, "com.acme.Svc")
	assert.False(t, ok, "class")
	_, ok = skeletonOf(t, src, "com.acme.Svc.repo")
	assert.False(t, ok, "field")
	ctor, ok := skeletonOf(t, src, "com.acme.Svc.Svc")
	require.True(t, ok, "constructor")
	assert.Equal(t, 1, ctor.Lines)
}
