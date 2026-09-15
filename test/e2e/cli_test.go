package e2e

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestInitIsIdempotent(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			root := fixture(t, name)
			out := run(t, root, "init")
			assert.Contains(t, out, "initialized")
			assert.FileExists(t, filepath.Join(root, ".mira", "index.db"))
			assert.FileExists(t, filepath.Join(root, ".mira", "config.yaml"))
			cfg, err := os.ReadFile(filepath.Join(root, ".mira", "config.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(cfg), "typescript")

			// Segunda rodada não recria nem falha.
			require.NoError(t, os.WriteFile(filepath.Join(root, ".mira", "config.yaml"), []byte("languages: [java]\n"), 0o644))
			out = run(t, root, "init")
			assert.Contains(t, out, "already initialized")
			cfg, err = os.ReadFile(filepath.Join(root, ".mira", "config.yaml"))
			require.NoError(t, err)
			assert.Equal(t, "languages: [java]\n", string(cfg), "existing config is preserved")
		})
	}
}

func TestCommandsRequireInit(t *testing.T) {
	root := fixture(t, "ts-frontend")
	out, err := tryRun(root, "index")
	require.Error(t, err)
	assert.Contains(t, out, "mira init")
}

func TestIndexAndStatusCounts(t *testing.T) {
	tests := []struct {
		fixture string
		want    map[string]int // files por linguagem
		symbols int
	}{
		// "text" são README.md e package.json/pom.xml, indexados só por palavras.
		// Os símbolos incluem propriedades de interface (CardProps, ButtonProps, CartItem, Order, OrderItem)
		// e a função aninhada `add` em useCart.
		{"ts-frontend", map[string]int{"typescript": 8, "javascript": 1, "text": 2}, 20},
		{"node-backend", map[string]int{"typescript": 6, "javascript": 3, "text": 2}, 23},
		{"java-service", map[string]int{"java": 9, "text": 2}, 41},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			root := fixture(t, tt.fixture)
			run(t, root, "init")
			var report indexReport
			runJSON(t, root, &report, "index")
			assert.Equal(t, report.Walked, report.New)
			assert.Equal(t, []string{"walk", "plan", "index", "remove", "resolve"}, phaseNames(report))

			var st status
			runJSON(t, root, &st, "status")
			got := map[string]int{}
			for l, c := range st.ByLang {
				got[l] = c.Files
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.symbols, st.Symbols)
			assert.Equal(t, 0, st.Outdated)
			assert.Greater(t, st.Edges, 0)

			text := run(t, root, "status")
			assert.Contains(t, text, "outdated files: 0")
		})
	}
}

func phaseNames(r indexReport) []string {
	var out []string
	for _, p := range r.Phases {
		out = append(out, p.Name)
	}
	return out
}

func TestResolveCentralSymbols(t *testing.T) {
	tests := []struct {
		fixture   string
		query     string
		kind      string
		file      string
		signature string
		start     int
		end       int
		callers   []string
		callees   []string
	}{
		{
			"ts-frontend", "Button", "function", "src/components/Button.tsx",
			"export default function Button({ label, price, onClick }: ButtonProps)", 10, 18,
			[]string{"Checkout"}, []string{"formatMoney"},
		},
		{
			"node-backend", "calculateDiscount", "function", "src/pricing/discount.ts",
			"export function calculateDiscount(total: number, coupon?: string): number", 12, 14,
			[]string{"OrderService.total"}, []string{"applyCoupon", "roundMoney"},
		},
		{
			"java-service", "Discount.calculate", "method", "src/main/java/com/acme/pricing/Discount.java",
			"public static Money calculate(Money base, int percent)", 6, 11,
			[]string{"com.acme.order.DefaultOrderService.total", "com.acme.report.ReportService.discounted"},
			[]string{"com.acme.pricing.Money", "com.acme.pricing.Money.percent", "com.acme.pricing.Money.cents"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture+"/"+tt.query, func(t *testing.T) {
			root := indexed(t, tt.fixture)
			var res resolveResult
			runJSON(t, root, &res, "resolve", tt.query)
			require.Len(t, res.Definitions, 1)
			d := res.Definitions[0]
			assert.Equal(t, tt.kind, d.Kind)
			assert.Equal(t, tt.file, d.File)
			assert.Equal(t, tt.signature, d.Signature)
			assert.Equal(t, tt.start, d.StartLine)
			assert.Equal(t, tt.end, d.EndLine)
			assert.ElementsMatch(t, tt.callers, qualifiedNames(d.Callers))
			assert.ElementsMatch(t, tt.callees, qualifiedNames(d.Callees))
			assert.True(t, d.Exported)
			if tt.end-tt.start+1 <= 30 {
				assert.Len(t, strings.Split(d.Body, "\n"), tt.end-tt.start+1, "short definitions carry the body")
			} else {
				assert.Empty(t, d.Body)
			}

			whole, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tt.file)))
			require.NoError(t, err)
			assert.NotEqual(t, strings.TrimSpace(string(whole)), strings.TrimSpace(d.Snippet), "snippet is never the whole file")
			assert.LessOrEqual(t, len(strings.Split(d.Snippet, "\n")), 8)

			var full resolveResult
			runJSON(t, root, &full, "resolve", tt.query, "--include-body", "--depth", "2")
			body := full.Definitions[0].Body
			assert.Len(t, strings.Split(body, "\n"), tt.end-tt.start+1)
			assert.NotContains(t, body, "import ")
		})
	}
}

func qualifiedNames(rel []related) []string {
	out := []string{}
	for _, r := range rel {
		out = append(out, r.Name)
	}
	return out
}

func TestResolveListsEveryDefinition(t *testing.T) {
	root := indexed(t, "ts-frontend")
	var res resolveResult
	runJSON(t, root, &res, "resolve", "formatMoney")
	require.Len(t, res.Definitions, 2)
	files := []string{res.Definitions[0].File, res.Definitions[1].File}
	assert.ElementsMatch(t, []string{"src/utils/format.ts", "src/legacy/format.ts"}, files)

	text := run(t, root, "resolve", "nothing_here")
	assert.Contains(t, text, "no definition found")
}

func TestRefsMarkResolutionIncludingAmbiguous(t *testing.T) {
	tests := []struct {
		fixture    string
		name       string
		ambiguous  string // arquivo com a ref ambígua proposital
		candidates int
		resolved   int
	}{
		{"ts-frontend", "formatMoney", "src/legacy/widget.js", 2, 4},
		{"node-backend", "roundMoney", "src/legacy/report.js", 2, 3}, // import + call + `module.exports = { roundMoney }`
		{"java-service", "Money", "src/main/java/com/acme/report/LegacyBridge.java", 2, -1},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			root := indexed(t, tt.fixture)
			var res refsResult
			runJSON(t, root, &res, "refs", tt.name)
			var ambiguous []flatRef
			for _, r := range res.flat() {
				if r.Resolution == "ambiguous" {
					ambiguous = append(ambiguous, r)
					assert.Equal(t, tt.ambiguous, r.File)
					assert.Equal(t, "", r.Target)
				}
			}
			assert.NotEmpty(t, ambiguous)
			assert.Len(t, res.Candidates, tt.candidates, "candidates are listed once, not per ref")
			assert.NotEmpty(t, res.Targets)
			if tt.resolved >= 0 {
				assert.Equal(t, tt.resolved, res.Summary["resolved"])
			}
			assert.Equal(t, len(ambiguous), res.Summary["ambiguous"])
		})
	}
}

// indexedAt lê files.indexed_at direto do SQLite para checar a incrementalidade.
func indexedAt(t *testing.T, root string) map[string]int64 {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, ".mira", "index.db"))
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT path, indexed_at FROM files`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var path string
		var at int64
		require.NoError(t, rows.Scan(&path, &at))
		out[path] = at
	}
	return out
}

func TestIncrementalReindexTouchesOnlyChangedFile(t *testing.T) {
	root := indexed(t, "node-backend")
	before := indexedAt(t, root)

	// Sem mudanças: nada é reprocessado.
	var report indexReport
	runJSON(t, root, &report, "index")
	assert.Equal(t, 0, report.New+report.Changed+report.Rehashed+report.Removed)
	assert.Equal(t, before, indexedAt(t, root))

	// Mesmo conteúdo, mtime novo: só o fingerprint é atualizado.
	target := filepath.Join(root, "src", "pricing", "discount.ts")
	future := time.Now().Add(2 * time.Hour)
	require.NoError(t, os.Chtimes(target, future, future))
	runJSON(t, root, &report, "index")
	assert.Equal(t, 1, report.Rehashed)
	assert.Equal(t, 0, report.Changed)
	assert.Equal(t, before, indexedAt(t, root))

	// Conteúdo novo: só esse arquivo muda indexed_at; os dependentes são
	// re-resolvidos e continuam apontando para o símbolo novo.
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(data, []byte("\nexport function extra() {}\n")...), 0o644))
	runJSON(t, root, &report, "index")
	assert.Equal(t, 1, report.Changed)
	after := indexedAt(t, root)
	for path, at := range before {
		if path == "src/pricing/discount.ts" {
			assert.Greater(t, after[path], at)
			continue
		}
		assert.Equal(t, at, after[path], path)
	}
	var res resolveResult
	runJSON(t, root, &res, "resolve", "calculateDiscount")
	require.Len(t, res.Definitions, 1)
	assert.Equal(t, []string{"OrderService.total"}, qualifiedNames(res.Definitions[0].Callers), "dependent re-resolved to the new symbol id")

	var st status
	runJSON(t, root, &st, "status")
	assert.Equal(t, 0, st.Outdated)
}

func TestRemovedFileDisappearsFromEveryTable(t *testing.T) {
	root := indexed(t, "ts-frontend")
	require.NoError(t, os.Remove(filepath.Join(root, "src", "legacy", "format.ts")))

	var st status
	runJSON(t, root, &st, "status")
	assert.Equal(t, 1, st.Outdated)

	var report indexReport
	runJSON(t, root, &report, "index")
	assert.Equal(t, 1, report.Removed)

	db, err := sql.Open("sqlite", filepath.Join(root, ".mira", "index.db"))
	require.NoError(t, err)
	defer db.Close()
	for _, q := range []string{
		`SELECT COUNT(*) FROM files WHERE path = 'src/legacy/format.ts'`,
		`SELECT COUNT(*) FROM symbols WHERE file_id NOT IN (SELECT id FROM files)`,
		`SELECT COUNT(*) FROM refs WHERE file_id NOT IN (SELECT id FROM files)`,
		`SELECT COUNT(*) FROM imports WHERE file_id NOT IN (SELECT id FROM files)`,
		`SELECT COUNT(*) FROM words WHERE file_id NOT IN (SELECT id FROM files)`,
		`SELECT COUNT(*) FROM edges WHERE from_symbol_id NOT IN (SELECT id FROM symbols) OR to_symbol_id NOT IN (SELECT id FROM symbols)`,
	} {
		var n int
		require.NoError(t, db.QueryRow(q).Scan(&n))
		assert.Equal(t, 0, n, q)
	}

	// Com uma única definição restante e sem import, a ref legada deixa de
	// ser ambígua e vira unresolved (nunca chutamos).
	var refs refsResult
	runJSON(t, root, &refs, "refs", "formatMoney")
	assert.Equal(t, 0, refs.Summary["ambiguous"])
	assert.Equal(t, 1, refs.Summary["unresolved"])
	assert.Empty(t, refs.Candidates)
	var res resolveResult
	runJSON(t, root, &res, "resolve", "formatMoney")
	assert.Len(t, res.Definitions, 1)
}

func TestSearchFindsWhatTheASTDoesNot(t *testing.T) {
	root := indexed(t, "ts-frontend")
	// `renderPrice` é usada como global em widget.js: a ref de propriedade
	// `window.renderPrice` existe, mas a busca lexical é o caminho barato.
	var res searchResult
	runJSON(t, root, &res, "search", "textContent")
	require.Len(t, res.Files, 1)
	assert.Equal(t, "src/legacy/widget.js", res.Files[0].File)
	assert.Equal(t, 4, res.Files[0].Hits[0].Line)
	assert.Contains(t, res.Files[0].Hits[0].Text, "el.textContent")

	// Palavras dentro de strings também são achadas e marcadas.
	runJSON(t, root, &res, "search", "Checkout")
	var inString, definition bool
	for _, f := range res.Files {
		for _, h := range f.Hits {
			inString = inString || h.Kind == "string"
			definition = definition || h.Definition
		}
	}
	assert.True(t, inString, `title="Checkout" is a string word`)
	assert.True(t, definition, "the component definition line is marked")
	assert.Equal(t, "src/pages/Checkout.tsx", res.Files[0].File, "file with the definition comes first")

	// Regex faz o papel do grep, inclusive em comentários.
	runJSON(t, root, &res, "search", "--regex", "Script legado")
	require.Len(t, res.Files, 1)
	assert.Equal(t, "src/legacy/widget.js", res.Files[0].File)

	// Filtro de caminho e de testes.
	runJSON(t, root, &res, "search", "formatMoney", "--path", "src/utils")
	require.Len(t, res.Files, 1)
	assert.Equal(t, "src/utils/format.ts", res.Files[0].File)

	text := run(t, root, "search", "formatMoney")
	assert.Contains(t, text, "src/utils/format.ts (1)\n  D 3 in formatMoney:3-5:")
	assert.Contains(t, text, "hit(s) in")
	limited := run(t, root, "--max-tokens", "40", "search", "formatMoney")
	assert.Contains(t, limited, "truncated at 40 tokens")
}

func TestFilesTreeAndSnippetBySymbol(t *testing.T) {
	root := indexed(t, "node-backend")
	var files filesResult
	runJSON(t, root, &files, "files", "--depth", "2")
	assert.Equal(t, 11, files.Files, "code plus README.md and package.json as text")
	require.Len(t, files.Tree.Dirs, 1)
	require.Len(t, files.Tree.Dirs[0].Dirs, 4, "src/ has four subdirectories")
	assert.Equal(t, "src/legacy", files.Tree.Dirs[0].Dirs[0].Path)

	text := run(t, root, "files", "src", "--depth", "1", "--exclude-tests")
	assert.Contains(t, text, "orders/ files=3")
	assert.Contains(t, text, "app.module.ts symbols=1")
	dirs := run(t, root, "files", "--dirs-only")
	assert.Contains(t, dirs, "orders/ files=3")
	assert.NotContains(t, dirs, "app.module.ts")
	compact := run(t, root, "--json", "symbols", "src/orders/order.service.ts", "--compact")
	assert.NotContains(t, compact, "signature")
	outlineText := run(t, root, "symbols", "src/orders/order.service.ts")
	assert.Contains(t, outlineText, "src/orders/order.service.ts (12 symbols)")
	assert.Contains(t, outlineText, "\n  24-27 method total [exported]  total(order: Order): number")

	var snip snippetResult
	runJSON(t, root, &snip, "snippet", "src/orders/order.service.ts", "--symbol", "OrderService.total")
	assert.Equal(t, 24, snip.StartLine)
	assert.Equal(t, 27, snip.EndLine)
	assert.Contains(t, snip.Text, "calculateDiscount(subtotal")

	// Anotações aparecem nos símbolos; membros ficam sob a classe.
	var syms symbolsResult
	runJSON(t, root, &syms, "symbols", "src/orders/order.controller.ts")
	require.Len(t, syms.Symbols, 1)
	class := syms.Symbols[0]
	assert.Equal(t, []string{"@Controller('orders')"}, class.Annotations)
	byName := map[string][]string{}
	for _, s := range class.Children {
		byName[s.Name] = s.Annotations
	}
	assert.Equal(t, []string{"@Get(':id/total')"}, byName["total"])
}

func TestPropertyRefsAndExternalMembers(t *testing.T) {
	root := indexed(t, "java-service")
	var refs refsResult
	runJSON(t, root, &refs, "refs", "percent", "--kind", "property")
	// DefaultOrderService.percent: `this.percent = percent` e o uso em total().
	assert.GreaterOrEqual(t, refs.Total, 2)
	assert.Equal(t, refs.Total, refs.Summary["resolved"], "field accesses resolve to the field symbol")
	require.Len(t, refs.Targets, 1)
	assert.Equal(t, "com.acme.order.DefaultOrderService.percent", refs.Targets[0].Name)
}

func TestSymbolsAndSnippet(t *testing.T) {
	root := indexed(t, "java-service")
	var syms symbolsResult
	runJSON(t, root, &syms, "symbols", "src/main/java/com/acme/pricing/Money.java")
	require.Len(t, syms.Symbols, 1)
	assert.Equal(t, "record:Money", syms.Symbols[0].Kind+":"+syms.Symbols[0].Name)
	var got []string
	for _, s := range syms.Symbols[0].Children {
		got = append(got, s.Kind+":"+s.Name)
	}
	assert.Equal(t, []string{"field:cents", "field:ZERO", "method:plus", "method:percent"}, got)
	assert.Equal(t, 5, syms.Count)

	var snip snippetResult
	runJSON(t, root, &snip, "snippet", "src/main/java/com/acme/pricing/Money.java", "6", "8")
	assert.Equal(t, 6, snip.StartLine)
	assert.Equal(t, 8, snip.EndLine)
	assert.Equal(t, "    public Money plus(Money other) {\n        return new Money(cents + other.cents());\n    }", snip.Text)
	numbered := run(t, root, "snippet", "src/main/java/com/acme/pricing/Money.java", "6", "8")
	assert.Equal(t, "src/main/java/com/acme/pricing/Money.java:6-8 (in Money.plus:6-8)\n6|     public Money plus(Money other) {\n7|         return new Money(cents + other.cents());\n8|     }\n", numbered)
	plain := run(t, root, "snippet", "src/main/java/com/acme/pricing/Money.java", "6", "6", "--no-numbers")
	assert.Equal(t, "src/main/java/com/acme/pricing/Money.java:6-6 (in Money.plus:6-8)\n    public Money plus(Money other) {\n", plain)

	out, err := tryRun(root, "snippet", "src/main/java/com/acme/pricing/Money.java", "8", "6")
	require.Error(t, err)
	assert.Contains(t, out, "invalid range")
}

func TestNodeBackendCommonJSAndDecorators(t *testing.T) {
	root := indexed(t, "node-backend")

	var refs refsResult
	runJSON(t, root, &refs, "refs", "calculateTax")
	require.Len(t, refs.flat(), 3, "import, call and the module.exports shorthand")
	for _, r := range refs.flat() {
		assert.Equal(t, "resolved", r.Resolution)
	}
	require.Len(t, refs.Targets, 1)
	assert.Equal(t, "src/legacy/tax.js", refs.Targets[0].File)

	var res resolveResult
	runJSON(t, root, &res, "resolve", "OrderController")
	require.Len(t, res.Definitions, 1)
	assert.Equal(t, "export class OrderController", res.Definitions[0].Signature)
	assert.Equal(t, 4, res.Definitions[0].StartLine, "range starts at the decorator")

	// resolve --include-refs traz as ocorrências numa chamada só; --kind/--exclude-tests filtram.
	withRefs := run(t, root, "resolve", "calculateDiscount", "--include-refs", "--exclude-tests")
	assert.Contains(t, withRefs, "refs (2):\n    src/orders/order.service.ts (2)\n")
	assert.Contains(t, withRefs, "[call] in OrderService.total:24-27: return calculateDiscount(subtotal, order.coupon);")
	assert.Contains(t, withRefs, "12| export function calculateDiscount", "snippet lines are numbered")
	filtered := run(t, root, "resolve", "total", "--kind", "class")
	assert.Contains(t, filtered, "filtered out")

	runJSON(t, root, &res, "resolve", "OrderController.total")
	require.Len(t, res.Definitions, 1)
	assert.Equal(t, "total(@Param('id') id: string): number", res.Definitions[0].Signature)
	assert.ElementsMatch(t, []string{"OrderService.findOne", "OrderService.total"}, qualifiedNames(res.Definitions[0].Callees))

	runJSON(t, root, &refs, "refs", "Injectable")
	require.NotEmpty(t, refs.flat())
	for _, r := range refs.flat() {
		assert.Equal(t, "external", r.Resolution)
	}
}

func TestJavaResolutionRules(t *testing.T) {
	root := indexed(t, "java-service")

	var refs refsResult
	runJSON(t, root, &refs, "refs", "OrderService")
	byFile := map[string]string{}
	for _, r := range refs.flat() {
		if r.Kind == "type" {
			byFile[r.File] = r.Resolution
		}
	}
	assert.Equal(t, "resolved", byFile["src/main/java/com/acme/order/OrderController.java"], "same package without import")

	runJSON(t, root, &refs, "refs", "calculate", "--path", "src/main/java/com/acme/report", "--kind", "method")
	require.Len(t, refs.flat(), 1)
	assert.Equal(t, "resolved", refs.flat()[0].Resolution, "import static resolves the unqualified call")
	require.Len(t, refs.Targets, 1)
	assert.Equal(t, "com.acme.pricing.Discount.calculate", refs.Targets[0].Name)

	runJSON(t, root, &refs, "refs", "Money")
	res := map[string]string{}
	for _, r := range refs.flat() {
		if r.Kind == "type" {
			res[r.File] = r.Resolution
		}
	}
	assert.Equal(t, "resolved", res["src/main/java/com/acme/order/OrderController.java"], "wildcard import")
	assert.Equal(t, "ambiguous", res["src/main/java/com/acme/report/LegacyBridge.java"])
	assert.Len(t, refs.Candidates, 2)

	runJSON(t, root, &refs, "refs", "List")
	require.NotEmpty(t, refs.flat())
	for _, r := range refs.flat() {
		assert.Equal(t, "external", r.Resolution)
	}

	var def resolveResult
	runJSON(t, root, &def, "resolve", "com.acme.order.DefaultOrderService")
	require.Len(t, def.Definitions, 1)
	require.Len(t, def.Definitions[0].Implements, 1)
	assert.Equal(t, "com.acme.order.OrderService", def.Definitions[0].Implements[0].Name)
	runJSON(t, root, &def, "resolve", "OrderService")
	require.Len(t, def.Definitions, 1)
	assert.Equal(t, "com.acme.order.DefaultOrderService", def.Definitions[0].ImplementedBy[0].Name)
}

func TestTSFrontendBarrelsAndAssets(t *testing.T) {
	root := indexed(t, "ts-frontend")

	var refs refsResult
	runJSON(t, root, &refs, "refs", "Button")
	var jsx *flatRef
	for _, r := range refs.flat() {
		if r.Kind == "jsx" {
			jsx = &r
		}
	}
	require.NotNil(t, jsx)
	assert.Equal(t, "resolved", jsx.Resolution)
	require.Len(t, refs.Targets, 1)
	assert.Equal(t, "src/components/Button.tsx", refs.Targets[0].File)
	assert.Equal(t, "default", exportNameOf(t, root, "src/components/Button.tsx", "Button"))

	db, err := sql.Open("sqlite", filepath.Join(root, ".mira", "index.db"))
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT module, resolution FROM imports WHERE module LIKE '%.css' OR module LIKE '%.svg'`)
	require.NoError(t, err)
	defer rows.Close()
	assets := map[string]string{}
	for rows.Next() {
		var module, resolution string
		require.NoError(t, rows.Scan(&module, &resolution))
		assets[module] = resolution
	}
	assert.Equal(t, map[string]string{"./Button.css": "external", "./styles.css": "external", "./logo.svg": "external"}, assets)

	var res resolveResult
	runJSON(t, root, &res, "resolve", "Checkout")
	require.Len(t, res.Definitions, 1)
	assert.True(t, res.Definitions[0].JSX)
	assert.ElementsMatch(t, []string{"Card", "Button", "useCart"}, qualifiedNames(res.Definitions[0].Callees))
}

func exportNameOf(t *testing.T, root, file, name string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, ".mira", "index.db"))
	require.NoError(t, err)
	defer db.Close()
	var exportName string
	require.NoError(t, db.QueryRow(`SELECT s.export_name FROM symbols s JOIN files f ON f.id = s.file_id WHERE f.path = ? AND s.name = ?`, file, name).Scan(&exportName))
	return exportName
}

func TestResolveSkeletonJava(t *testing.T) {
	root := indexed(t, "java-service")
	out := run(t, root, "resolve", "DefaultOrderService.total", "--include-skeleton")
	assert.Contains(t, out, "  skeleton: 15 lines, 1 branches, 1 loops\n")
	assert.Contains(t, out, "  returns (2):\n    23 return cached;\n    32 return result;\n")
	assert.Contains(t, out, "  locals: cached (21), sum (25), lines (26), result (30)\n")
	assert.Contains(t, out, "  uses (same file):\n    Cache.get :38 (21)\n    Cache.put :42 (31)\n")
	assert.Contains(t, out, "  uses (other files):\n    Order.id src/main/java/com/acme/order/Order.java:15 (21, 31)\n"+
		"    Order.lines src/main/java/com/acme/order/Order.java:19 (26)\n"+
		"    Discount.calculate src/main/java/com/acme/pricing/Discount.java:6 (30)\n"+
		"    Money.plus src/main/java/com/acme/pricing/Money.java:6 (28)\n")
	assert.Contains(t, out, "  outer:\n    DefaultOrderService.cache property :12 (21, 31)\n"+
		"    Money.ZERO property src/main/java/com/acme/pricing/Money.java:4 (25)\n"+
		"    DefaultOrderService.percent property :10 (30)\n", "fields used as call receivers count as outer state")
	assert.NotContains(t, out, "callees")

	stale := run(t, root, "resolve", "OrderController.show", "--include-skeleton")
	assert.Contains(t, stale, "  returns (1):\n    14 return service.total(order);\n")
	path := filepath.Join(root, "src", "main", "java", "com", "acme", "order", "OrderController.java")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append([]byte("// touched\n"), data...), 0o644))
	require.NoError(t, os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
	stale = run(t, root, "--no-auto-index", "resolve", "OrderController.show", "--include-skeleton")
	assert.Contains(t, stale, "skeleton: file changed since last index", "spans would not match the file on disk")
	fresh := run(t, root, "resolve", "OrderController.show", "--include-skeleton")
	assert.NotContains(t, fresh, "file changed since last index", "queries refresh the index first")
	assert.Contains(t, fresh, "  returns (1):\n    15 return service.total(order);\n", "the touched file shifted every line by one")
}

func TestQueriesSeeEditsWithoutReindex(t *testing.T) {
	root := indexed(t, "node-backend")
	path := filepath.Join(root, "src", "utils", "money.ts")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	edited := strings.Replace(string(data), "export function roundMoney(", "export function roundMoneyValue(", 1)
	require.NoError(t, os.WriteFile(path, []byte(edited), 0o644))
	require.NoError(t, os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	old, err := tryRun(root, "--no-auto-index", "resolve", "roundMoneyValue")
	require.NoError(t, err)
	assert.Contains(t, old, "no definition found", "without auto-index the rename is invisible")

	fresh := run(t, root, "resolve", "roundMoneyValue")
	assert.Contains(t, fresh, "roundMoneyValue function src/utils/money.ts:1-3")
	var st status
	runJSON(t, root, &st, "status")
	assert.Equal(t, 0, st.Outdated, "the query refreshed the index")
}

func TestRefsSeveralNames(t *testing.T) {
	root := indexed(t, "java-service")
	out := run(t, root, "refs", "calculate", "percent", "--exclude-tests")
	assert.Contains(t, out, "refs calculate: ")
	assert.Contains(t, out, "\n\nrefs percent: ")
	var many []refsResult
	runJSON(t, root, &many, "refs", "calculate", "percent")
	require.Len(t, many, 2)
	assert.Equal(t, "calculate", many[0].Name)
	assert.Equal(t, "percent", many[1].Name)
	var one refsResult
	runJSON(t, root, &one, "refs", "calculate")
	assert.Equal(t, "calculate", one.Name, "one name keeps the single-object JSON")
}

func TestSkillCommand(t *testing.T) {
	root := indexed(t, "node-backend")
	printed := run(t, root, "skill")
	assert.True(t, strings.HasPrefix(printed, "---\nname: mira\n"), printed[:40])
	assert.Contains(t, printed, "`resolve_symbol`")

	installed := run(t, root, "skill", "--install")
	path := filepath.Join(root, ".claude", "skills", "mira", "SKILL.md")
	assert.Equal(t, "installed "+path+"\n", installed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, printed, string(data))

	out, err := tryRun(root, "skill", "--install")
	require.Error(t, err, "second install must not overwrite")
	assert.Contains(t, out, "already exists")
	run(t, root, "skill", "--install", "--force")
}

func TestDoctorAndVersion(t *testing.T) {
	root := indexed(t, "node-backend")
	out := run(t, root, "doctor")
	assert.Contains(t, out, "OK   parser     tree-sitter (cgo) parses TypeScript, Java, Python and Go\n")
	assert.Contains(t, out, "OK   index      11 files,")
	assert.Contains(t, out, "WARN skill      agent skill not installed\n")
	assert.Contains(t, out, "INFO mcp        Claude Code: claude mcp add mira -- ")
	assert.Contains(t, out, "all checks passed\n")

	run(t, root, "skill", "--install")
	after := run(t, root, "doctor")
	assert.Contains(t, after, "OK   skill      agent skill installed at .claude/skills/mira\n")

	empty := t.TempDir()
	failed, err := tryRun(empty, "doctor")
	require.Error(t, err, "an uninitialized repository fails the check")
	assert.Contains(t, failed, "FAIL index      .mira/index.db not found\n")
	assert.Contains(t, failed, "fix: run `mira init && mira index`")

	version := run(t, root, "version")
	assert.True(t, strings.HasPrefix(version, "mira dev ("), version)
	assert.Contains(t, version, "tree-sitter via cgo")

	var res resolveResult
	runJSON(t, root, &res, "resolve", "OrderService", "--kind", "class")
	require.Len(t, res.Definitions, 1)
	assert.NotEmpty(t, res.Definitions[0].Body, "a 13-line class carries its body")
	multi := run(t, root, "resolve", "calculateDiscount", "roundMoney")
	assert.Contains(t, multi, "calculateDiscount function src/pricing/discount.ts:12-14")
	assert.Contains(t, multi, "\n\nroundMoney function src/utils/money.ts:1-3")
	search := run(t, root, "search", "coupon", "--context", "1")
	assert.Contains(t, search, "~ ", "sub-token hits are marked")
	assert.Contains(t, search, "> ", "context windows point at the hit line")
}

func TestEditBySymbolCLI(t *testing.T) {
	root := indexed(t, "java-service")
	const money = "src/main/java/com/acme/pricing/Money.java"
	out := run(t, root, "edit", "replace", money, "Money.plus", "--text",
		"    public Money plus(Money other) {\n        // sum in cents\n        return new Money(cents + other.cents());\n    }")
	assert.Equal(t, "replace Money.plus: "+money+":6-9 (4 lines); index refreshed\n  verified: 1 of 1 references still resolve\n", out)
	resolved := run(t, root, "resolve", "Money.plus")
	assert.Contains(t, resolved, "com.acme.pricing.Money.plus method "+money+":6-9")
	assert.Contains(t, resolved, "7|         // sum in cents")

	cmd := exec.Command(binary, "--repo", root, "edit", "insert-after", money, "Money.plus")
	cmd.Stdin = strings.NewReader("\n    public Money zero() { return ZERO; }")
	piped, err := cmd.CombinedOutput()
	require.NoError(t, err, string(piped))
	assert.Contains(t, string(piped), "insert-after Money.plus: "+money+":10-11")
	assert.Contains(t, run(t, root, "resolve", "Money.zero"), "com.acme.pricing.Money.zero method "+money+":11-11")

	refused, err := tryRun(root, "edit", "delete", money, "Money.percent")
	require.Error(t, err, "delete refuses while Discount still calls percent")
	assert.Contains(t, refused, "com.acme.pricing.Money.percent is still referenced 1 time(s): src/main/java/com/acme/pricing/Discount.java:10")

	preview := run(t, root, "edit", "rename", money, "Money.percent", "pct", "--preview")
	assert.Contains(t, preview, "rename Money.percent -> pct: 2 lines in 2 files (preview: nothing written)\n")
	assert.Contains(t, preview, "  -  13|     public Money percent(int percent) {\n  +  13|     public Money pct(int percent) {\n",
		"the method name changes, the parameter with the same name does not")
	renamed := run(t, root, "edit", "rename", money, "Money.percent", "pct")
	assert.Contains(t, renamed, "  src/main/java/com/acme/pricing/Discount.java: 10\n  "+money+": 13\n  verified: 1 of 1 references still resolve\n")
	assert.Contains(t, run(t, root, "resolve", "Money.pct"), "com.acme.pricing.Money.pct method "+money+":13-15")

	bad, err := tryRun(root, "edit", "replace", money, "nope", "--text", "x")
	require.Error(t, err)
	assert.Contains(t, bad, "not found")
}
