package editor_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/editor"
	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

const discount = "src/pricing/discount.ts"

// fixture copia o node-backend para um diretório temporário e indexa.
func fixture(t *testing.T) (string, *store.Store) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "fixtures", "node-backend")
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, rel), data, 0o644)
	}))
	return root, openIndexed(t, root)
}

// repoWith monta um repositório com os arquivos dados e indexa.
func repoWith(t *testing.T, files map[string]string) (string, *store.Store) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return root, openIndexed(t, root)
}

func openIndexed(t *testing.T, root string) *store.Store {
	t.Helper()
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	reindex(t, root, st)
	return st
}

func reindex(t *testing.T, root string, st *store.Store) {
	t.Helper()
	_, err := indexer.New(root, repo.DefaultConfig(), st).Run(context.Background(), indexer.Options{})
	require.NoError(t, err)
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(data)
}

// apply edita, reindexa e verifica, na ordem que CLI e MCP usam.
func apply(t *testing.T, root string, st *store.Store, req editor.Request) editor.Result {
	t.Helper()
	res, err := editor.Apply(root, st, req)
	require.NoError(t, err)
	reindex(t, root, st)
	require.NoError(t, editor.Verify(st, &res))
	return res
}

func TestReplaceAndInsert(t *testing.T) {
	root, st := fixture(t)
	res := apply(t, root, st, editor.Request{Action: editor.Replace, File: discount, Symbol: "calculateDiscount",
		Text: "export function calculateDiscount(total: number, coupon?: string): number {\n  // rounded once\n  return roundMoney(applyCoupon(total, coupon));\n}\n"})
	assert.Equal(t, []int{12, 15, 4}, []int{res.StartLine, res.EndLine, res.Lines})
	assert.Equal(t, 2, res.Checked, "the import and the call in order.service.ts are re-checked")
	assert.Empty(t, res.Dangling)
	data := read(t, root, discount)
	assert.Contains(t, data, "  // rounded once\n  return roundMoney(applyCoupon(total, coupon));\n}\n")
	assert.Equal(t, 1, strings.Count(data, "export function calculateDiscount"), "the old definition is gone")

	after := apply(t, root, st, editor.Request{Action: editor.InsertAfter, File: discount, Symbol: "calculateDiscount", Text: "\nexport const VERSION = 2;"})
	assert.Equal(t, []int{16, 17}, []int{after.StartLine, after.EndLine})
	before := apply(t, root, st, editor.Request{Action: editor.InsertBefore, File: discount, Symbol: "applyCoupon", Text: "/** applies a coupon */"})
	assert.Equal(t, 5, before.StartLine)
	data = read(t, root, discount)
	assert.Contains(t, data, "/** applies a coupon */\nexport function applyCoupon(")
	assert.Contains(t, data, "}\n\nexport const VERSION = 2;\n")
}

func TestReplaceIn(t *testing.T) {
	root, st := fixture(t)
	res := apply(t, root, st, editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "calculateDiscount",
		Old: "roundMoney(applyCoupon(total, coupon))", Text: "Math.round(applyCoupon(total, coupon))"})
	assert.Equal(t, []int{13, 13, 1}, []int{res.StartLine, res.EndLine, res.Lines}, "only the changed line is rewritten")
	assert.Equal(t, 2, res.Checked)
	assert.Empty(t, res.Dangling)
	assert.Contains(t, read(t, root, discount), "  return Math.round(applyCoupon(total, coupon));\n")

	tests := []struct {
		name string
		req  editor.Request
		want string
	}{
		{"no old text", editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "applyCoupon", Text: "x"}, "replace-in needs the exact text to find inside applyCoupon"},
		{"not found", editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "applyCoupon", Old: "nope", Text: "x"}, "text not found in applyCoupon (lines 5-10)"},
		{"not unique", editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "applyCoupon", Old: "total", Text: "amount"},
			"text found 3 times in applyCoupon (lines 5-10); include surrounding text to make it unique, or pass all"},
		{"scoped to the symbol", editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "applyCoupon", Old: "roundMoney", Text: "x"}, "text not found in applyCoupon"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := editor.Apply(root, st, tc.req)
			assert.ErrorContains(t, err, tc.want)
		})
	}

	all := apply(t, root, st, editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "applyCoupon", Old: "total", Text: "amount", All: true})
	assert.Equal(t, []int{5, 9}, []int{all.StartLine, all.EndLine}, "from the first to the last changed line")
	data := read(t, root, discount)
	assert.Contains(t, data, "export function applyCoupon(amount: number, coupon?: string): number {\n")
	assert.Contains(t, data, "  return amount * (1 - COUPONS[coupon]);\n")
	assert.Contains(t, data, "export function calculateDiscount(total: number", "other symbols keep their text")

	pre, err := editor.Apply(root, st, editor.Request{Action: editor.ReplaceIn, File: discount, Symbol: "calculateDiscount", Old: "Math.round", Text: "roundMoney", Preview: true})
	require.NoError(t, err)
	assert.Equal(t, discount+"\n  -  13|   return Math.round(applyCoupon(total, coupon));\n  +  13|   return roundMoney(applyCoupon(total, coupon));\n", pre.Diff)
}

func TestVerifyReportsBreakage(t *testing.T) {
	root, st := fixture(t)
	renamed := apply(t, root, st, editor.Request{Action: editor.Replace, File: discount, Symbol: "calculateDiscount",
		Text: "export function computeDiscount(total: number, coupon?: string): number {\n  return roundMoney(applyCoupon(total, coupon));\n}"})
	assert.Equal(t, 2, renamed.Checked)
	require.Len(t, renamed.Dangling, 2, "renaming inside the replacement text breaks the import and the call")
	assert.Equal(t, []int{2, 26}, []int{renamed.Dangling[0].Line, renamed.Dangling[1].Line})
	assert.Equal(t, "src/orders/order.service.ts", renamed.Dangling[0].File)

	broken := apply(t, root, st, editor.Request{Action: editor.Replace, File: discount, Symbol: "applyCoupon",
		Text: "export function applyCoupon(total: number {\n  return total;\n}"})
	assert.Contains(t, strings.Join(broken.Notes, "\n"), discount+": does not parse after the edit")
}

func TestEditErrors(t *testing.T) {
	root, st := fixture(t)
	tests := []struct {
		name string
		req  editor.Request
		want string
	}{
		{"file not indexed", editor.Request{Action: editor.Replace, File: "src/nope.ts", Symbol: "x", Text: "y"}, "file src/nope.ts is not indexed"},
		{"symbol not in the file", editor.Request{Action: editor.Replace, File: discount, Symbol: "missing", Text: "y"}, "symbol missing not found"},
		{"unknown action", editor.Request{Action: "move", File: discount, Symbol: "applyCoupon"}, `unknown action "move"`},
		{"replace without text", editor.Request{Action: editor.Replace, File: discount, Symbol: "applyCoupon"}, "no text for replace"},
		{"delete with text", editor.Request{Action: editor.Delete, File: discount, Symbol: "applyCoupon", Text: "x"}, "delete takes no text"},
		{"invalid new name", editor.Request{Action: editor.Rename, File: discount, Symbol: "applyCoupon", Text: "apply coupon"}, `invalid new name "apply coupon"`},
		{"same name", editor.Request{Action: editor.Rename, File: discount, Symbol: "applyCoupon", Text: "applyCoupon"}, "already named applyCoupon"},
		{"name taken in the same scope", editor.Request{Action: editor.Rename, File: discount, Symbol: "applyCoupon", Text: "calculateDiscount"},
			"calculateDiscount is already declared in the same scope (src/pricing/discount.ts:12); pick another name"},
		{"delete while referenced", editor.Request{Action: editor.Delete, File: discount, Symbol: "applyCoupon"},
			"applyCoupon is still referenced 1 time(s): src/pricing/discount.ts:13; update those first, or pass force"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := editor.Apply(root, st, tc.req)
			assert.ErrorContains(t, err, tc.want)
		})
	}
	original := read(t, root, discount)
	assert.Contains(t, original, "export function applyCoupon(", "failed edits write nothing")

	require.NoError(t, os.WriteFile(filepath.Join(root, discount), []byte("// changed\n"+original), 0o644))
	_, err := editor.Apply(root, st, editor.Request{Action: editor.Delete, File: discount, Symbol: "calculateDiscount", Force: true})
	assert.ErrorContains(t, err, "changed since it was indexed")
}

const tsModule = "import { used } from './b';\n\n/**\n * Doc for old.\n */\nexport function old(): number {\n  return 1;\n}\n\nexport function keep(): number {\n  return used();\n}\n"

func TestDelete(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		file   string
		symbol string
		want   string
	}{
		{
			name:  "typescript: the doc comment and the extra blank line go too",
			files: map[string]string{"src/a.ts": tsModule, "src/b.ts": "export function used(): number {\n  return 2;\n}\n"},
			file:  "src/a.ts", symbol: "old",
			want: "import { used } from './b';\n\nexport function keep(): number {\n  return used();\n}\n",
		},
		{
			name:  "typescript: the last symbol leaves no trailing blank line",
			files: map[string]string{"src/a.ts": tsModule, "src/b.ts": "export function used(): number {\n  return 2;\n}\n"},
			file:  "src/a.ts", symbol: "keep",
			want: "import { used } from './b';\n\n/**\n * Doc for old.\n */\nexport function old(): number {\n  return 1;\n}\n",
		},
		{
			name:  "typescript: last member of a class",
			files: map[string]string{"src/cart.ts": "export class Cart {\n  keep(): number {\n    return 1;\n  }\n\n  old(): number {\n    return 2;\n  }\n}\n"},
			file:  "src/cart.ts", symbol: "Cart.old",
			want: "export class Cart {\n  keep(): number {\n    return 1;\n  }\n}\n",
		},
		{
			name:  "python: comment above and PEP 8 spacing kept",
			files: map[string]string{"app/util.py": "import os\n\n\n# helper doc\ndef helper():\n    return 1\n\n\ndef main():\n    return os.getcwd()\n"},
			file:  "app/util.py", symbol: "helper",
			want: "import os\n\n\ndef main():\n    return os.getcwd()\n",
		},
		{
			name:  "go: doc comment",
			files: map[string]string{"go.mod": "module example.com/p\n\ngo 1.22\n", "p.go": "package p\n\n// Old does nothing.\nfunc Old() {}\n\n// Keep stays.\nfunc Keep() {}\n"},
			file:  "p.go", symbol: "Old",
			want: "package p\n\n// Keep stays.\nfunc Keep() {}\n",
		},
		{
			name:  "java: javadoc and annotation of the first member",
			files: map[string]string{"src/A.java": "package p;\n\npublic class A {\n    /** Old. */\n    @Deprecated\n    public void old() {\n    }\n\n    public void keep() {\n    }\n}\n"},
			file:  "src/A.java", symbol: "A.old",
			want: "package p;\n\npublic class A {\n    public void keep() {\n    }\n}\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, st := repoWith(t, tc.files)
			res := apply(t, root, st, editor.Request{Action: editor.Delete, File: tc.file, Symbol: tc.symbol})
			assert.Equal(t, tc.want, read(t, root, tc.file))
			assert.Empty(t, res.Dangling)
		})
	}
}

func TestDeleteRefusesWhileReferenced(t *testing.T) {
	root, st := repoWith(t, map[string]string{
		"src/cart.ts": "export class Cart {\n  total(): number {\n    return 0;\n  }\n}\n",
		"src/use.ts":  "import { Cart } from './cart';\n\nexport function sum(cart: Cart): number {\n  return cart.total();\n}\n",
	})
	before := read(t, root, "src/cart.ts")
	_, err := editor.Apply(root, st, editor.Request{Action: editor.Delete, File: "src/cart.ts", Symbol: "Cart"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cart is still referenced 3 time(s): src/use.ts:1, src/use.ts:3, src/use.ts:4",
		"the import, the parameter type and the call to a member")
	assert.Equal(t, before, read(t, root, "src/cart.ts"))

	forced := apply(t, root, st, editor.Request{Action: editor.Delete, File: "src/cart.ts", Symbol: "Cart", Force: true})
	assert.Len(t, forced.Dangling, 3)
	assert.Empty(t, read(t, root, "src/cart.ts"))
}

const tsMoney = "export function plus(a: number, b: number): number {\n  return a + b;\n}\n"

const tsCart = "import { plus } from './money';\nimport { plus as add } from './money';\n\nexport class Cart {\n  plus(n: number): number {\n    return n;\n  }\n}\n\nexport function total(c: Cart, x: any): number {\n  return plus(add(1, 2), c.plus(3)) + x.plus(4);\n}\n"

// tsTable usa `plus` como chave de objeto: não é referência, e o rename
// precisa dizer que viu e não mexeu.
const tsTable = "export function table(): Record<string, number> {\n  const ops = { plus: 1 };\n  return ops;\n}\n"

func TestRename(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		file    string
		symbol  string
		newName string
		want    map[string]string
		updated int
		checked int
		skipped []string
		notes   []string
	}{
		{
			name:  "typescript: homonym method and alias survive, unresolved use is listed",
			files: map[string]string{"src/money.ts": tsMoney, "src/cart.ts": tsCart, "src/table.ts": tsTable},
			file:  "src/money.ts", symbol: "plus", newName: "sum",
			want: map[string]string{
				"src/table.ts": tsTable,
				"src/money.ts": strings.Replace(tsMoney, "plus", "sum", 1),
				"src/cart.ts":  "import { sum } from './money';\nimport { sum as add } from './money';\n\nexport class Cart {\n  plus(n: number): number {\n    return n;\n  }\n}\n\nexport function total(c: Cart, x: any): number {\n  return sum(add(1, 2), c.plus(3)) + x.plus(4);\n}\n",
			},
			updated: 4, checked: 3,
			skipped: []string{"src/cart.ts:11 unresolved: may be this symbol, left untouched"},
			notes: []string{"1 uses of plus resolve to other symbols or libraries and were left alone",
				"1 line(s) use plus as an identifier the index has no reference for, left untouched: src/table.ts:2"},
		},
		{
			name: "java: the class, its constructor, types, new expressions and static access",
			files: map[string]string{
				"src/main/java/com/acme/Money.java": "package com.acme;\n\npublic class Money {\n    public static final Money ZERO = new Money(0);\n    private final long cents;\n\n    public Money(long cents) {\n        this.cents = cents;\n    }\n\n    public Money plus(Money other) {\n        return new Money(cents + other.cents);\n    }\n}\n",
				"src/main/java/com/acme/Cart.java":  "package com.acme;\n\npublic class Cart {\n    Money total = new Money(0);\n    Money empty = Money.ZERO;\n}\n",
			},
			file: "src/main/java/com/acme/Money.java", symbol: "Money", newName: "Amount",
			want: map[string]string{
				"src/main/java/com/acme/Money.java": "package com.acme;\n\npublic class Amount {\n    public static final Amount ZERO = new Amount(0);\n    private final long cents;\n\n    public Amount(long cents) {\n        this.cents = cents;\n    }\n\n    public Amount plus(Amount other) {\n        return new Amount(cents + other.cents);\n    }\n}\n",
				"src/main/java/com/acme/Cart.java":  "package com.acme;\n\npublic class Cart {\n    Amount total = new Amount(0);\n    Amount empty = Amount.ZERO;\n}\n",
			},
			updated: 7, checked: 6,
			notes: []string{"java: the file must be renamed to Amount.java too"},
		},
		{
			name: "typescript: static member called through the class name",
			files: map[string]string{
				"src/money.ts": "export class Money {\n  static zero(): Money {\n    return new Money();\n  }\n}\n",
				"src/use.ts":   "import { Money } from './money';\n\nexport const empty: Money = Money.zero();\n",
			},
			file: "src/money.ts", symbol: "Money", newName: "Amount",
			want: map[string]string{
				"src/money.ts": "export class Amount {\n  static zero(): Amount {\n    return new Amount();\n  }\n}\n",
				"src/use.ts":   "import { Amount } from './money';\n\nexport const empty: Amount = Amount.zero();\n",
			},
			updated: 5, checked: 5,
		},
		{
			name: "python: qualified references, the string keeps its text",
			files: map[string]string{
				"app/__init__.py": "",
				"app/money.py":    "class Money:\n    pass\n",
				"app/cart.py":     "from . import money\n\n\ndef pair(a: money.Money, b: money.Money) -> None:\n    pass\n\n\ndef label(a: money.Money, text=\"Money\") -> str:\n    return text\n",
			},
			file: "app/money.py", symbol: "Money", newName: "Amount",
			want: map[string]string{
				"app/money.py": "class Amount:\n    pass\n",
				"app/cart.py":  "from . import money\n\n\ndef pair(a: money.Amount, b: money.Amount) -> None:\n    pass\n\n\ndef label(a: money.Amount, text=\"Money\") -> str:\n    return text\n",
			},
			updated: 3, checked: 2,
			notes: []string{"1 mention(s) of Money in comments, strings or text files were not changed: app/cart.py:8"},
		},
		{
			name: "go: receiver, parameters, results, literal and package-qualified types",
			files: map[string]string{
				"go.mod":           "module example.com/shop\n\ngo 1.22\n",
				"pricing/money.go": "package pricing\n\n// Money is an amount in cents.\ntype Money struct{ Cents int64 }\n\nfunc (m Money) Plus(o Money) Money { return Money{Cents: m.Cents + o.Cents} }\n",
				"order/order.go":   "package order\n\nimport \"example.com/shop/pricing\"\n\ntype Line struct {\n\tPrice pricing.Money\n}\n\nfunc Sum(a, b pricing.Money) pricing.Money { return a.Plus(b) }\n",
			},
			file: "pricing/money.go", symbol: "Money", newName: "Amount",
			want: map[string]string{
				"pricing/money.go": "package pricing\n\n// Money is an amount in cents.\ntype Amount struct{ Cents int64 }\n\nfunc (m Amount) Plus(o Amount) Amount { return Amount{Cents: m.Cents + o.Cents} }\n",
				"order/order.go":   "package order\n\nimport \"example.com/shop/pricing\"\n\ntype Line struct {\n\tPrice pricing.Amount\n}\n\nfunc Sum(a, b pricing.Amount) pricing.Amount { return a.Plus(b) }\n",
			},
			updated: 4, checked: 3,
			notes: []string{"1 mention(s) of Money in comments, strings or text files were not changed: pricing/money.go:3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, st := repoWith(t, tc.files)
			res := apply(t, root, st, editor.Request{Action: editor.Rename, File: tc.file, Symbol: tc.symbol, Text: tc.newName})
			for rel, content := range tc.want {
				assert.Equal(t, content, read(t, root, rel), rel)
			}
			assert.Len(t, res.Updated, tc.updated)
			assert.Equal(t, tc.checked, res.Checked, "renamed references re-checked after reindexing")
			assert.Empty(t, res.Dangling, "every renamed reference resolves again")
			var skipped []string
			for _, s := range res.Skipped {
				skipped = append(skipped, fmt.Sprintf("%s:%d %s", s.File, s.Line, s.Reason))
			}
			assert.Equal(t, tc.skipped, skipped)
			notes := strings.Join(res.Notes, "\n")
			for _, n := range tc.notes {
				assert.Contains(t, notes, n)
			}
		})
	}
}

func TestOverloadByLine(t *testing.T) {
	const shelf = "package p;\n\npublic class Shelf {\n    public String get(int index) {\n        return \"\";\n    }\n\n    public String get(String name) {\n        return name;\n    }\n\n    public String first() {\n        return get(0) + get(\"x\");\n    }\n}\n"
	const file = "src/p/Shelf.java"
	root, st := repoWith(t, map[string]string{file: shelf})
	_, err := editor.Apply(root, st, editor.Request{Action: editor.Rename, File: file, Symbol: "Shelf.get", Text: "at"})
	assert.ErrorContains(t, err, "symbol Shelf.get is ambiguous in this file: Shelf.get:4 (4-6), Shelf.get:8 (8-10); pass one of these")
	_, err = editor.Apply(root, st, editor.Request{Action: editor.Rename, File: file, Symbol: "p.Shelf.get", Text: "at"})
	assert.ErrorContains(t, err, "is ambiguous", "a qualified name shared by overloads never picks the first one silently")
	_, err = editor.Apply(root, st, editor.Request{Action: editor.Rename, File: file, Symbol: "Shelf.get:11", Text: "at"})
	assert.ErrorContains(t, err, "symbol Shelf.get not found at line 11")

	res := apply(t, root, st, editor.Request{Action: editor.Rename, File: file, Symbol: "Shelf.get:5", Text: "at"})
	want := strings.Replace(strings.Replace(shelf, "String get(int index)", "String at(int index)", 1), "get(0)", "at(0)", 1)
	assert.Equal(t, want, read(t, root, file), "only the int overload and its call change")
	assert.Equal(t, 1, res.Checked)
	assert.Contains(t, strings.Join(res.Notes, "\n"), "1 uses of get resolve to other symbols or libraries and were left alone")
}

func TestRenameFollowsReexports(t *testing.T) {
	root, st := repoWith(t, map[string]string{
		"src/money.ts": tsMoney,
		"src/index.ts": "export { plus } from './money';\nexport { plus as add } from './money';\n",
		"src/use.ts":   "import { plus } from './index';\n\nexport const three = plus(1, 2);\n",
	})
	res := apply(t, root, st, editor.Request{Action: editor.Rename, File: "src/money.ts", Symbol: "plus", Text: "sum"})
	assert.Equal(t, "export { sum } from './money';\nexport { sum as add } from './money';\n", read(t, root, "src/index.ts"),
		"re-exports have no refs, only import rows; the path './money' is never touched")
	assert.Equal(t, "import { sum } from './index';\n\nexport const three = sum(1, 2);\n", read(t, root, "src/use.ts"),
		"uses through the barrel resolve to the symbol")
	assert.Contains(t, strings.Join(res.Notes, "\n"), "plus is re-exported under its own name by src/index.ts: that public name changes too")
	assert.Empty(t, res.Dangling)
}

func TestRenameDefaultExport(t *testing.T) {
	root, st := repoWith(t, map[string]string{
		"src/isKey.ts":   "export default function isKey(value: string): boolean {\n  return value.length > 1 ? isKey(value.slice(1)) : true;\n}\n",
		"src/use.ts":     "import isKey from './isKey';\n\nexport const ok = isKey('a');\n",
		"src/anon.ts":    "export default (value: string) => value.length;\n",
		"src/useAnon.ts": "import anon from './anon';\n\nexport const n = anon('a');\n",
	})
	res := apply(t, root, st, editor.Request{Action: editor.Rename, File: "src/isKey.ts", Symbol: "isKey", Text: "isPathKey"})
	assert.Equal(t, "export default function isPathKey(value: string): boolean {\n  return value.length > 1 ? isPathKey(value.slice(1)) : true;\n}\n",
		read(t, root, "src/isKey.ts"))
	assert.Equal(t, "import isKey from './isKey';\n\nexport const ok = isKey('a');\n", read(t, root, "src/use.ts"), "the importer's local name stays valid")
	assert.Contains(t, strings.Join(res.Notes, "\n"), "2 use(s) in 1 file(s) reach isKey through a default import under their own local name and were left as they are")
	assert.Empty(t, res.Dangling)

	_, err := editor.Apply(root, st, editor.Request{Action: editor.Rename, File: "src/anon.ts", Symbol: "anon", Text: "size"})
	assert.ErrorContains(t, err, "anon is an anonymous default export named after its file (src/anon.ts): rename the file and its imports instead")
}

func TestLargePreviewStaysShort(t *testing.T) {
	var many strings.Builder
	many.WriteString("import { plus } from './money';\n\nexport function many(): number {\n  let n = 0;\n")
	for i := 0; i < 45; i++ {
		many.WriteString("  n = plus(n, 1);\n")
	}
	many.WriteString("  return n;\n}\n")
	root, st := repoWith(t, map[string]string{"src/money.ts": tsMoney, "src/many.ts": many.String()})
	res, err := editor.Apply(root, st, editor.Request{Action: editor.Rename, File: "src/money.ts", Symbol: "plus", Text: "sum", Preview: true})
	require.NoError(t, err)
	assert.Len(t, res.Updated, 47, "declaration, import and 45 calls")
	assert.Contains(t, res.Diff, "  … 43 more changes in this file\n")
	assert.Less(t, len(res.Diff), 1200, "a large preview shows where changes land, not every line")
}

func TestPreviewWritesNothing(t *testing.T) {
	root, st := repoWith(t, map[string]string{"src/money.ts": tsMoney, "src/cart.ts": tsCart})
	res, err := editor.Apply(root, st, editor.Request{Action: editor.Rename, File: "src/money.ts", Symbol: "plus", Text: "sum", Preview: true})
	require.NoError(t, err)
	assert.True(t, res.Preview)
	assert.Equal(t, tsCart, read(t, root, "src/cart.ts"))
	assert.Equal(t, tsMoney, read(t, root, "src/money.ts"))
	assert.True(t, strings.HasPrefix(res.Diff, "src/money.ts\n  -   1| export function plus(a: number, b: number): number {\n  +   1| export function sum(a: number, b: number): number {\n"), res.Diff)
	assert.Contains(t, res.Diff, "src/cart.ts\n  -   1| import { plus } from './money';\n  +   1| import { sum } from './money';\n")
	assert.Len(t, res.Updated, 4, "the preview lists the lines the rename would touch")

	del, err := editor.Apply(root, st, editor.Request{Action: editor.Delete, File: "src/cart.ts", Symbol: "total", Preview: true})
	require.NoError(t, err)
	assert.Equal(t, "src/cart.ts\n  @@ delete 9-12\n  -   9| \n  -  10| export function total(c: Cart, x: any): number {\n  -  11|   return plus(add(1, 2), c.plus(3)) + x.plus(4);\n  -  12| }\n", del.Diff)
	assert.Equal(t, tsCart, read(t, root, "src/cart.ts"))
}
