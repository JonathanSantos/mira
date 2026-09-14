package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/lang"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		path string
		want lang.Lang
		ok   bool
	}{
		{"src/a.ts", lang.TypeScript, true},
		{"src/a.tsx", lang.TypeScript, true},
		{"src/a.d.ts", lang.TypeScript, true},
		{"src/a.js", lang.JavaScript, true},
		{"src/a.jsx", lang.JavaScript, true},
		{"src/a.mjs", lang.JavaScript, true},
		{"src/a.cjs", lang.JavaScript, true},
		{"src/A.java", lang.Java, true},
		{"src/a.css", "", false},
		{"src/a.py", lang.Python, true},
		{"README.md", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, ok := Detect(tt.path)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseFamilies(t *testing.T) {
	p := New()
	tests := []struct {
		name string
		lang lang.Lang
		src  string
		root string
	}{
		{"typescript class with methods", lang.TypeScript, "class A {\n  total(): number { return 1; }\n}\n", "program"},
		{"jsx in javascript", lang.JavaScript, "const A = () => <div><B /></div>;\n", "program"},
		{"java", lang.Java, "package a;\nclass A { int b() { return 1; } }\n", "program"},
		{"ambiguous generics do not exhaust the GLR", lang.TypeScript,
			"export interface O { id: string; coupon?: string; items: I[]; }\n@Injectable()\nexport class S {\n  private readonly m = new Map<string, O>();\n  f(id: string): O | undefined { return this.m.get(id); }\n  t(o: O): number { return o.items.reduce((s, i) => s + i.p * i.q, 0); }\n}\n", "program"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, err := p.Parse(tt.lang, []byte(tt.src))
			require.NoError(t, err)
			defer tree.Release()
			assert.False(t, tree.HasError)
			assert.Equal(t, tt.root, tree.Root().Type())
		})
	}
}

func TestNodeHelpers(t *testing.T) {
	tree, err := New().Parse(lang.TypeScript, []byte("export function f(a: number) {\n  return a;\n}\n"))
	require.NoError(t, err)
	defer tree.Release()
	fn := tree.Root().Child("export_statement").Child("function_declaration")
	require.False(t, fn.IsNil())
	assert.Equal(t, "f", fn.Child("identifier").Text())
	assert.Equal(t, 1, fn.StartLine())
	assert.Equal(t, 3, fn.EndLine())
	assert.Equal(t, 8, fn.StartCol())
	assert.Equal(t, "{", fn.Child("statement_block").AnonChild("{").Text())
	assert.True(t, fn.Child("nope").IsNil())
	assert.Equal(t, "", fn.Child("nope").Text(), "nil node is safe to read")
	assert.Equal(t, 0, fn.Child("nope").StartLine())
	assert.Equal(t, "export_statement", fn.Parent().Type())
	assert.Equal(t, "program", fn.Ancestor("program").Type())

	var visited []string
	fn.Walk(func(n Node) bool {
		visited = append(visited, n.Type())
		return !n.Is("formal_parameters")
	})
	assert.Contains(t, visited, "statement_block")
	assert.NotContains(t, visited, "required_parameter", "walk skips children when fn returns false")
}
