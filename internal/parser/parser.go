// Package parser é o adaptador fino sobre o tree-sitter (binding oficial,
// cgo): detecta a linguagem pela extensão, escolhe a gramática e expõe uma
// API de nó pequena o bastante para os extractors não dependerem do runtime.
package parser

import (
	"fmt"
	"path/filepath"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/JonathanSantos/mira/internal/lang"
)

// extensions mapeia extensão -> linguagem. ".js" e ".jsx" entram na família
// TypeScript porque a gramática tsx é um superconjunto do JavaScript.
var extensions = map[string]lang.Lang{
	".ts": lang.TypeScript, ".mts": lang.TypeScript, ".cts": lang.TypeScript, ".tsx": lang.TypeScript,
	".js": lang.JavaScript, ".mjs": lang.JavaScript, ".cjs": lang.JavaScript, ".jsx": lang.JavaScript,
	".java": lang.Java,
	".py":   lang.Python, ".pyi": lang.Python,
	".go": lang.Go,
}

// Detect mapeia o caminho para uma linguagem suportada.
func Detect(path string) (lang.Lang, bool) {
	l, ok := extensions[filepath.Ext(path)]
	return l, ok
}

// Extensions lista as extensões de arquivo das linguagens dadas.
func Extensions(langs ...lang.Lang) map[string]bool {
	out := map[string]bool{}
	for ext, l := range extensions {
		for _, want := range langs {
			if l == want {
				out[ext] = true
			}
		}
	}
	return out
}

// Parser segura as gramáticas carregadas. Cada Parse cria um parser do
// runtime, já que ele não é seguro para uso concorrente; as gramáticas são.
type Parser struct {
	typescript *tree_sitter.Language
	tsx        *tree_sitter.Language
	java       *tree_sitter.Language
	python     *tree_sitter.Language
	golang     *tree_sitter.Language
}

func New() *Parser {
	return &Parser{
		typescript: tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
		tsx:        tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()),
		java:       tree_sitter.NewLanguage(tree_sitter_java.Language()),
		python:     tree_sitter.NewLanguage(tree_sitter_python.Language()),
		golang:     tree_sitter.NewLanguage(tree_sitter_go.Language()),
	}
}

// grammarFor escolhe a gramática de uma linguagem (tsx para a família TS/JS).
func (p *Parser) grammarFor(l lang.Lang) *tree_sitter.Language {
	switch l {
	case lang.Java:
		return p.java
	case lang.Python:
		return p.python
	case lang.Go:
		return p.golang
	}
	return p.tsx
}

// ParseFile escolhe a gramática pela extensão: ".ts"/".mts"/".cts" usam a
// gramática typescript (aceita o cast `<T>x`); todo o resto da família usa
// tsx (aceita JSX). As duas têm os mesmos tipos de nó.
func (p *Parser) ParseFile(path string, src []byte) (*Tree, error) {
	l, ok := Detect(path)
	if !ok {
		return nil, fmt.Errorf("parsing %s: unsupported extension", path)
	}
	grammar := p.grammarFor(l)
	switch filepath.Ext(path) {
	case ".ts", ".mts", ".cts":
		grammar = p.typescript
	}
	return p.parse(grammar, l, src)
}

// Parse usa a gramática padrão da linguagem (tsx para a família TS/JS).
func (p *Parser) Parse(l lang.Lang, src []byte) (*Tree, error) {
	return p.parse(p.grammarFor(l), l, src)
}

func (p *Parser) parse(grammar *tree_sitter.Language, l lang.Lang, src []byte) (*Tree, error) {
	ts := tree_sitter.NewParser()
	defer ts.Close()
	if err := ts.SetLanguage(grammar); err != nil {
		return nil, fmt.Errorf("loading %s grammar: %w", l, err)
	}
	tree := ts.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parsing %s: parser returned no tree", l)
	}
	return &Tree{tree: tree, src: src, HasError: tree.RootNode().HasError()}, nil
}

// Tree é uma árvore parseada com o fonte que a gerou. Release libera a
// memória do runtime; nós não podem ser usados depois disso.
type Tree struct {
	tree     *tree_sitter.Tree
	src      []byte
	HasError bool
}

func (t *Tree) Root() Node     { return Node{n: t.tree.RootNode(), t: t} }
func (t *Tree) Source() []byte { return t.src }
func (t *Tree) Release()       { t.tree.Close() }

// Node é um nó da árvore. O valor zero representa "nenhum nó", e todos os
// getters aceitam esse valor devolvendo zero, para que um filho ausente
// nunca derrube um worker.
type Node struct {
	n *tree_sitter.Node
	t *Tree
}

func (n Node) IsNil() bool   { return n.n == nil }
func (n Node) IsNamed() bool { return n.n != nil && n.n.IsNamed() }

func (n Node) Type() string {
	if n.n == nil {
		return ""
	}
	return n.n.Kind()
}

func (n Node) Text() string {
	if n.n == nil {
		return ""
	}
	return n.n.Utf8Text(n.t.src)
}

func (n Node) StartByte() int { return n.num(func(r *tree_sitter.Node) uint { return r.StartByte() }) }
func (n Node) EndByte() int   { return n.num(func(r *tree_sitter.Node) uint { return r.EndByte() }) }
func (n Node) StartLine() int {
	return n.num(func(r *tree_sitter.Node) uint { return r.StartPosition().Row + 1 })
}
func (n Node) EndLine() int {
	return n.num(func(r *tree_sitter.Node) uint { return r.EndPosition().Row + 1 })
}
func (n Node) StartCol() int {
	return n.num(func(r *tree_sitter.Node) uint { return r.StartPosition().Column + 1 })
}
func (n Node) HasError() bool { return n.n != nil && n.n.HasError() }

func (n Node) num(get func(*tree_sitter.Node) uint) int {
	if n.n == nil {
		return 0
	}
	return int(get(n.n))
}

func (n Node) Is(types ...string) bool {
	if n.n == nil {
		return false
	}
	typ := n.Type()
	for _, t := range types {
		if typ == t {
			return true
		}
	}
	return false
}

func (n Node) Parent() Node {
	if n.n == nil {
		return Node{}
	}
	return n.wrap(n.n.Parent())
}

// Children devolve todos os filhos, inclusive nós anônimos (pontuação,
// keywords), úteis para achar "=" ou "{".
func (n Node) Children() []Node {
	if n.n == nil {
		return nil
	}
	count := n.n.ChildCount()
	out := make([]Node, 0, count)
	for i := uint(0); i < count; i++ {
		out = append(out, n.wrap(n.n.Child(i)))
	}
	return out
}

func (n Node) NamedChildren() []Node {
	if n.n == nil {
		return nil
	}
	count := n.n.NamedChildCount()
	out := make([]Node, 0, count)
	for i := uint(0); i < count; i++ {
		out = append(out, n.wrap(n.n.NamedChild(i)))
	}
	return out
}

// Child devolve o primeiro filho nomeado de um dos tipos, ou o nó nulo.
// Os extractors procuram por tipo em vez de campo (name:, body:) para
// funcionar igual nas gramáticas typescript e tsx.
func (n Node) Child(types ...string) Node {
	for _, c := range n.NamedChildren() {
		if c.Is(types...) {
			return c
		}
	}
	return Node{}
}

// ChildrenOf devolve todos os filhos nomeados de um dos tipos.
func (n Node) ChildrenOf(types ...string) []Node {
	var out []Node
	for _, c := range n.NamedChildren() {
		if c.Is(types...) {
			out = append(out, c)
		}
	}
	return out
}

// AnonChild devolve o primeiro filho anônimo com o texto dado ("{", "=").
func (n Node) AnonChild(text string) Node {
	for _, c := range n.Children() {
		if !c.IsNamed() && c.Text() == text {
			return c
		}
	}
	return Node{}
}

// Walk percorre a subárvore em pré-ordem; fn devolve false para não descer.
func (n Node) Walk(fn func(Node) bool) {
	if n.n == nil || !fn(n) {
		return
	}
	for _, c := range n.NamedChildren() {
		c.Walk(fn)
	}
}

// Ancestor sobe até achar um ancestral de um dos tipos.
func (n Node) Ancestor(types ...string) Node {
	for p := n.Parent(); !p.IsNil(); p = p.Parent() {
		if p.Is(types...) {
			return p
		}
	}
	return Node{}
}

func (n Node) wrap(raw *tree_sitter.Node) Node {
	if raw == nil {
		return Node{}
	}
	return Node{n: raw, t: n.t}
}

// NamedDescendantForByteRange devolve o menor nó nomeado que cobre o range
// de bytes [start, end): é como o graph reencontra o nó de um símbolo a
// partir do span gravado no índice.
func (n Node) NamedDescendantForByteRange(start, end int) Node {
	if n.n == nil || start < 0 || end < start {
		return Node{}
	}
	return n.wrap(n.n.NamedDescendantForByteRange(uint(start), uint(end)))
}
