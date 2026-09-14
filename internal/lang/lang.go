// Package lang define o identificador de linguagem usado por todo o projeto.
// Fica num pacote próprio para que scanner, walker e store não precisem
// importar o parser (que carrega as gramáticas do tree-sitter).
package lang

import "strings"

// Lang identifica uma linguagem suportada. O valor é o que fica gravado na
// coluna files.lang do índice.
type Lang string

const (
	TypeScript Lang = "typescript"
	JavaScript Lang = "javascript"
	Java       Lang = "java"
	Python     Lang = "python"
	Go         Lang = "go"
	// Text são arquivos sem código indexados só pelas palavras (properties,
	// YAML, templates, SQL, Markdown): sem símbolos, sem refs.
	Text Lang = "text"
)

// All lista as linguagens de código suportadas, na ordem usada em
// relatórios e na config. Text fica de fora: é ligado por skip_text.
var All = []Lang{TypeScript, JavaScript, Java, Python, Go}

// TextExtensions são os arquivos sem código que entram no índice lexical.
var TextExtensions = map[string]bool{
	".properties": true, ".yml": true, ".yaml": true, ".html": true, ".htm": true,
	".sql": true, ".md": true, ".xml": true, ".json": true, ".txt": true, ".toml": true,
}

// maxTextSize evita indexar dumps e lockfiles gigantes.
const maxTextSize = 512 * 1024

// skippedTextFiles são gerados por ferramentas: só ruído no índice.
var skippedTextFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "composer.lock": true,
	"Cargo.lock": true, "poetry.lock": true, "Gemfile.lock": true, "go.sum": true,
}

// IsTextFile diz se o caminho é um arquivo de texto indexável desse
// tamanho.
func IsTextFile(path string, size int64) bool {
	dot := strings.LastIndex(path, ".")
	slash := strings.LastIndex(path, "/")
	if dot < 0 || dot < slash || !TextExtensions[path[dot:]] || size > maxTextSize {
		return false
	}
	return !skippedTextFiles[path[slash+1:]]
}

// IsTS diz se a linguagem pertence à família TypeScript/JavaScript, que
// compartilha gramática, scanner de keywords e extractor.
func (l Lang) IsTS() bool {
	return l == TypeScript || l == JavaScript
}

// Valid diz se o nome corresponde a uma linguagem suportada.
func Valid(name string) bool {
	for _, l := range All {
		if string(l) == name {
			return true
		}
	}
	return false
}
