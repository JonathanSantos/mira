package typescript

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

// skeletonOf parseia o fonte e monta o esqueleto do símbolo, reencontrando o
// nó pelo span gravado, como o graph faz.
func skeletonOf(t *testing.T, src, name string) extract.Skeleton {
	t.Helper()
	tree, err := parser.New().Parse(lang.TypeScript, []byte(src))
	require.NoError(t, err)
	require.False(t, tree.HasError, "parse error in fixture")
	sym := symbol(t, New().Extract(tree), name)
	sk, ok := Skeleton(tree.Root().NamedDescendantForByteRange(sym.StartByte, sym.EndByte))
	require.True(t, ok, "no skeleton for %s", name)
	return sk
}

func TestSkeletonFunction(t *testing.T) {
	src := `import { load } from './repo';

export async function handle(id: string, opts?: { strict: boolean }): Promise<number> {
  const item = await load(id);
  let total = 0;
  if (!item) {
    throw new Error('missing ' + id);
  }
  for (const line of item.lines) {
    total += line.price;
  }
  item.lines.forEach((line) => {
    if (line.price < 0) {
      return;
    }
  });
  const helper = () => { return 1; };
  try {
    return opts?.strict ? total : Math.round(total);
  } catch (err) {
    return -1;
  }
}
`
	sk := skeletonOf(t, src, "handle")
	assert.Equal(t, 21, sk.Lines)
	assert.True(t, sk.Async)
	assert.Equal(t, 2, sk.Nested, "the forEach callback and helper are nested functions")
	assert.Equal(t, 4, sk.Branches, "if, if in callback, ternary, catch")
	assert.Equal(t, 1, sk.Loops)
	assert.Equal(t, []extract.Point{
		{Line: 19, Text: "return opts?.strict ? total : Math.round(total);"},
		{Line: 21, Text: "return -1;"},
	}, sk.Returns, "returns inside nested functions are not the outer function's")
	assert.Equal(t, []extract.Point{{Line: 7, Text: "throw new Error('missing ' + id);"}}, sk.Throws)
	assert.Equal(t, []extract.Point{{Line: 4, Text: "item"}, {Line: 5, Text: "total"}}, sk.Locals, "helper is a closure, not data")
	for _, name := range []string{"id", "opts", "item", "total", "line", "err", "helper"} {
		assert.True(t, sk.Declared[name], "declared: %s", name)
	}
	assert.False(t, sk.Declared["load"], "imported names are not declared here")
}

func TestSkeletonCurried(t *testing.T) {
	src := `export const handleSubmit = (onValid, onInvalid) => async (e) => {
  if (e) {
    return;
  }
  const values = await onValid(e);
  return values;
};
`
	sk := skeletonOf(t, src, "handleSubmit")
	assert.True(t, sk.Async, "the inner arrow is async")
	assert.Equal(t, 0, sk.Nested, "the curried inner arrow is the function itself")
	assert.Equal(t, []extract.Point{{Line: 3, Text: "return;"}, {Line: 6, Text: "return values;"}}, sk.Returns)
	assert.Equal(t, []extract.Point{{Line: 5, Text: "values"}}, sk.Locals)
	for _, name := range []string{"onValid", "onInvalid", "e", "values"} {
		assert.True(t, sk.Declared[name], "declared: %s", name)
	}
}

func TestSkeletonArrowAndMethod(t *testing.T) {
	src := `export const double = (n: number) => n * 2;

const factory = () => {
  const inner = (x: number) => {
    if (x) { return x; }
    return 0;
  };
  return { inner };
};

class Svc {
  private cache = new Map<string, number>();

  get(id: string): number | undefined {
    const { a, b } = this.split(id);
    return this.cache.get(a + b);
  }

  handle = async (id: string) => {
    return this.get(id);
  };
}
`
	double := skeletonOf(t, src, "double")
	assert.Equal(t, []extract.Point{{Line: 1, Text: "n * 2"}}, double.Returns, "concise arrow: the expression is the return")
	assert.Empty(t, double.Locals)

	factory := skeletonOf(t, src, "factory")
	assert.Equal(t, []extract.Point{{Line: 8, Text: "return { inner };"}}, factory.Returns)
	assert.Equal(t, 1, factory.Nested)
	assert.Equal(t, 1, factory.Branches, "branches count the whole body, nested closures included")

	inner := skeletonOf(t, src, "inner")
	assert.Equal(t, []extract.Point{{Line: 5, Text: "return x;"}, {Line: 6, Text: "return 0;"}}, inner.Returns)

	get := skeletonOf(t, src, "get")
	assert.Equal(t, []extract.Point{{Line: 16, Text: "return this.cache.get(a + b);"}}, get.Returns)
	assert.Equal(t, []extract.Point{{Line: 15, Text: "{ a, b }"}}, get.Locals, "destructuring keeps the pattern text")

	handle := skeletonOf(t, src, "handle")
	assert.True(t, handle.Async)
	assert.Equal(t, []extract.Point{{Line: 20, Text: "return this.get(id);"}}, handle.Returns)
}

func TestSkeletonLocalsRange(t *testing.T) {
	src := `export function build() {
  const methods = {
    a: 1,
    b: 2,
  };
  const n = 1;
  const cb = () => {
    return 1;
  };
  return methods;
}
`
	sk := skeletonOf(t, src, "build")
	assert.Equal(t, []extract.Point{{Line: 2, EndLine: 5, Text: "methods"}, {Line: 6, Text: "n"}}, sk.Locals,
		"multi-line values carry the end line; closures are nested functions")
}

func TestSkeletonNotAFunction(t *testing.T) {
	src := "export class Empty {}\nexport const LIMIT = 3;\n"
	tree, err := parser.New().Parse(lang.TypeScript, []byte(src))
	require.NoError(t, err)
	res := New().Extract(tree)
	for _, name := range []string{"Empty", "LIMIT"} {
		sym := symbol(t, res, name)
		_, ok := Skeleton(tree.Root().NamedDescendantForByteRange(sym.StartByte, sym.EndByte))
		assert.False(t, ok, name)
	}
}
