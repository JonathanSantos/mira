package graph

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/store"
)

// Reference é uma ocorrência do nome. Text é a linha do fonte, que vale
// mais para o agente do que os metadados do alvo; Target só aparece quando
// há mais de um alvo possível ou a ref não está resolvida.
type Reference struct {
	Line           int    `json:"line"`
	Kind           string `json:"kind"`
	Resolution     string `json:"resolution,omitempty"` // omitido quando resolvido
	Target         string `json:"target,omitempty"`     // nome do alvo, quando há mais de um
	Container      string `json:"container,omitempty"`  // símbolo que envolve a ref
	ContainerStart int    `json:"container_start,omitempty"`
	ContainerEnd   int    `json:"container_end,omitempty"`
	Text           string `json:"text"`
}

// RefFile agrupa as referências de um arquivo.
type RefFile struct {
	File string      `json:"file"`
	Refs []Reference `json:"refs"`
}

// RefsResult agrupa as referências de um nome por arquivo. Targets lista uma
// vez os símbolos para os quais as refs resolveram; Candidates, as
// definições possíveis das refs ambíguas.
type RefsResult struct {
	Name       string         `json:"name"`
	Total      int            `json:"total"`
	Truncated  bool           `json:"truncated,omitempty"`
	Targets    []SymbolRef    `json:"targets,omitempty"`
	Candidates []SymbolRef    `json:"candidates,omitempty"`
	Files      []RefFile      `json:"files"`
	Summary    map[string]int `json:"summary"`
	ByKind     map[string]int `json:"by_kind"`
	// Unmatched conta, numa consulta `Container.member`, as refs com o mesmo
	// nome que podem ser dele mas não resolveram para ele (ambíguas ou sem
	// resolução); as resolvidas para outro símbolo não entram.
	Unmatched map[string]int `json:"unmatched,omitempty"`
}

// RefsOptions filtra e limita a listagem de referências.
type RefsOptions struct {
	Path         string // prefixo ou glob de caminho
	ExcludeTests bool
	Kind         string // só refs deste kind (call, method, property, type, ...)
	Limit        int    // máximo de referências devolvidas (default 200)
}

const defaultRefsLimit = 200

// RefsMany roda Refs para cada nome, na ordem dada; o limite vale por nome.
func (g *Graph) RefsMany(names []string, opts RefsOptions) ([]RefsResult, error) {
	out := make([]RefsResult, 0, len(names))
	for _, name := range names {
		res, err := g.Refs(name, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// SplitNames aceita "a, b" e "a b": é como um agente passa vários nomes num
// campo só.
func SplitNames(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// Refs lista as referências a um nome, marcando a resolução.
func (g *Graph) Refs(name string, opts RefsOptions) (RefsResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = defaultRefsLimit
	}
	refName, defs, err := g.refTargets(name)
	if err != nil {
		return RefsResult{}, err
	}
	refs, err := g.store.RefsByName(refName)
	if err != nil {
		return RefsResult{}, err
	}
	_, glob := splitPath(opts.Path)
	prefix := opts.Path
	if glob != "" {
		prefix = ""
	}
	result := RefsResult{Name: name, Files: []RefFile{}, Summary: map[string]int{}, ByKind: map[string]int{}}
	targets := map[int64]SymbolRef{}
	var targetOrder []int64
	only := map[int64]bool{} // `Container.member`: só as refs resolvidas para estas definições
	for _, d := range defs {
		target, err := g.ref(d)
		if err != nil {
			return RefsResult{}, err
		}
		only[d.ID], targets[d.ID] = true, target
		targetOrder = append(targetOrder, d.ID)
	}
	ambiguous := false
	groups := map[string]*RefFile{}
	var order []string
	listed := 0
	for _, r := range refs {
		file, _, err := g.store.FileByID(r.FileID)
		if err != nil {
			return RefsResult{}, err
		}
		if opts.Kind != "" && r.Kind != opts.Kind {
			continue
		}
		if prefix != "" && !strings.HasPrefix(file.Path, prefix) || !pathAllowed(file.Path, glob, opts.ExcludeTests) {
			continue
		}
		if len(defs) > 0 && (r.ResolvedSymbolID == nil || !only[*r.ResolvedSymbolID]) {
			if r.Resolution == store.Ambiguous || r.Resolution == store.Unresolved {
				if result.Unmatched == nil {
					result.Unmatched = map[string]int{}
				}
				result.Unmatched[r.Resolution]++
			}
			continue
		}
		result.Total++
		result.Summary[r.Resolution]++
		result.ByKind[r.Kind]++
		if r.Resolution == store.Ambiguous {
			ambiguous = true
		}
		if listed >= opts.Limit {
			result.Truncated = true
			continue
		}
		listed++
		ref := Reference{Line: r.Line, Kind: r.Kind}
		if r.Resolution != store.Resolved {
			ref.Resolution = r.Resolution
		}
		if r.ResolvedSymbolID != nil {
			target, known := targets[*r.ResolvedSymbolID]
			if !known {
				sym, ok, err := g.store.SymbolByID(*r.ResolvedSymbolID)
				if err == nil && ok {
					if target, err = g.ref(sym); err == nil {
						targets[sym.ID] = target
						targetOrder = append(targetOrder, sym.ID)
					}
				}
			}
			ref.Target = target.Name
		}
		g.fillContainer(&ref, r)
		lines, err := g.lines(file.Path)
		if err != nil {
			return RefsResult{}, err
		}
		ref.Text = strings.TrimSpace(slice(lines, r.Line, r.Line))
		group, ok := groups[file.Path]
		if !ok {
			group = &RefFile{File: file.Path}
			groups[file.Path] = group
			order = append(order, file.Path)
		}
		group.Refs = append(group.Refs, ref)
	}
	for _, p := range order {
		result.Files = append(result.Files, *groups[p])
	}
	for _, id := range targetOrder {
		result.Targets = append(result.Targets, targets[id])
	}
	// Com um único alvo, repetir o nome em cada ref é ruído.
	if len(result.Targets) == 1 {
		for i := range result.Files {
			for j := range result.Files[i].Refs {
				result.Files[i].Refs[j].Target = ""
			}
		}
	}
	if ambiguous {
		if result.Candidates, err = g.candidates(name); err != nil {
			return RefsResult{}, err
		}
	}
	return result, nil
}

// fillContainer anota o símbolo que envolve a ref e o range dele, para que
// "quem chama" já venha com arquivo:início-fim sem outra chamada.
func (g *Graph) fillContainer(ref *Reference, r store.Ref) {
	if r.ContainerSymbol == nil {
		return
	}
	c, ok, err := g.store.SymbolByID(*r.ContainerSymbol)
	if err != nil || !ok {
		return
	}
	ref.Container, ref.ContainerStart, ref.ContainerEnd = c.QualifiedName, c.StartLine, c.EndLine
}

// refTargets trata `Container.member` e `Container.member:linha`: refs
// guardam só o nome, então a consulta vira o nome do membro, restrita às
// refs resolvidas para as definições que casam (as mesmas do resolve; com
// linha, a sobrecarga que a contém). Sem ponto, ou sem definição que case,
// o nome segue como está.
func (g *Graph) refTargets(query string) (string, []store.Symbol, error) {
	name, line := query, 0
	if i := strings.LastIndex(query, ":"); i > 0 {
		if n, err := strconv.Atoi(query[i+1:]); err == nil {
			name, line = query[:i], n
		}
	}
	dot := strings.LastIndex(name, ".")
	if dot <= 0 || dot == len(name)-1 {
		return query, nil, nil
	}
	defs, err := g.lookup(name)
	if err != nil {
		return query, nil, err
	}
	if line > 0 {
		defs = slices.DeleteFunc(defs, func(s store.Symbol) bool { return line < s.StartLine || line > s.EndLine })
	}
	if len(defs) == 0 {
		return query, nil, nil
	}
	return name[dot+1:], defs, nil
}

// candidates são as definições exportadas com esse nome, listadas quando há
// refs ambíguas.
func (g *Graph) candidates(name string) ([]SymbolRef, error) {
	symbols, err := g.store.SymbolsByName(name)
	if err != nil {
		return nil, err
	}
	var out []SymbolRef
	for _, s := range symbols {
		if !s.Exported || s.Kind == extract.KindConstructor {
			continue
		}
		ref, err := g.ref(s)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// Outline é um símbolo na visão de arquivo: sem caminho (está no topo do
// resultado), sem nome qualificado (a indentação diz o container) e com a
// assinatura cortada. Children são métodos, campos e funções aninhadas.
type Outline struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	StartLine   int       `json:"start_line"`
	EndLine     int       `json:"end_line"`
	Exported    bool      `json:"exported,omitempty"`
	JSX         bool      `json:"jsx,omitempty"`
	Annotations []string  `json:"annotations,omitempty"`
	Signature   string    `json:"signature,omitempty"`
	Children    []Outline `json:"children,omitempty"`
}

// SymbolsResult é a saída de symbols. ParseError avisa que a árvore do
// arquivo teve erro e a lista pode estar incompleta.
type SymbolsResult struct {
	File       string    `json:"file"`
	Count      int       `json:"count"`
	ParseError bool      `json:"parse_error,omitempty"`
	Hint       string    `json:"hint,omitempty"`
	Symbols    []Outline `json:"symbols"`
}

const (
	parseErrorHint = "file had parse errors; symbols may be missing, use search --regex and snippet"
	// maxSignature corta assinaturas de generics de 300 caracteres no outline;
	// o resolve do símbolo traz a inteira.
	maxSignature = 100
)

// Symbols lista os símbolos definidos num arquivo como outline aninhado.
// Com compact, devolve só nome, kind e linhas.
func (g *Graph) Symbols(file string, compact bool) (SymbolsResult, error) {
	rel := g.relative(file)
	f, ok, err := g.store.FileByPath(rel)
	if err != nil {
		return SymbolsResult{}, err
	}
	if !ok {
		return SymbolsResult{}, fmt.Errorf("file %s is not indexed (use files to list indexed paths)", rel)
	}
	symbols, err := g.store.SymbolsOfFile(f.ID)
	if err != nil {
		return SymbolsResult{}, err
	}
	result := SymbolsResult{File: rel, Count: len(symbols), Symbols: []Outline{}, ParseError: f.ParseError}
	if f.ParseError {
		result.Hint = parseErrorHint
	}
	if f.Lang == lang.Text {
		result.Hint = "text file: indexed by words only (search finds them); no symbols"
	}
	result.Symbols = outline(symbols, compact)
	return result, nil
}

// outline aninha os símbolos pelo container: o pai é o símbolo com o nome
// do container cujo range envolve o filho (o mais próximo acima vence). Sem
// pai conhecido, o símbolo fica no topo.
func outline(symbols []store.Symbol, compact bool) []Outline {
	byName := map[string][]int{}
	for i, s := range symbols {
		byName[s.Name] = append(byName[s.Name], i)
	}
	children := make([][]int, len(symbols))
	var roots []int
	for i, s := range symbols {
		parent := -1
		for _, j := range byName[s.Container] {
			if j != i && symbols[j].StartLine <= s.StartLine && s.EndLine <= symbols[j].EndLine {
				parent = j
			}
		}
		if s.Container == "" || parent == -1 {
			roots = append(roots, i)
			continue
		}
		children[parent] = append(children[parent], i)
	}
	var build func(i int) Outline
	build = func(i int) Outline {
		s := symbols[i]
		o := Outline{Name: s.Name, Kind: s.Kind, StartLine: s.StartLine, EndLine: s.EndLine, Exported: s.Exported, JSX: s.JSX}
		if !compact {
			o.Signature = truncate(s.Signature, maxSignature)
			o.Annotations = s.Annotations
		}
		for _, c := range children[i] {
			o.Children = append(o.Children, build(c))
		}
		return o
	}
	out := []Outline{}
	for _, i := range roots {
		out = append(out, build(i))
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// SnippetResult é a saída de snippet. Stale avisa que o arquivo mudou
// desde a última indexação.
type SnippetResult struct {
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Text        string `json:"text"`
	Stale       bool   `json:"stale,omitempty"`
	Symbol      string `json:"symbol,omitempty"` // símbolo que envolve a primeira linha do range
	SymbolStart int    `json:"symbol_start,omitempty"`
	SymbolEnd   int    `json:"symbol_end,omitempty"`
	// Plain desliga os números de linha no modo texto; por padrão cada
	// linha sai prefixada com o número, para que o agente cite sem contar.
	Plain bool `json:"-"`
}

// SnippetOfSymbol devolve o range completo de um símbolo do arquivo, para
// não obrigar o agente a saber números de linha.
func (g *Graph) SnippetOfSymbol(file, name string) (SnippetResult, error) {
	rel := g.relative(file)
	f, ok, err := g.store.FileByPath(rel)
	if err != nil {
		return SnippetResult{}, err
	}
	if !ok {
		return SnippetResult{}, fmt.Errorf("file %s is not indexed", rel)
	}
	symbols, err := g.store.SymbolsOfFile(f.ID)
	if err != nil {
		return SnippetResult{}, err
	}
	for _, s := range symbols {
		if s.Name == name || s.QualifiedName == name {
			return g.Snippet(rel, s.StartLine, s.EndLine)
		}
	}
	return SnippetResult{}, fmt.Errorf("symbol %s not found in %s (use symbols to list them)", name, rel)
}

// Snippet devolve exatamente o range pedido.
func (g *Graph) Snippet(file string, start, end int) (SnippetResult, error) {
	if start < 1 || end < start {
		return SnippetResult{}, fmt.Errorf("invalid range %d-%d", start, end)
	}
	rel := g.relative(file)
	lines, err := g.lines(rel)
	if err != nil {
		return SnippetResult{}, err
	}
	if start > len(lines) {
		return SnippetResult{}, fmt.Errorf("%s has only %d lines", rel, len(lines))
	}
	result := SnippetResult{File: rel, StartLine: start, EndLine: min(end, len(lines)), Text: slice(lines, start, end)}
	if f, ok, err := g.store.FileByPath(rel); err == nil && ok {
		result.Stale = g.isStale(f)
		if sym, ok, err := g.store.SymbolAt(f.ID, start); err == nil && ok {
			result.Symbol, result.SymbolStart, result.SymbolEnd = sym.QualifiedName, sym.StartLine, sym.EndLine
		}
	}
	return result, nil
}
