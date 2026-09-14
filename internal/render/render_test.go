package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/graph"
)

func TestBudget(t *testing.T) {
	lines := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		lines = append(lines, strings.Repeat("x", 39)) // 40 bytes por linha com o \n = 10 tokens
	}
	text := strings.Join(lines, "\n") + "\n"

	assert.Equal(t, text, Budget(text, 0), "0 = unlimited")
	assert.Equal(t, text, Budget(text, 1000), "fits")

	cut := Budget(text, 100)
	assert.LessOrEqual(t, Tokens(cut), 100+40, "cut text plus the marker stays near the budget")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(cut), "raise max_tokens"))
	assert.Contains(t, cut, "truncated at 100 tokens (90 lines omitted)")
	body := cut[:strings.Index(cut, "\n… truncated")]
	assert.Equal(t, 10, strings.Count(body, "\n")+1, "cut happens at a line boundary: 10 lines of 40 bytes = 100 tokens")
}

func TestSymbolsOutlineAndRefs(t *testing.T) {
	out := Symbols(graph.SymbolsResult{File: "a.ts", Count: 3, Symbols: []graph.Outline{
		{Name: "Svc", Kind: "class", StartLine: 1, EndLine: 9, Exported: true, Annotations: []string{"@Injectable()"},
			Signature: "export class Svc", Children: []graph.Outline{
				{Name: "total", Kind: "method", StartLine: 3, EndLine: 5, Exported: true, Signature: "total(): number"},
				{Name: "cache", Kind: "property", StartLine: 2, EndLine: 2},
			}},
	}})
	assert.Equal(t, "a.ts (3 symbols)\n1-9 class Svc [exported]  @Injectable()  export class Svc\n  3-5 method total [exported]  total(): number\n  2-2 property cache\n", out)

	refs := Refs(graph.RefsResult{Name: "total", Total: 3, Summary: map[string]int{"resolved": 2, "ambiguous": 1},
		ByKind:     map[string]int{"call": 1, "method": 2},
		Targets:    []graph.SymbolRef{{Name: "Svc.total", Kind: "method", File: "a.ts", StartLine: 3, EndLine: 5}},
		Candidates: []graph.SymbolRef{{Name: "total", Kind: "function", File: "b.ts", StartLine: 1, EndLine: 2}},
		Files: []graph.RefFile{{File: "c.ts", Refs: []graph.Reference{
			{Line: 4, Kind: "method", Text: "svc.total()"},
			{Line: 9, Kind: "call", Resolution: "ambiguous", Text: "total()"},
		}}}})
	assert.Equal(t, "refs total: 3 total (ambiguous 1, resolved 2) by kind (call 1, method 2)\n"+
		"targets:\n  Svc.total method a.ts:3-5\n"+
		"candidates (ambiguous refs could be any of these):\n  total function b.ts:1-2\n"+
		"c.ts (2)\n  4 [method]: svc.total()\n  9 [call ambiguous]: total()\n", refs)
}

func TestResolveRendersRelatedAsOneLineRefs(t *testing.T) {
	out := Resolve(graph.ResolveResult{Query: "f", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "f", Kind: "function", File: "a.ts", StartLine: 1, EndLine: 3, Exported: true, Signature: "export function f()"},
		Snippet:    "export function f() {\n  return g();\n}",
		Callers:    []graph.Related{{SymbolRef: graph.SymbolRef{Name: "main", Kind: "function", File: "m.ts", StartLine: 1, EndLine: 5}}},
		Callees:    []graph.Related{},
	}}})
	assert.Contains(t, out, "f function a.ts:1-3 [exported]\n  export function f()\n  1| export function f() {\n  2|   return g();\n  3| }\n")
	assert.Contains(t, out, "callers (1):\n    main function m.ts:1-5\n")
	assert.NotContains(t, out, "signature", "no JSON keys in text mode")
}

func TestSnippetNumbering(t *testing.T) {
	res := graph.SnippetResult{File: "a.ts", StartLine: 98, EndLine: 101, Text: "a\nb\nc\nd"}
	assert.Equal(t, "a.ts:98-101\n 98| a\n 99| b\n100| c\n101| d\n", Snippet(res))
	res.Plain = true
	assert.Equal(t, "a.ts:98-101\na\nb\nc\nd\n", Snippet(res))
}

func TestRefsShowContainer(t *testing.T) {
	out := Refs(graph.RefsResult{Name: "x", Total: 2, Summary: map[string]int{"resolved": 2}, ByKind: map[string]int{"call": 2},
		Files: []graph.RefFile{{File: "c.ts", Refs: []graph.Reference{
			{Line: 4, Kind: "call", Container: "run", Text: "x()"},
			{Line: 9, Kind: "method", Container: "com.acme.order.Owner.addVisit", ContainerStart: 173, ContainerEnd: 183, Target: "com.acme.order.Pet.addVisit", Text: "pet.x()"},
		}}}})
	assert.Contains(t, out, "c.ts (2)\n  4 [call] in run: x()\n  9 [method -> Pet.addVisit] in Owner.addVisit:173-183: pet.x()\n")
}

func TestResolveRendersSkeletonInsteadOfCallees(t *testing.T) {
	out := Resolve(graph.ResolveResult{Query: "process", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "process", Kind: "method", File: "Svc.java", StartLine: 10, EndLine: 30, Signature: "public String process(Order o) throws IOException"},
		Snippet:    "public String process(Order o) throws IOException {",
		Callers:    []graph.Related{},
		Callees:    []graph.Related{{SymbolRef: graph.SymbolRef{Name: "Repo.save", Kind: "method", File: "Repo.java", StartLine: 5, EndLine: 7}}},
		Skeleton: &graph.SkeletonInfo{
			Lines: 21, Branches: 2, Loops: 1, NestedCount: 1, Async: true,
			Returns:        []extract.Point{{Line: 14, Text: `return "form";`}, {Line: 29, Text: "return ok;"}},
			Throws:         []extract.Point{{Line: 20, Text: `throw new IOException("x");`}},
			DeclaredThrows: []string{"IOException"},
			Locals:         []extract.Point{{Line: 11, Text: "total"}},
			Nested:         []graph.SymbolRef{{Name: "Svc.process.helper", Kind: "function", File: "Svc.java", StartLine: 15, EndLine: 18}},
			NestedTotal:    3,
			Uses: graph.Uses{
				SameFile:   []graph.Use{{Name: "com.acme.Svc.check", Kind: "method", Lines: []int{12}, Count: 1, Line: 40}},
				OtherFiles: []graph.Use{{Name: "com.acme.Repo.save", Kind: "method", Lines: []int{21, 25}, Count: 5, File: "Repo.java", Line: 5}},
				External:   []graph.Use{{Name: "BindingResult.hasErrors", Kind: "method", Lines: []int{13}, Count: 1}},
				Outer:      []graph.Use{{Name: "com.acme.Svc.repo", Kind: "property", Lines: []int{21}, Count: 1, Line: 8}},
				Unresolved: []graph.Use{{Name: "isAfter", Kind: "method", Lines: []int{13}, Count: 1, Resolution: "unresolved"}},
			},
		},
	}}})
	want := "  skeleton: 21 lines, 2 branches, 1 loops, 1 nested function(s), async\n" +
		"  returns (2):\n    14 return \"form\";\n    29 return ok;\n" +
		"  throws (1) declared IOException:\n    20 throw new IOException(\"x\");\n" +
		"  locals: total (11)\n" +
		"  nested (3): helper 15-18, +2 more (see symbols)\n" +
		"  uses (same file):\n    Svc.check :40 (12)\n" +
		"  uses (other files):\n    Repo.save Repo.java:5 (21, 25, 5 total)\n" +
		"  uses (external):\n    BindingResult.hasErrors (13)\n" +
		"  outer:\n    Svc.repo property :8 (21)\n" +
		"  unresolved:\n    isAfter [unresolved] (13)\n"
	assert.Contains(t, out, want)
	assert.NotContains(t, out, "callees", "callees are folded into uses when the skeleton is present")

	note := Resolve(graph.ResolveResult{Query: "C", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "C", Kind: "class", File: "c.ts", StartLine: 1, EndLine: 2},
		Callers:    []graph.Related{}, Callees: []graph.Related{},
		Skeleton: &graph.SkeletonInfo{Note: "skeleton applies to functions"},
	}}})
	assert.Contains(t, note, "  skeleton: skeleton applies to functions\n")
	assert.NotContains(t, note, "returns")
}

func TestResolveHintsWhenSnippetIsCut(t *testing.T) {
	out := Resolve(graph.ResolveResult{Query: "f", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "f", Kind: "function", File: "a.ts", StartLine: 10, EndLine: 60, Signature: "function f()"},
		Snippet:    "l10\nl11\nl12\nl13\nl14\nl15\nl16\nl17",
		Callers:    []graph.Related{}, Callees: []graph.Related{},
	}}})
	assert.Contains(t, out, "  17| l17\n  … 43 more lines: include-body, skeleton, or snippet 18-60\n")
}

func TestSkeletonLocalsWithRangeAndRefsMany(t *testing.T) {
	out := Resolve(graph.ResolveResult{Query: "f", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "f", Kind: "function", File: "a.ts", StartLine: 1, EndLine: 9},
		Body:       "x", Callers: []graph.Related{},
		Skeleton: &graph.SkeletonInfo{Lines: 9, Returns: []extract.Point{}, Locals: []extract.Point{{Line: 2, EndLine: 5, Text: "methods"}, {Line: 6, Text: "n"}}},
	}}})
	assert.Contains(t, out, "  locals: methods (2-5), n (6)\n")

	many := RefsMany([]graph.RefsResult{
		{Name: "a", Total: 1, Summary: map[string]int{"resolved": 1}, ByKind: map[string]int{"call": 1}},
		{Name: "b", Total: 0, Summary: map[string]int{}, ByKind: map[string]int{}},
	})
	assert.Equal(t, "refs a: 1 total (resolved 1) by kind (call 1)\n\nrefs b: 0 total () by kind ()\n", many)
}

func TestRenderSearchMarksContextAndMembers(t *testing.T) {
	out := Search(graph.SearchResult{Total: 3, Files: []graph.FileHits{{File: "a.ts", Count: 3, Hits: []graph.Hit{
		{Line: 5, Text: "x", Kind: "comment", Container: "pkg.Svc.run", ContainerStart: 3, ContainerEnd: 9},
		{Line: 7, Text: "y", Kind: "part"},
		{Line: 8, Text: "z", Kind: "text", Context: "l7\nz\nl9", ContextStart: 7},
	}}}})
	assert.Contains(t, out, "  c 5 in Svc.run:3-9: x\n  ~ 7: y\n  t 8\n      7| l7\n    > 8| z\n      9| l9\n")

	members := Resolve(graph.ResolveResult{Query: "C", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "C", Kind: "class", File: "c.ts", StartLine: 1, EndLine: 80},
		Snippet:    "class C {", Callers: []graph.Related{}, Callees: []graph.Related{},
		Members: []graph.Outline{{Name: "run", Kind: "method", StartLine: 10, EndLine: 20, Signature: "run(): void",
			Children: []graph.Outline{{Name: "inner", Kind: "function", StartLine: 12, EndLine: 14}}}},
	}}})
	assert.Contains(t, members, "  … 79 more lines: include-body, skeleton, or snippet 2-80\n  members (2):\n    10-20 method run  run(): void\n      12-14 function inner\n")

	snip := Snippet(graph.SnippetResult{File: "a.ts", StartLine: 5, EndLine: 5, Text: "x", Symbol: "pkg.Svc.run", SymbolStart: 3, SymbolEnd: 9})
	assert.Equal(t, "a.ts:5-5 (in Svc.run:3-9)\n5| x\n", snip)
}

func TestResolveRendersWindowWithGaps(t *testing.T) {
	out := Resolve(graph.ResolveResult{Query: "f", Definitions: []graph.Definition{{
		SymbolInfo: graph.SymbolInfo{Name: "f", Kind: "function", File: "a.ts", StartLine: 10, EndLine: 60},
		Snippet:    "x", Window: "l20\nl21\nl22", WindowStart: 20,
		Callers: []graph.Related{}, Callees: []graph.Related{},
	}}})
	assert.Contains(t, out, "  … 10-19\n  20| l20\n  21| l21\n  22| l22\n  … 23-60\n")
}
