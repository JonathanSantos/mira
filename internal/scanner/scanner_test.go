package scanner

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/JonathanSantos/mira/internal/lang"
)

// id e str abreviam as expectativas.
func id(text string, line int) Word  { return Word{Text: text, Line: line, Kind: KindIdent} }
func str(text string, line int) Word { return Word{Text: text, Line: line, Kind: KindString} }

func TestScan(t *testing.T) {
	tests := []struct {
		name string
		lang lang.Lang
		src  string
		want []Word
	}{
		{
			name: "identifiers with line numbers, deduplicated per line",
			lang: lang.TypeScript,
			src:  "const total = price + price;\nreturn total;",
			want: []Word{id("total", 1), id("price", 1), id("total", 2)},
		},
		{
			name: "string contents are indexed as string words, not identifiers",
			lang: lang.TypeScript,
			src:  `const a = "hidden b"; const c = 'hidden d';`,
			want: []Word{id("a", 1), str("hidden", 1), str("b", 1), id("c", 1), str("d", 1)},
		},
		{
			name: "template literal text is string words, substitutions are code",
			lang: lang.TypeScript,
			src:  "const s = `hello ${formatMoney(v)} world ${x}`;",
			want: []Word{id("s", 1), str("hello", 1), id("formatMoney", 1), id("v", 1), str("world", 1), id("x", 1)},
		},
		{
			name: "nested braces inside template substitution",
			lang: lang.TypeScript,
			src:  "const s = `${ items.map(i => { return i.name; }) } tail`;",
			want: []Word{id("s", 1), id("items", 1), id("map", 1), id("i", 1), id("name", 1), str("tail", 1)},
		},
		{
			name: "comment words are indexed with kind comment, code words with ident",
			lang: lang.TypeScript,
			src:  "// hidden one\nlet a; /* hidden\ntwo */ let b;",
			want: []Word{
				{Text: "hidden", Line: 1, Kind: KindComment}, {Text: "one", Line: 1, Kind: KindComment},
				id("a", 2), {Text: "hidden", Line: 2, Kind: KindComment},
				{Text: "two", Line: 3, Kind: KindComment}, id("b", 3),
			},
		},
		{
			name: "excludes typescript keywords",
			lang: lang.TypeScript,
			src:  "export async function run(): Promise<void> { await done; }",
			want: []Word{id("run", 1), id("Promise", 1), id("done", 1)},
		},
		{
			name: "excludes java keywords; text block words are string words",
			lang: lang.Java,
			src:  "public static final String X = \"\"\"\n hidden\n \"\"\";\nint y = 0;",
			want: []Word{id("String", 1), id("X", 1), str("hidden", 2), id("y", 4)},
		},
		{
			name: "java char literal",
			lang: lang.Java,
			src:  "char c = 'x';",
			want: []Word{id("c", 1), str("x", 1)},
		},
		{
			name: "dollar sign and underscore are identifier chars",
			lang: lang.JavaScript,
			src:  "$el.on(_private$)",
			want: []Word{id("$el", 1), id("on", 1), id("_private$", 1)},
		},
		{
			name: "unicode letters form identifiers, other symbols separate",
			lang: lang.TypeScript,
			src:  "const café = 1; const 日本語 = café;\nconst x = '→';",
			want: []Word{id("café", 1), id("日本語", 1), id("x", 2)},
		},
		{
			name: "numeric literals are not identifiers",
			lang: lang.TypeScript,
			src:  "const n = 0xFF + 1e10 + 42n;",
			want: []Word{id("n", 1)},
		},
		{
			name: "unterminated string stops at newline",
			lang: lang.TypeScript,
			src:  "const a = 'oops\nconst b = 1;",
			want: []Word{id("a", 1), str("oops", 1), id("b", 2)},
		},
		{
			name: "backtick is not special in java",
			lang: lang.Java,
			src:  "int a = 1; // `not a template`\nint b = 2;",
			want: []Word{id("a", 1), {Text: "not", Line: 1, Kind: KindComment}, {Text: "a", Line: 1, Kind: KindComment},
				{Text: "template", Line: 1, Kind: KindComment}, id("b", 2)},
		},
		{
			name: "string literal used as key is searchable",
			lang: lang.Java,
			src:  `mvc.perform(post("/owners/new").param("telephone", "0123"));`,
			// "new" é keyword e fica fora mesmo dentro da string; "0123" não é identificador.
			want: []Word{id("mvc", 1), id("perform", 1), id("post", 1), str("owners", 1), id("param", 1), str("telephone", 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Scan([]byte(tt.src), tt.lang))
		})
	}
}

func TestPartsAndScanText(t *testing.T) {
	tests := map[string][]string{
		"visitDate":        {"visit", "date"},
		"VISIT_DATE":       {"visit", "date"},
		"getHTTPResponse2": {"get", "http", "response", "2"},
		"date":             {"date"},
		"x":                nil,
		"$scope":           {"scope"},
		"snake_case_name":  {"snake", "case", "name"},
	}
	for word, want := range tests {
		assert.Equal(t, want, Parts(word), word)
	}
	words := WithParts([]Word{id("visitDate", 3), id("date", 4)})
	assert.Equal(t, []Word{id("visitDate", 3), id("date", 4),
		{Text: "visit", Line: 3, Kind: KindPart}, {Text: "date", Line: 3, Kind: KindPart}}, words,
		"sub-tokens are appended in lowercase; a simple word gets none")

	text := ScanText([]byte("typeMismatch.visitDate=A data da visita deve estar no futuro\n# comentário\n"))
	assert.Equal(t, []Word{
		{Text: "typeMismatch", Line: 1, Kind: KindText}, {Text: "visitDate", Line: 1, Kind: KindText},
		{Text: "A", Line: 1, Kind: KindText}, {Text: "data", Line: 1, Kind: KindText}, {Text: "da", Line: 1, Kind: KindText},
		{Text: "visita", Line: 1, Kind: KindText}, {Text: "deve", Line: 1, Kind: KindText}, {Text: "estar", Line: 1, Kind: KindText},
		{Text: "no", Line: 1, Kind: KindText}, {Text: "futuro", Line: 1, Kind: KindText}, {Text: "comentário", Line: 2, Kind: KindText},
	}, text)
}
