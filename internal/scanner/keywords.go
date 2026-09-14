package scanner

import "github.com/JonathanSantos/mira/internal/lang"

// Keywords ficam fora do índice lexical: nunca são o que alguém procura e
// inflariam a tabela words sem utilidade.
var tsKeywords = toSet(
	"abstract", "any", "as", "asserts", "async", "await", "boolean", "break",
	"case", "catch", "class", "const", "constructor", "continue", "debugger",
	"declare", "default", "delete", "do", "else", "enum", "export", "extends",
	"false", "finally", "for", "from", "function", "get", "if", "implements",
	"import", "in", "infer", "instanceof", "interface", "is", "keyof", "let",
	"module", "namespace", "never", "new", "null", "number", "object", "of",
	"override", "package", "private", "protected", "public", "readonly",
	"return", "satisfies", "set", "static", "string", "super", "switch",
	"symbol", "this", "throw", "true", "try", "type", "typeof", "undefined",
	"unknown", "var", "void", "while", "with", "yield",
)

var javaKeywords = toSet(
	"abstract", "assert", "boolean", "break", "byte", "case", "catch", "char",
	"class", "const", "continue", "default", "do", "double", "else", "enum",
	"extends", "false", "final", "finally", "float", "for", "goto", "if",
	"implements", "import", "instanceof", "int", "interface", "long", "native",
	"new", "non-sealed", "null", "package", "permits", "private", "protected",
	"public", "record", "return", "sealed", "short", "static", "strictfp",
	"super", "switch", "synchronized", "this", "throw", "throws", "transient",
	"true", "try", "var", "void", "volatile", "while", "yield",
)

var pythonKeywords = toSet(
	"False", "None", "True", "and", "as", "assert", "async", "await", "break", "class",
	"continue", "def", "del", "elif", "else", "except", "finally", "for", "from", "global",
	"if", "import", "in", "is", "lambda", "nonlocal", "not", "or", "pass", "raise",
	"return", "try", "while", "with", "yield", "self", "cls", "print",
)

var goKeywords = toSet(
	"break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough",
	"for", "func", "go", "goto", "if", "import", "interface", "map", "package", "range",
	"return", "select", "struct", "switch", "type", "var", "nil", "true", "false", "iota",
	"string", "int", "int64", "int32", "uint", "byte", "rune", "bool", "float64", "error",
	"any", "err", "len", "cap", "make", "new", "append", "panic", "recover",
)

func keywordsFor(l lang.Lang) map[string]bool {
	switch l {
	case lang.Java:
		return javaKeywords
	case lang.Python:
		return pythonKeywords
	case lang.Go:
		return goKeywords
	}
	return tsKeywords
}

func toSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}
