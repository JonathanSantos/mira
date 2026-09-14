// Command gotruth extrai as definições e referências de um módulo Go com
// go/types, o mesmo verificador de tipos do compilador. É o gabarito do
// benchmark: precision e recall das ferramentas são medidos contra esta saída.
//
// Uso: gotruth -root <módulo> -out <diretório>  (grava defs.jsonl e refs.jsonl)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Definition é um símbolo declarado no repositório que pode ser referenciado
// de outro lugar: funções, métodos, tipos, campos e nomes de pacote.
type Definition struct {
	ID        string `json:"id"` // arquivo:linha:coluna do nome
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Container string `json:"container,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Col       int    `json:"col"`
}

// Reference é um uso de uma definição do repositório.
type Reference struct {
	Def  string `json:"def"`
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func main() {
	root := flag.String("root", ".", "module root to analyze")
	out := flag.String("out", ".", "directory for defs.jsonl and refs.jsonl")
	flag.Parse()
	if err := run(*root, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gotruth:", err)
		os.Exit(1)
	}
}

func run(root, out string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving root: %w", err)
	}
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   abs,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}
	c := &collector{root: abs, defs: map[string]Definition{}, refs: map[Reference]bool{}, fieldOwner: map[string]string{}}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			fmt.Fprintln(os.Stderr, "warning:", e)
		}
		if p.TypesInfo == nil {
			continue
		}
		c.indexFields(p)
	}
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for ident, obj := range p.TypesInfo.Defs {
			c.define(p.Fset, ident, obj)
		}
	}
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for ident, obj := range p.TypesInfo.Uses {
			c.use(p.Fset, ident, obj)
		}
	}
	return c.write(out)
}

// collector acumula definições e referências; pacotes de teste repetem os
// mesmos objetos, então tudo é chaveado pela posição no arquivo.
type collector struct {
	root       string
	defs       map[string]Definition
	refs       map[Reference]bool
	fieldOwner map[string]string // posição do campo -> struct que o declara
}

func (c *collector) position(fset *token.FileSet, pos token.Pos) (string, int, int, bool) {
	p := fset.Position(pos)
	rel, err := filepath.Rel(c.root, p.Filename)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", 0, 0, false
	}
	return filepath.ToSlash(rel), p.Line, p.Column, true
}

func (c *collector) indexFields(p *packages.Package) {
	scope := p.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		st, ok := tn.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			if file, line, col, ok := c.position(p.Fset, st.Field(i).Pos()); ok {
				c.fieldOwner[fmt.Sprintf("%s:%d:%d", file, line, col)] = name
			}
		}
	}
}

func (c *collector) define(fset *token.FileSet, ident *ast.Ident, obj types.Object) {
	if obj == nil {
		return
	}
	kind, container := classify(obj)
	if kind == "" {
		return
	}
	file, line, col, ok := c.position(fset, ident.Pos())
	if !ok {
		return
	}
	id := fmt.Sprintf("%s:%d:%d", file, line, col)
	if kind == "field" {
		container = c.fieldOwner[id]
	}
	c.defs[id] = Definition{ID: id, Name: obj.Name(), Kind: kind, Container: container, File: file, Line: line, Col: col}
}

func (c *collector) use(fset *token.FileSet, ident *ast.Ident, obj types.Object) {
	if obj == nil {
		return
	}
	obj = origin(obj)
	defFile, defLine, defCol, ok := c.position(fset, obj.Pos())
	if !ok {
		return
	}
	id := fmt.Sprintf("%s:%d:%d", defFile, defLine, defCol)
	if _, known := c.defs[id]; !known {
		return
	}
	file, line, col, ok := c.position(fset, ident.Pos())
	if !ok || (file == defFile && line == defLine && col == defCol) {
		return
	}
	c.refs[Reference{Def: id, File: file, Line: line, Col: col}] = true
}

// origin troca a instância de um genérico pela declaração original.
func origin(obj types.Object) types.Object {
	switch o := obj.(type) {
	case *types.Func:
		return o.Origin()
	case *types.Var:
		return o.Origin()
	}
	return obj
}

// classify diz o tipo do símbolo e, para métodos, o receptor. Locais,
// parâmetros e parâmetros de tipo ficam de fora: as ferramentas medidas não
// os tratam como símbolos.
func classify(obj types.Object) (string, string) {
	switch o := obj.(type) {
	case *types.Func:
		sig, _ := o.Type().(*types.Signature)
		if sig != nil && sig.Recv() != nil {
			return "method", receiverName(sig.Recv().Type())
		}
		return "function", ""
	case *types.TypeName:
		if o.Pkg() == nil || o.Parent() != o.Pkg().Scope() {
			return "", ""
		}
		switch o.Type().Underlying().(type) {
		case *types.Struct:
			return "struct", ""
		case *types.Interface:
			return "interface", ""
		}
		return "type", ""
	case *types.Var:
		if o.IsField() && !o.Embedded() {
			return "field", ""
		}
		if o.Pkg() != nil && o.Parent() == o.Pkg().Scope() {
			return "var", ""
		}
	case *types.Const:
		if o.Pkg() != nil && o.Parent() == o.Pkg().Scope() {
			return "const", ""
		}
	}
	return "", ""
}

func receiverName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return ""
}

func (c *collector) write(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", out, err)
	}
	defs := make([]Definition, 0, len(c.defs))
	for _, d := range c.defs {
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	refs := make([]Reference, 0, len(c.refs))
	for r := range c.refs {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.Def != b.Def {
			return a.Def < b.Def
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	if err := writeJSONL(filepath.Join(out, "defs.jsonl"), defs); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(out, "refs.jsonl"), refs); err != nil {
		return err
	}
	fmt.Printf("gotruth: %d definitions, %d references\n", len(defs), len(refs))
	return nil
}

func writeJSONL[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
}
