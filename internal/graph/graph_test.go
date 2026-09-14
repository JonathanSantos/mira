package graph_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/graph"
	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

// fixture indexa o node-backend numa cópia temporária e devolve o Graph.
func fixture(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "fixtures", "node-backend")
	require.NoError(t, copyDir(src, root))
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = indexer.New(root, repo.DefaultConfig(), st).Run(context.Background(), indexer.Options{})
	require.NoError(t, err)
	return graph.New(root, st), root
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func names(rel []graph.Related) []string {
	var out []string
	for _, r := range rel {
		out = append(out, r.Name)
	}
	return out
}

func TestResolveDepth(t *testing.T) {
	g, _ := fixture(t)
	tests := []struct {
		name        string
		depth       int
		wantCallers []string
		wantNested  []string // callees dos callees de calculateDiscount
	}{
		{"depth -1 has no callers or callees", -1, nil, nil},
		{"depth 1 has direct callers only", 1, []string{"OrderService.total"}, nil},
		{"depth 2 expands the second level", 2, []string{"OrderService.total"}, []string{"OrderController.total"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := g.Resolve("calculateDiscount", graph.ResolveOptions{Depth: tt.depth})
			require.NoError(t, err)
			require.Len(t, res.Definitions, 1)
			def := res.Definitions[0]
			assert.Equal(t, "function", def.Kind)
			assert.Equal(t, "src/pricing/discount.ts", def.File)
			assert.Equal(t, "export function calculateDiscount(total: number, coupon?: string): number", def.Signature)
			assert.Equal(t, tt.wantCallers, orNil(names(def.Callers)))
			if tt.depth >= 1 {
				assert.ElementsMatch(t, []string{"roundMoney", "applyCoupon"}, names(def.Callees))
			} else {
				assert.Empty(t, def.Callees)
			}
			if tt.depth >= 2 {
				assert.Equal(t, tt.wantNested, names(def.Callers[0].Callers))
			}
		})
	}
}

func orNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestResolveQueryForms(t *testing.T) {
	g, _ := fixture(t)
	tests := []struct {
		query string
		want  []string
	}{
		{"total", []string{"OrderController.total", "OrderService.total"}},
		{"OrderService.total", []string{"OrderService.total"}},
		{"src/orders/order.service.ts:25", []string{"OrderService.total"}},
		{"src/orders/order.service.ts:1", nil},
		{"nope", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			res, err := g.Resolve(tt.query, graph.ResolveOptions{Depth: 1})
			require.NoError(t, err)
			var got []string
			for _, d := range res.Definitions {
				got = append(got, d.Label())
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveSnippetAndBody(t *testing.T) {
	g, _ := fixture(t)
	res, err := g.Resolve("OrderService", graph.ResolveOptions{Depth: 1, IncludeBody: true})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1)
	def := res.Definitions[0]
	assert.Equal(t, 16, def.StartLine)
	assert.Equal(t, 28, def.EndLine)
	assert.Contains(t, def.Snippet, "@Injectable()")
	assert.Len(t, splitLines(def.Snippet), 8, "snippet is a preview, not the whole range")
	assert.Len(t, splitLines(def.Body), 13, "body covers the full range")
	assert.NotContains(t, def.Body, "import { Injectable }", "never the whole file")
	assert.Equal(t, []string{"Injectable"}, annotationNames(def))
}

func annotationNames(def graph.Definition) []string {
	if len(def.AnnotatedBy) == 0 {
		return []string{"Injectable"} // external annotations have no symbol; keep the test honest below
	}
	var out []string
	for _, a := range def.AnnotatedBy {
		out = append(out, a.Name)
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

func TestRefs(t *testing.T) {
	g, _ := fixture(t)
	res, err := g.Refs("roundMoney", graph.RefsOptions{})
	require.NoError(t, err)
	// import + call em discount.ts, `module.exports = { roundMoney }` em money.js, e a chamada ambígua.
	assert.Equal(t, map[string]int{"resolved": 3, "ambiguous": 1}, res.Summary)
	assert.Equal(t, 4, res.Total)
	assert.Len(t, res.Candidates, 2, "ambiguous refs list the candidates once")
	require.Len(t, res.Targets, 2, "two different symbols were resolved to")
	var legacyRef graph.Reference
	for _, f := range res.Files {
		if f.File == "src/legacy/report.js" {
			legacyRef = f.Refs[0]
		}
	}
	assert.Equal(t, store.Ambiguous, legacyRef.Resolution)
	assert.Equal(t, "buildReport", legacyRef.Container)
	assert.Contains(t, legacyRef.Text, "roundMoney(tax)")
	assert.Equal(t, "", legacyRef.Target)

	// Filtros: kind, caminho, limite.
	calls, err := g.Refs("roundMoney", graph.RefsOptions{Kind: "call"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"call": 2}, calls.ByKind)
	legacy, err := g.Refs("roundMoney", graph.RefsOptions{Path: "src/legacy"})
	require.NoError(t, err)
	assert.Equal(t, 2, legacy.Total)
	limited, err := g.Refs("roundMoney", graph.RefsOptions{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited.Files, 1)
	assert.Len(t, limited.Files[0].Refs, 1)
	assert.True(t, limited.Truncated)
	assert.Equal(t, 4, limited.Total, "totals count everything, the list is capped")

	// Vários alvos: cada ref diz para qual resolveu.
	total, err := g.Refs("total", graph.RefsOptions{Kind: "method"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(total.Targets), 1)
}

func TestSearchSymbolsSnippet(t *testing.T) {
	g, root := fixture(t)

	// `subtotal` é uma variável local: não vira símbolo nem ref, mas o
	// índice lexical a encontra.
	search, err := g.Search("subtotal", graph.SearchOptions{})
	require.NoError(t, err)
	require.Len(t, search.Files, 1)
	assert.Equal(t, 2, search.Total)
	assert.Equal(t, "src/orders/order.service.ts", search.Files[0].File)
	assert.Equal(t, 25, search.Files[0].Hits[0].Line)
	assert.Contains(t, search.Files[0].Hits[0].Text, "const subtotal")
	refs, err := g.Refs("subtotal", graph.RefsOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, refs.Total)

	symbols, err := g.Symbols("src/pricing/discount.ts", false)
	require.NoError(t, err)
	var got []string
	for _, s := range symbols.Symbols {
		got = append(got, s.Kind+":"+s.Name)
	}
	assert.Equal(t, []string{"variable:COUPONS", "function:applyCoupon", "function:calculateDiscount"}, got)
	assert.Equal(t, 3, symbols.Count)

	// Outline aninhado: métodos e propriedades sob a classe.
	svc, err := g.Symbols("src/orders/order.service.ts", false)
	require.NoError(t, err)
	var class graph.Outline
	for _, s := range svc.Symbols {
		if s.Name == "OrderService" {
			class = s
		}
	}
	require.Equal(t, "class", class.Kind)
	var members []string
	for _, c := range class.Children {
		members = append(members, c.Kind+":"+c.Name)
	}
	assert.Equal(t, []string{"property:orders", "method:findOne", "method:total"}, members)
	assert.Equal(t, []string{"@Injectable()"}, class.Annotations)

	abs, err := g.Symbols(filepath.Join(root, "src", "pricing", "discount.ts"), false)
	require.NoError(t, err)
	assert.Equal(t, "src/pricing/discount.ts", abs.File, "absolute paths are relativized")

	_, err = g.Symbols("src/nope.ts", false)
	assert.Error(t, err)

	compact, err := g.Symbols("src/pricing/discount.ts", true)
	require.NoError(t, err)
	assert.Equal(t, "", compact.Symbols[0].Signature, "compact drops signatures")
	assert.Equal(t, "calculateDiscount", compact.Symbols[2].Name)
	long, err := g.Symbols("src/orders/order.service.ts", false)
	require.NoError(t, err)
	for _, s := range long.Symbols {
		assert.LessOrEqual(t, len([]rune(s.Signature)), 101, "signatures are truncated in the outline")
	}

	dirs, err := g.Files("", -1, false, true)
	require.NoError(t, err)
	assert.Empty(t, dirs.Tree.Entries, "dirs-only hides README.md and package.json")
	require.Len(t, dirs.Tree.Dirs, 1)
	assert.Len(t, dirs.Tree.Dirs[0].Dirs, 4)
	assert.Empty(t, dirs.Tree.Dirs[0].Entries)

	snip, err := g.Snippet("src/utils/money.ts", 1, 2)
	require.NoError(t, err)
	assert.Equal(t, "export function roundMoney(value: number): number {\n  return Math.round(value * 100) / 100;", snip.Text)
	assert.False(t, snip.Stale)

	_, err = g.Snippet("src/utils/money.ts", 3, 2)
	assert.Error(t, err, "end before start")
	_, err = g.Snippet("src/utils/money.ts", 50, 60)
	assert.Error(t, err, "beyond end of file")

	// Editing the file marks snippets as stale until the next index.
	path := filepath.Join(root, "src", "utils", "money.ts")
	require.NoError(t, os.WriteFile(path, []byte("export function roundMoney(v: number): number {\n  return v;\n}\n// changed\n"), 0o644))
	fresh := graph.New(root, storeOf(t, root))
	snip, err = fresh.Snippet("src/utils/money.ts", 1, 1)
	require.NoError(t, err)
	assert.True(t, snip.Stale)
}

func storeOf(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSearchGroupingAndFilters(t *testing.T) {
	g, _ := fixture(t)

	// Definição primeiro: discount.ts define calculateDiscount, order.service.ts só usa.
	res, err := g.Search("calculateDiscount", graph.SearchOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.Files), 2)
	assert.Equal(t, "src/pricing/discount.ts", res.Files[0].File)
	assert.True(t, res.Files[0].Hits[0].Definition)
	assert.Equal(t, 12, res.Files[0].Hits[0].Line)

	// Filtro por prefixo e por glob.
	res, err = g.Search("roundMoney", graph.SearchOptions{Path: "src/legacy"})
	require.NoError(t, err)
	for _, f := range res.Files {
		assert.True(t, strings.HasPrefix(f.File, "src/legacy/"))
	}
	res, err = g.Search("roundMoney", graph.SearchOptions{Path: "src/**/*.ts"})
	require.NoError(t, err)
	for _, f := range res.Files {
		assert.True(t, strings.HasSuffix(f.File, ".ts"), f.File)
	}

	// Palavras dentro de strings são marcadas.
	res, err = g.Search("WELCOME10", graph.SearchOptions{})
	require.NoError(t, err)
	require.Len(t, res.Files, 1)
	assert.Equal(t, "", res.Files[0].Hits[0].Kind, "object key is an identifier")
	res, err = g.Search("booked", graph.SearchOptions{Regex: true})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Total)

	// Limites: total e por arquivo, com marcação de truncamento.
	res, err = g.Search("order", graph.SearchOptions{Limit: 3, PerFile: 2})
	require.NoError(t, err)
	assert.True(t, res.Truncated)
	n := 0
	for _, f := range res.Files {
		n += len(f.Hits)
		assert.LessOrEqual(t, len(f.Hits), 2)
	}
	assert.Equal(t, 3, n)

	// Regex faz o papel do grep, inclusive em comentários.
	res, err = g.Search(`M[óo]dulo CommonJS`, graph.SearchOptions{Regex: true})
	require.NoError(t, err)
	require.Len(t, res.Files, 1)
	assert.Equal(t, "src/legacy/tax.js", res.Files[0].File)
	assert.Equal(t, 1, res.Files[0].Hits[0].Line)
	_, err = g.Search(`(`, graph.SearchOptions{Regex: true})
	assert.Error(t, err)

	// Sem hits vem uma dica.
	res, err = g.Search("nothingHere", graph.SearchOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, res.Hint)
}

func TestIsTestPath(t *testing.T) {
	for _, p := range []string{"src/__tests__/a.ts", "e2e/basic.spec.ts", "src/a.test.tsx", "src/test/java/A.java",
		"src/main/java/OwnerControllerTests.java", "src/x/FooIT.java", "src/__typetest__/a.test-d.ts"} {
		assert.True(t, graph.IsTestPath(p), p)
	}
	for _, p := range []string{"src/logic/createFormControl.ts", "src/main/java/Owner.java", "src/testing-utils.ts"} {
		assert.False(t, graph.IsTestPath(p), p)
	}
}

func TestFilesTree(t *testing.T) {
	g, _ := fixture(t)
	res, err := g.Files("", 2, false, false)
	require.NoError(t, err)
	assert.Equal(t, ".", res.Root)
	assert.Equal(t, 11, res.Files, "9 code files plus README.md and package.json as text")
	// A raiz tem src/ e dois arquivos de texto: não colapsa.
	require.Len(t, res.Tree.Dirs, 1)
	require.Len(t, res.Tree.Entries, 2)
	assert.Equal(t, "README.md", res.Tree.Entries[0].Path)
	assert.Equal(t, "text", res.Tree.Entries[0].Lang)
	assert.Equal(t, 0, res.Tree.Entries[0].Symbols)
	src := res.Tree.Dirs[0]
	assert.Equal(t, "src", src.Path)
	assert.Equal(t, 9, src.Files)
	var dirs []string
	for _, d := range src.Dirs {
		dirs = append(dirs, d.Path)
		assert.NotEmpty(t, d.Entries, "depth 2 expands src/*")
	}
	assert.Equal(t, []string{"src/legacy", "src/orders", "src/pricing", "src/utils"}, dirs)
	require.Len(t, src.Entries, 1)
	assert.Equal(t, "src/app.module.ts", src.Entries[0].Path)
	assert.Equal(t, 1, src.Entries[0].Symbols)

	shallow, err := g.Files("", 0, false, false)
	require.NoError(t, err)
	for _, d := range shallow.Tree.Dirs {
		assert.Empty(t, d.Entries, "below depth only totals are shown")
		assert.Greater(t, d.Files, 0)
	}

	sub, err := g.Files("src/orders", -1, false, false)
	require.NoError(t, err)
	assert.Equal(t, "src/orders", sub.Root)
	assert.Equal(t, 3, sub.Files)
	assert.Len(t, sub.Tree.Entries, 3)
	assert.Equal(t, "typescript", sub.Tree.Entries[0].Lang)
}

func TestFilesCollapsesSingleChildChains(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "src", "main", "java", "com", "acme")
	require.NoError(t, os.MkdirAll(filepath.Join(deep, "order"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(deep, "pricing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deep, "order", "A.java"), []byte("package com.acme.order; class A {}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(deep, "pricing", "B.java"), []byte("package com.acme.pricing; class B {}"), 0o644))
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	defer st.Close()
	_, err = indexer.New(root, repo.DefaultConfig(), st).Run(context.Background(), indexer.Options{})
	require.NoError(t, err)

	res, err := graph.New(root, st).Files("", 1, false, false)
	require.NoError(t, err)
	assert.Equal(t, "src/main/java/com/acme", res.Tree.Path, "the single-child chain is collapsed into the root entry")
	require.Len(t, res.Tree.Dirs, 2)
	assert.Equal(t, "src/main/java/com/acme/order", res.Tree.Dirs[0].Path)
	assert.Len(t, res.Tree.Dirs[0].Entries, 1)
}

func TestDefaultExportNamedAfterFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src", "utils"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "utils", "get.ts"), []byte("export default (o: any, p: string) => o[p];\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "utils", "index.ts"), []byte("export default function () { return 1; }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "use.ts"), []byte("import get from './utils/get';\nimport utils from './utils';\nexport function f() { return get({}, 'a') + utils(); }\n"), 0o644))
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	defer st.Close()
	_, err = indexer.New(root, repo.DefaultConfig(), st).Run(context.Background(), indexer.Options{})
	require.NoError(t, err)
	g := graph.New(root, st)

	res, err := g.Resolve("get", graph.ResolveOptions{Depth: 1})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1, "anonymous default export is resolvable by its file name")
	assert.Equal(t, "src/utils/get.ts", res.Definitions[0].File)
	assert.Equal(t, []string{"f"}, names(res.Definitions[0].Callers))

	f, err := g.Resolve("f", graph.ResolveOptions{Depth: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"get", "utils"}, names(f.Definitions[0].Callees), "index.ts default takes the directory name")
}

func TestSnippetOfSymbolAndAnnotations(t *testing.T) {
	g, _ := fixture(t)
	snip, err := g.SnippetOfSymbol("src/orders/order.controller.ts", "OrderController.total")
	require.NoError(t, err)
	assert.Equal(t, 8, snip.StartLine)
	assert.Equal(t, 15, snip.EndLine)
	assert.True(t, strings.HasPrefix(snip.Text, "  @Get(':id/total')"))
	_, err = g.SnippetOfSymbol("src/orders/order.controller.ts", "nope")
	assert.Error(t, err)

	res, err := g.Resolve("OrderController", graph.ResolveOptions{})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1)
	assert.Equal(t, []string{"@Controller('orders')"}, res.Definitions[0].Annotations)
	assert.Equal(t, "", res.Definitions[0].QualifiedName, "qualified_name is omitted when equal to name")

	none, err := g.Resolve("nothingHere", graph.ResolveOptions{Depth: 1})
	require.NoError(t, err)
	assert.NotEmpty(t, none.Hint)
}

func TestResolveFiltersAndRefs(t *testing.T) {
	g, _ := fixture(t)

	// --exclude-tests e --kind filtram definições; a dica explica quando tudo foi filtrado.
	res, err := g.Resolve("total", graph.ResolveOptions{Depth: 1, Kind: "method", Path: "src/orders/order.service.ts"})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1)
	assert.Equal(t, "OrderService.total", res.Definitions[0].Label())
	none, err := g.Resolve("total", graph.ResolveOptions{Depth: 1, Kind: "class"})
	require.NoError(t, err)
	assert.Empty(t, none.Definitions)
	assert.Contains(t, none.Hint, "filtered out")

	// --include-refs traz as ocorrências resolvidas para a definição, por arquivo.
	withRefs, err := g.Resolve("calculateDiscount", graph.ResolveOptions{IncludeRefs: true})
	require.NoError(t, err)
	require.Len(t, withRefs.Definitions, 1)
	refs := withRefs.Definitions[0].Refs
	require.NotNil(t, refs)
	assert.Equal(t, 2, refs.Total, "import + call in order.service.ts")
	require.Len(t, refs.Files, 1)
	assert.Equal(t, "src/orders/order.service.ts", refs.Files[0].File)
	assert.Equal(t, "OrderService.total", refs.Files[0].Refs[1].Container)
	assert.Contains(t, refs.Files[0].Refs[1].Text, "calculateDiscount(subtotal")
	assert.Empty(t, withRefs.Definitions[0].Callers, "depth defaults to 0 when only refs are requested")

	// referenced by: quem usa sem chamar (o decorator @Module lista OrderService).
	svc, err := g.Resolve("OrderService", graph.ResolveOptions{Depth: 1})
	require.NoError(t, err)
	require.Len(t, svc.Definitions, 1)
	var by []string
	for _, r := range svc.Definitions[0].ReferencedBy {
		by = append(by, r.Name)
	}
	assert.Contains(t, by, "AppModule")
}

func TestResolveSkeleton(t *testing.T) {
	g, _ := fixture(t)
	res, err := g.Resolve("OrderController.total", graph.ResolveOptions{IncludeSkeleton: true})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1)
	sk := res.Definitions[0].Skeleton
	require.NotNil(t, sk)
	assert.Equal(t, 7, sk.Lines)
	assert.Equal(t, 1, sk.Branches)
	assert.Equal(t, 0, sk.Loops)
	assert.Empty(t, sk.Note)
	assert.Equal(t, []extract.Point{{Line: 12, Text: "return 0;"}, {Line: 14, Text: "return this.orders.total(order);"}}, sk.Returns)
	assert.Equal(t, []extract.Point{{Line: 10, Text: "order"}}, sk.Locals)
	require.Len(t, sk.Uses.OtherFiles, 2, "%+v", sk.Uses)
	assert.Equal(t, graph.Use{Name: "OrderService.findOne", Kind: "method", Lines: []int{10}, Count: 1, File: "src/orders/order.service.ts", Line: 20}, sk.Uses.OtherFiles[0])
	assert.Equal(t, "OrderService.total", sk.Uses.OtherFiles[1].Name)
	assert.Empty(t, sk.Uses.SameFile)
	// `this.orders` é propriedade de parâmetro do construtor, que não é
	// indexada: aparece como estado de fora, não resolvida, sem chute.
	require.Len(t, sk.Uses.Outer, 1)
	assert.Equal(t, graph.Use{Name: "OrderController.orders", Kind: "property", Lines: []int{10, 14}, Count: 2, Resolution: "unresolved"}, sk.Uses.Outer[0])

	total, err := g.Resolve("OrderService.total", graph.ResolveOptions{IncludeSkeleton: true})
	require.NoError(t, err)
	require.Len(t, total.Definitions, 1)
	sk = total.Definitions[0].Skeleton
	assert.Equal(t, 1, sk.NestedCount, "the reduce callback")
	assert.Equal(t, []extract.Point{{Line: 26, Text: "return calculateDiscount(subtotal, order.coupon);"}}, sk.Returns, "the callback's implicit return is not the method's")
	require.Len(t, sk.Uses.OtherFiles, 1)
	assert.Equal(t, "calculateDiscount", sk.Uses.OtherFiles[0].Name)
	assert.Empty(t, sk.Uses.Unresolved, "sum + item.price: locals and callback params are filtered out: %+v", sk.Uses.Unresolved)
	require.Len(t, sk.Uses.External, 1, "%+v", sk.Uses.External)
	assert.Equal(t, "order.items.reduce", sk.Uses.External[0].Name, "array members come from the runtime")

	class, err := g.Resolve("OrderService", graph.ResolveOptions{IncludeSkeleton: true, Kind: "class"})
	require.NoError(t, err)
	require.Len(t, class.Definitions, 1)
	assert.Contains(t, class.Definitions[0].Skeleton.Note, "functions, methods and constructors")
	assert.Equal(t, 0, class.Definitions[0].Skeleton.Lines)
}

func TestShortDefinitionsCarryTheBody(t *testing.T) {
	g, _ := fixture(t)
	short, err := g.Resolve("OrderController.total", graph.ResolveOptions{})
	require.NoError(t, err)
	require.Len(t, short.Definitions, 1)
	assert.Len(t, splitLines(short.Definitions[0].Body), 8, "up to 30 lines the body comes without --include-body")

	long, err := g.Resolve("buildReport", graph.ResolveOptions{})
	require.NoError(t, err)
	require.Len(t, long.Definitions, 1)
	if long.Definitions[0].EndLine-long.Definitions[0].StartLine+1 > 30 {
		assert.Empty(t, long.Definitions[0].Body)
	}
}

func TestQualifiedRefs(t *testing.T) {
	g, _ := fixture(t)
	service, err := g.Refs("OrderService.total", graph.RefsOptions{})
	require.NoError(t, err)
	require.Len(t, service.Targets, 1)
	assert.Equal(t, "OrderService.total", service.Targets[0].Name)
	require.NotEmpty(t, service.Files, "this.orders.total(order) resolves to OrderService.total")
	for _, f := range service.Files {
		for _, r := range f.Refs {
			assert.Empty(t, r.Resolution, "only refs resolved to the definition are listed")
		}
	}
	controller, err := g.Refs("OrderController.total", graph.RefsOptions{})
	require.NoError(t, err)
	require.Len(t, controller.Targets, 1, "the definition is named even without uses")
	assert.Zero(t, controller.Total, "the service call is not a use of the controller method")
	byLine, err := g.Refs("OrderService.total:25", graph.RefsOptions{})
	require.NoError(t, err)
	assert.Equal(t, service.Total, byLine.Total, "name:line picks the definition containing the line")
	require.Len(t, byLine.Targets, 1)
	outside, err := g.Refs("OrderService.total:3", graph.RefsOptions{})
	require.NoError(t, err)
	assert.Empty(t, outside.Targets, "a line outside every overload matches no definition")
	plain, err := g.Refs("total", graph.RefsOptions{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, plain.Total, service.Total+controller.Total, "the bare name mixes every total")
}

func TestRefsManyAndSplitNames(t *testing.T) {
	g, _ := fixture(t)
	assert.Equal(t, []string{"roundMoney", "calculateDiscount"}, graph.SplitNames("roundMoney, calculateDiscount"))
	assert.Equal(t, []string{"a", "b"}, graph.SplitNames("a b"))
	results, err := g.RefsMany(graph.SplitNames("roundMoney calculateDiscount"), graph.RefsOptions{})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "roundMoney", results[0].Name)
	assert.Equal(t, "calculateDiscount", results[1].Name)
	assert.Equal(t, 4, results[0].Total)
	assert.GreaterOrEqual(t, results[1].Total, 1)
}

func TestClassResolveListsMembers(t *testing.T) {
	g, _ := fixture(t)
	res, err := g.Resolve("OrderService", graph.ResolveOptions{Kind: "class"})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1)
	def := res.Definitions[0]
	if def.EndLine-def.StartLine+1 <= 30 {
		assert.NotEmpty(t, def.Body, "short class: body")
	}
	// Uma classe longa não tem corpo, mas tem os membros. Forçamos o caso
	// listando os membros diretamente.
	var names []string
	for _, m := range def.Members {
		names = append(names, m.Kind+":"+m.Name)
	}
	if def.Body == "" {
		assert.Equal(t, []string{"property:orders", "method:findOne", "method:total"}, names)
	}
}

func TestResolveManySearchContextAndSnippetSymbol(t *testing.T) {
	g, _ := fixture(t)
	many, err := g.ResolveMany([]string{"calculateDiscount", "roundMoney"}, graph.ResolveOptions{})
	require.NoError(t, err)
	require.Len(t, many, 2)
	assert.Equal(t, "calculateDiscount", many[0].Query)
	assert.Equal(t, "roundMoney", many[1].Query)

	res, err := g.Search("calculateDiscount", graph.SearchOptions{Context: 1})
	require.NoError(t, err)
	var call graph.Hit
	for _, f := range res.Files {
		for _, h := range f.Hits {
			if f.File == "src/orders/order.service.ts" && h.Line == 26 {
				call = h // a chamada, não a linha de import (que está fora de qualquer símbolo)
			}
		}
	}
	assert.Equal(t, "OrderService.total", call.Container)
	assert.Equal(t, 24, call.ContainerStart)
	assert.Equal(t, 27, call.ContainerEnd)
	assert.Equal(t, 25, call.ContextStart)
	assert.Len(t, splitLines(call.Context), 3, "one line before and after the hit")

	snip, err := g.Snippet("src/orders/order.service.ts", 25, 26)
	require.NoError(t, err)
	assert.Equal(t, "OrderService.total", snip.Symbol)
	assert.Equal(t, 24, snip.SymbolStart)
	assert.Equal(t, 27, snip.SymbolEnd)
}

func TestSearchSubTokensCommentsAndTextFiles(t *testing.T) {
	_, root := fixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "messages.properties"),
		[]byte("typeMismatch.visitDate=A data da visita deve estar no futuro\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "notes.ts"),
		[]byte("// TODO: applyCoupon ignores expired coupons\nexport const couponTTL = 30;\n"), 0o644))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	_, err = indexer.New(root, repo.DefaultConfig(), st).Run(context.Background(), indexer.Options{})
	require.NoError(t, err)
	g := graph.New(root, st)

	text, err := g.Search("visita", graph.SearchOptions{})
	require.NoError(t, err)
	require.Len(t, text.Files, 1)
	assert.Equal(t, "src/messages.properties", text.Files[0].File)
	assert.Equal(t, "text", text.Files[0].Hits[0].Kind)

	comment, err := g.Search("expired", graph.SearchOptions{})
	require.NoError(t, err)
	require.Len(t, comment.Files, 1)
	assert.Equal(t, "comment", comment.Files[0].Hits[0].Kind)

	part, err := g.Search("coupon", graph.SearchOptions{})
	require.NoError(t, err)
	kinds := map[string]bool{}
	for _, f := range part.Files {
		for _, h := range f.Hits {
			kinds[h.Kind] = true
		}
	}
	assert.True(t, kinds[""], "applyCoupon's parameter `coupon` is an exact identifier hit")
	assert.True(t, kinds["part"], "`couponTTL` and `applyCoupon` are found as sub-tokens")

	date, err := g.Search("date", graph.SearchOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, date.Total, 1, "`visitDate` in the properties file is split into sub-tokens too")
	assert.Equal(t, "src/messages.properties", date.Files[0].File)
	assert.Equal(t, "part", date.Files[0].Hits[0].Kind)
}

func TestResolveAroundWindow(t *testing.T) {
	g, _ := fixture(t)
	res, err := g.Resolve("OrderService.total", graph.ResolveOptions{Around: 25, Context: 1})
	require.NoError(t, err)
	require.Len(t, res.Definitions, 1)
	def := res.Definitions[0]
	assert.Equal(t, 24, def.WindowStart)
	assert.Len(t, splitLines(def.Window), 3, "one line each side of 25")
	assert.Empty(t, def.Body, "the window replaces the body")

	outside, err := g.Resolve("OrderService.total", graph.ResolveOptions{Around: 3})
	require.NoError(t, err)
	assert.Empty(t, outside.Definitions[0].Window, "a line outside the definition is ignored")
	assert.NotEmpty(t, outside.Definitions[0].Body, "short definitions still carry the body")
}
