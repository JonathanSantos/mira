// Package scanner produz o índice lexical: identificadores encontrados fora
// de strings, template literals e comentários, sem usar o tree-sitter.
package scanner

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JonathanSantos/mira/internal/lang"
)

// Kinds de palavra: identificador no código, palavra dentro de um literal
// de string (chaves, rótulos), palavra de comentário, palavra de arquivo de
// texto, e sub-token de um identificador composto (`date` de `visitDate`).
const (
	KindIdent   = "ident"
	KindString  = "string"
	KindComment = "comment"
	KindText    = "text"
	KindPart    = "part"
)

// Word é uma ocorrência de palavra numa linha. Ocorrências repetidas na
// mesma linha (mesmo kind) são colapsadas.
type Word struct {
	Text string
	Line int
	Kind string
}

// Scan varre o código e devolve as palavras na ordem em que aparecem.
func Scan(src []byte, l lang.Lang) []Word {
	s := &state{
		src:      src,
		line:     1,
		keywords: keywordsFor(l),
		java:     l == lang.Java,
		python:   l == lang.Python,
		golang:   l == lang.Go,
		seen:     map[Word]bool{},
	}
	s.scanCode(false)
	return s.words
}

type state struct {
	src      []byte
	pos      int
	line     int
	keywords map[string]bool
	java     bool
	python   bool
	golang   bool
	seen     map[Word]bool
	words    []Word
}

// scanCode consome código até o fim do fonte ou, quando stopAtBrace é true,
// até o "}" que fecha uma substituição ${...} de template literal.
func (s *state) scanCode(stopAtBrace bool) {
	depth := 0
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '\n':
			s.line++
			s.pos++
		case c == '#' && s.python:
			s.skipLineComment()
		case c == '/' && s.peek(1) == '/' && !s.python:
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*' && !s.python:
			s.skipBlockComment()
		case (c == '"' || c == '\'') && (s.java || s.python) && s.peek(1) == c && s.peek(2) == c:
			s.skipTextBlock(c)
		case c == '"' || c == '\'':
			s.skipString(c)
		case c == '`' && s.golang:
			s.skipRawString()
		case c == '`' && !s.java && !s.python:
			s.scanTemplate()
		case stopAtBrace && c == '{':
			depth++
			s.pos++
		case stopAtBrace && c == '}':
			if depth == 0 {
				s.pos++
				return
			}
			depth--
			s.pos++
		case c >= '0' && c <= '9':
			s.skipNumber()
		case isIdentStart(s.src, s.pos):
			s.readIdentifier()
		default:
			s.pos++
		}
	}
}

// scanTemplate consome um template literal a partir do "`" de abertura. As
// substituições ${...} contêm código de verdade e voltam para scanCode.
func (s *state) scanTemplate() {
	s.pos++ // "`"
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '\\':
			s.pos += 2
		case c == '\n':
			s.line++
			s.pos++
		case c == '`':
			s.pos++
			return
		case c == '$' && s.peek(1) == '{':
			s.pos += 2
			s.scanCode(true)
		case isIdentStart(s.src, s.pos):
			s.readWord(KindString)
		default:
			s.pos++
		}
	}
}

// skipLineComment e skipBlockComment guardam as palavras do comentário com
// kind "comment": é onde vive a intenção do código ("empty string means the
// broadest search") e, muitas vezes, o português.
func (s *state) skipLineComment() {
	for s.pos < len(s.src) && s.src[s.pos] != '\n' {
		if isIdentStart(s.src, s.pos) {
			s.readWord(KindComment)
			continue
		}
		s.pos++
	}
}

func (s *state) skipBlockComment() {
	s.pos += 2
	for s.pos < len(s.src) {
		switch {
		case s.src[s.pos] == '*' && s.peek(1) == '/':
			s.pos += 2
			return
		case s.src[s.pos] == '\n':
			s.line++
			s.pos++
		case isIdentStart(s.src, s.pos):
			s.readWord(KindComment)
		default:
			s.pos++
		}
	}
}

// ScanText indexa um arquivo sem código: toda palavra com kind "text".
func ScanText(src []byte) []Word {
	s := &state{src: src, line: 1, keywords: map[string]bool{}, seen: map[Word]bool{}}
	for s.pos < len(s.src) {
		switch {
		case s.src[s.pos] == '\n':
			s.line++
			s.pos++
		case isIdentStart(s.src, s.pos):
			s.readWord(KindText)
		default:
			s.pos++
		}
	}
	return s.words
}

// WithParts acrescenta, para cada palavra composta (camelCase, snake_case,
// dígitos), os sub-tokens em minúsculas com kind "part": `search date`
// passa a achar `visitDate` e `VISIT_DATE`.
func WithParts(words []Word) []Word {
	seen := map[Word]bool{}
	out := words
	for _, w := range words {
		if w.Kind == KindPart {
			continue
		}
		parts := Parts(w.Text)
		if len(parts) < 2 && (len(parts) == 0 || parts[0] == w.Text) {
			continue // palavra simples em minúsculas: a linha exata já basta
		}
		for _, p := range parts {
			pw := Word{Text: p, Line: w.Line, Kind: KindPart}
			if seen[pw] || p == w.Text {
				continue
			}
			seen[pw] = true
			out = append(out, pw)
		}
	}
	return out
}

// Parts separa um identificador nos seus pedaços, em minúsculas:
// `visitDate` -> [visit date], `VISIT_DATE` -> [visit date],
// `getHTTPResponse2` -> [get http response 2]. Pedaços de uma letra e
// palavras simples devolvem uma lista de um elemento (ou vazia).
func Parts(word string) []string {
	var parts []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 || (len(cur) == 1 && unicode.IsDigit(cur[0])) {
			parts = append(parts, strings.ToLower(string(cur)))
		}
		cur = cur[:0]
	}
	runes := []rune(word)
	for i, r := range runes {
		switch {
		case r == '_' || r == '$':
			flush()
		case i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])):
			flush()
			cur = append(cur, r)
		case i > 0 && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]):
			// fim de uma sigla: HTTPResponse -> HTTP | Response
			flush()
			cur = append(cur, r)
		case i > 0 && unicode.IsDigit(r) != unicode.IsDigit(runes[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return parts
}

// skipString termina no delimitador ou na quebra de linha: uma string
// não terminada não pode engolir o resto do arquivo. As palavras do
// conteúdo entram no índice com kind "string".
func (s *state) skipString(quote byte) {
	s.pos++
	for s.pos < len(s.src) {
		switch c := s.src[s.pos]; {
		case c == '\\':
			s.pos += 2
		case c == quote:
			s.pos++
			return
		case c == '\n':
			return
		case isIdentStart(s.src, s.pos):
			s.readWord(KindString)
		default:
			s.pos++
		}
	}
}

// skipRawString consome uma raw string Go (crases, sem escapes), guardando
// as palavras como string.
func (s *state) skipRawString() {
	s.pos++
	for s.pos < len(s.src) {
		switch {
		case s.src[s.pos] == '`':
			s.pos++
			return
		case s.src[s.pos] == '\n':
			s.line++
			s.pos++
		case isIdentStart(s.src, s.pos):
			s.readWord(KindString)
		default:
			s.pos++
		}
	}
}

// skipTextBlock consome um text block Java (""") ou uma string tripla
// Python (""" ou ”'), guardando as palavras como string.
func (s *state) skipTextBlock(quote byte) {
	s.pos += 3
	for s.pos < len(s.src) {
		switch {
		case s.src[s.pos] == quote && s.peek(1) == quote && s.peek(2) == quote:
			s.pos += 3
			return
		case s.src[s.pos] == '\n':
			s.line++
			s.pos++
		case isIdentStart(s.src, s.pos):
			s.readWord(KindString)
		default:
			s.pos++
		}
	}
}

// skipNumber consome literais numéricos inteiros para que "0xFF" ou "1e10"
// não virem identificadores "xFF" e "e10".
func (s *state) skipNumber() {
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		if isASCIIAlnum(c) || c == '_' || c == '.' {
			s.pos++
			continue
		}
		return
	}
}

func (s *state) readIdentifier() {
	s.readWord(KindIdent)
}

// readWord consome um token de identificador e o registra com o kind dado.
// Keywords ficam de fora nos dois casos: em strings elas são só prosa.
func (s *state) readWord(kind string) {
	start := s.pos
	for s.pos < len(s.src) && isIdentPart(s.src, s.pos) {
		_, size := utf8.DecodeRune(s.src[s.pos:])
		s.pos += size
	}
	text := string(s.src[start:s.pos])
	if s.keywords[text] {
		return
	}
	w := Word{Text: text, Line: s.line, Kind: kind}
	if s.seen[w] {
		return
	}
	s.seen[w] = true
	s.words = append(s.words, w)
}

func (s *state) peek(offset int) byte {
	if s.pos+offset >= len(s.src) {
		return 0
	}
	return s.src[s.pos+offset]
}

func isIdentStart(src []byte, pos int) bool {
	c := src[pos]
	if c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
		return true
	}
	if c < utf8.RuneSelf {
		return false
	}
	r, _ := utf8.DecodeRune(src[pos:])
	return unicode.IsLetter(r)
}

func isIdentPart(src []byte, pos int) bool {
	c := src[pos]
	if c >= '0' && c <= '9' {
		return true
	}
	if c < utf8.RuneSelf {
		return isIdentStart(src, pos)
	}
	r, _ := utf8.DecodeRune(src[pos:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isASCIIAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
