// Package graph é a API de leitura do índice, compartilhada por CLI e MCP.
// Toda saída de código é por range de linhas, nunca o arquivo inteiro.
package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/hasher"
	"github.com/JonathanSantos/mira/internal/parser"
	"github.com/JonathanSantos/mira/internal/store"
)

// snippetLines é o tamanho da prévia devolvida por resolve sem --include-body.
const snippetLines = 8

// aroundContext é a janela padrão de --around, para cada lado.
const aroundContext = 8

// shortBodyLines: até este tamanho o resolve traz o corpo inteiro mesmo sem
// --include-body. Uma prévia de 8 linhas de um método de 16 custava uma
// chamada a mais, e o corpo inteiro cabe no orçamento.
const shortBodyLines = 30

// Graph consulta um índice já aberto.
type Graph struct {
	root  string
	store *store.Store
	files map[string][]string // cache de linhas por arquivo, numa mesma consulta
	ts    *parser.Parser      // criado na primeira consulta com esqueleto
}

func New(root string, s *store.Store) *Graph {
	return &Graph{root: root, store: s, files: map[string][]string{}}
}

// SymbolInfo é a visão pública de um símbolo. O id interno fica de fora
// do JSON: o agente localiza símbolos por nome ou arquivo:linha.
type SymbolInfo struct {
	ID            int64    `json:"-"`
	Name          string   `json:"name"`
	QualifiedName string   `json:"qualified_name,omitempty"` // só quando difere de name
	Kind          string   `json:"kind"`
	Container     string   `json:"container,omitempty"`
	Exported      bool     `json:"exported"`
	JSX           bool     `json:"jsx,omitempty"`
	Signature     string   `json:"signature,omitempty"`
	Annotations   []string `json:"annotations,omitempty"`
	File          string   `json:"file,omitempty"`
	StartLine     int      `json:"start_line"`
	EndLine       int      `json:"end_line"`
}

// Label é o nome mais específico disponível (qualificado ou simples).
func (s SymbolInfo) Label() string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

// SymbolRef é a forma curta de apontar para um símbolo: só o que o agente
// precisa para citar ou pedir um resolve/snippet depois. Sem assinatura:
// em listas de callers/callees ela era 30-45% dos bytes.
type SymbolRef struct {
	ID        int64  `json:"-"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Related é um símbolo ligado por edges, com a expansão recursiva por depth.
type Related struct {
	SymbolRef
	Callers []Related `json:"callers,omitempty"`
	Callees []Related `json:"callees,omitempty"`
}

// Definition é uma definição resolvida com contexto de grafo. ReferencedBy
// são símbolos que usam a definição sem chamá-la (expõem num objeto
// retornado, usam como tipo); Refs, quando pedido, são as ocorrências
// resolvidas para ela, por arquivo.
type Definition struct {
	SymbolInfo
	ParseError    bool          `json:"parse_error,omitempty"`
	Snippet       string        `json:"snippet"`
	Body          string        `json:"body,omitempty"`
	Window        string        `json:"window,omitempty"`       // trecho em volta de --around, no lugar do corpo
	WindowStart   int           `json:"window_start,omitempty"` // primeira linha de Window
	Members       []Outline     `json:"members,omitempty"`      // classes e interfaces longas: os membros em vez do corpo
	Skeleton      *SkeletonInfo `json:"skeleton,omitempty"`
	Callers       []Related     `json:"callers"`
	Callees       []Related     `json:"callees"`
	ReferencedBy  []SymbolRef   `json:"referenced_by,omitempty"`
	Extends       []SymbolRef   `json:"extends,omitempty"`
	Implements    []SymbolRef   `json:"implements,omitempty"`
	ExtendedBy    []SymbolRef   `json:"extended_by,omitempty"`
	ImplementedBy []SymbolRef   `json:"implemented_by,omitempty"`
	AnnotatedBy   []SymbolRef   `json:"annotated_by,omitempty"`
	Refs          *RefsResult   `json:"refs,omitempty"`
}

// ResolveResult lista todas as definições que casam com a consulta.
type ResolveResult struct {
	Query       string       `json:"query"`
	Definitions []Definition `json:"definitions"`
	Hint        string       `json:"hint,omitempty"`
}

// ResolveOptions controla o que o resolve traz e quais definições aceita.
type ResolveOptions struct {
	Depth           int    // níveis de callers/callees (default 1)
	IncludeBody     bool   // corpo completo da definição
	IncludeSkeleton bool   // esqueleto: retornos, throws, locais e usos por origem, sem o corpo
	Around          int    // linha dentro da definição em volta da qual mostrar um trecho
	Context         int    // linhas antes e depois de Around (default 8)
	IncludeRefs     bool   // referências resolvidas para a definição, por arquivo
	Kind            string // só definições deste kind (class, function, type, ...)
	Path            string // só definições sob este prefixo ou glob
	ExcludeTests    bool   // ignora definições, callers e refs em arquivos de teste
	MaxRefs         int    // máximo de refs listadas com IncludeRefs (default 50)
}

const (
	defaultResolveRefs = 50
	// maxReferencedBy limita a lista de símbolos que referenciam a definição
	// sem chamá-la (expõem num objeto, usam como tipo).
	maxReferencedBy = 20
)

// Resolve aceita nome, nome qualificado (Order.total, com.acme.X) ou
// arquivo:linha. Definições em código de produção vêm antes das de teste.
func (g *Graph) Resolve(query string, opts ResolveOptions) (ResolveResult, error) {
	if opts.Depth == 0 && !opts.IncludeRefs {
		opts.Depth = 1
	}
	symbols, err := g.lookup(query)
	if err != nil {
		return ResolveResult{}, err
	}
	result := ResolveResult{Query: query, Definitions: []Definition{}}
	filtered, err := g.filterDefinitions(symbols, opts)
	if err != nil {
		return ResolveResult{}, err
	}
	for _, sym := range filtered {
		def, err := g.definition(sym, opts)
		if err != nil {
			return ResolveResult{}, err
		}
		result.Definitions = append(result.Definitions, def)
	}
	if len(result.Definitions) == 0 {
		result.Hint = "no definition with that name; try search (identifiers and string literals), search --regex, or files to browse"
		if len(symbols) > 0 {
			result.Hint = fmt.Sprintf("%d definition(s) exist but were filtered out by kind/path/exclude-tests", len(symbols))
		}
	}
	return result, nil
}

// filterDefinitions aplica kind/path/testes e ordena: produção antes de
// teste, depois caminho.
func (g *Graph) filterDefinitions(symbols []store.Symbol, opts ResolveOptions) ([]store.Symbol, error) {
	_, glob := splitPath(opts.Path)
	prefix := opts.Path
	if glob != "" {
		prefix = ""
	}
	type ranked struct {
		sym  store.Symbol
		path string
	}
	var keep []ranked
	for _, s := range symbols {
		if opts.Kind != "" && s.Kind != opts.Kind {
			continue
		}
		file, ok, err := g.store.FileByID(s.FileID)
		if err != nil {
			return nil, err
		}
		if !ok || prefix != "" && !strings.HasPrefix(file.Path, prefix) || !pathAllowed(file.Path, glob, opts.ExcludeTests) {
			continue
		}
		keep = append(keep, ranked{s, file.Path})
	}
	sort.SliceStable(keep, func(i, j int) bool {
		ti, tj := IsTestPath(keep[i].path), IsTestPath(keep[j].path)
		if ti != tj {
			return !ti
		}
		return keep[i].path < keep[j].path
	})
	out := make([]store.Symbol, 0, len(keep))
	for _, k := range keep {
		out = append(out, k.sym)
	}
	return out, nil
}

func (g *Graph) lookup(query string) ([]store.Symbol, error) {
	if file, line, ok := parseFileLine(query); ok {
		return g.symbolAt(file, line)
	}
	if strings.Contains(query, ".") {
		byQualified, err := g.store.SymbolsByQualifiedName(query)
		if err != nil {
			return nil, err
		}
		if len(byQualified) > 0 {
			return byQualified, nil
		}
		return g.byContainerAndName(query)
	}
	return g.store.SymbolsByName(query)
}

// byContainerAndName trata `Order.total` quando o qualified_name completo
// (Java) não bate: casa container + nome.
func (g *Graph) byContainerAndName(query string) ([]store.Symbol, error) {
	dot := strings.LastIndex(query, ".")
	container, name := query[:dot], query[dot+1:]
	if i := strings.LastIndex(container, "."); i >= 0 {
		container = container[i+1:]
	}
	all, err := g.store.SymbolsByName(name)
	if err != nil {
		return nil, err
	}
	var out []store.Symbol
	for _, s := range all {
		if s.Container == container {
			out = append(out, s)
		}
	}
	return out, nil
}

func (g *Graph) symbolAt(file string, line int) ([]store.Symbol, error) {
	f, ok, err := g.store.FileByPath(g.relative(file))
	if err != nil || !ok {
		return nil, err
	}
	sym, ok, err := g.store.SymbolAt(f.ID, line)
	if err != nil || !ok {
		return nil, err
	}
	return []store.Symbol{sym}, nil
}

func parseFileLine(query string) (string, int, bool) {
	i := strings.LastIndex(query, ":")
	if i <= 0 {
		return "", 0, false
	}
	line, err := strconv.Atoi(query[i+1:])
	if err != nil || line <= 0 {
		return "", 0, false
	}
	return query[:i], line, true
}

func (g *Graph) definition(sym store.Symbol, opts ResolveOptions) (Definition, error) {
	info, err := g.info(sym)
	if err != nil {
		return Definition{}, err
	}
	def := Definition{SymbolInfo: info, Callers: []Related{}, Callees: []Related{}}
	file, ok, err := g.store.FileByID(sym.FileID)
	if err != nil {
		return Definition{}, err
	}
	if !ok {
		return Definition{}, fmt.Errorf("symbol %d points to missing file %d", sym.ID, sym.FileID)
	}
	def.ParseError = file.ParseError
	lines, err := g.lines(info.File)
	if err != nil {
		return Definition{}, err
	}
	def.Snippet = slice(lines, sym.StartLine, min(sym.EndLine, sym.StartLine+snippetLines-1))
	if opts.IncludeBody || sym.EndLine-sym.StartLine+1 <= shortBodyLines {
		def.Body = slice(lines, sym.StartLine, sym.EndLine)
	}
	if opts.Around > 0 && opts.Around >= sym.StartLine && opts.Around <= sym.EndLine {
		// Uma função longa raramente precisa vir inteira: --around traz só a
		// janela em volta da linha que interessa (a de um return, de um throw).
		ctx := opts.Context
		if ctx <= 0 {
			ctx = aroundContext
		}
		def.WindowStart = max(sym.StartLine, opts.Around-ctx)
		def.Window = slice(lines, def.WindowStart, min(sym.EndLine, opts.Around+ctx))
		def.Body = ""
	}
	if def.Body == "" && isTypeKind(sym.Kind) {
		if def.Members, err = g.members(sym); err != nil {
			return Definition{}, err
		}
	}
	if opts.IncludeSkeleton {
		if def.Skeleton, err = g.skeleton(sym, file); err != nil {
			return Definition{}, err
		}
	}
	if def.Callers, err = g.related(sym.ID, opts.Depth, true, opts.ExcludeTests, map[int64]bool{sym.ID: true}); err != nil {
		return Definition{}, err
	}
	if def.Callees, err = g.related(sym.ID, opts.Depth, false, opts.ExcludeTests, map[int64]bool{sym.ID: true}); err != nil {
		return Definition{}, err
	}
	if def.ReferencedBy, err = g.referencedBy(sym.ID, def.Callers, opts.ExcludeTests); err != nil {
		return Definition{}, err
	}
	if err := g.hierarchy(&def); err != nil {
		return Definition{}, err
	}
	if opts.IncludeRefs {
		refs, err := g.refsTo(sym, opts)
		if err != nil {
			return Definition{}, err
		}
		def.Refs = &refs
	}
	return def, nil
}

// ResolveMany resolve vários nomes numa chamada, na ordem dada.
func (g *Graph) ResolveMany(queries []string, opts ResolveOptions) ([]ResolveResult, error) {
	out := make([]ResolveResult, 0, len(queries))
	for _, q := range queries {
		res, err := g.Resolve(q, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// members é o outline dos símbolos dentro de uma classe ou interface: o
// que um agente quer de um `resolve Classe` de 70 linhas em vez de 8 linhas
// de corpo.
func (g *Graph) members(sym store.Symbol) ([]Outline, error) {
	symbols, err := g.store.SymbolsOfFile(sym.FileID)
	if err != nil {
		return nil, err
	}
	var inside []store.Symbol
	for _, s := range symbols {
		if s.ID != sym.ID && s.StartByte >= sym.StartByte && s.EndByte <= sym.EndByte {
			inside = append(inside, s)
		}
	}
	return outline(inside, false), nil
}

func isTypeKind(kind string) bool {
	switch kind {
	case extract.KindClass, extract.KindInterface, extract.KindEnum, extract.KindRecord, extract.KindNamespace:
		return true
	}
	return false
}

// referencedBy lista quem referencia a definição sem chamá-la: quem a
// expõe num objeto retornado, usa como tipo ou como valor.
func (g *Graph) referencedBy(symbolID int64, callers []Related, excludeTests bool) ([]SymbolRef, error) {
	symbols, err := g.store.Callers(symbolID, store.EdgeReferences)
	if err != nil {
		return nil, err
	}
	already := map[int64]bool{symbolID: true}
	for _, c := range callers {
		already[c.ID] = true
	}
	var out []SymbolRef
	for _, s := range symbols {
		if already[s.ID] || len(out) >= maxReferencedBy {
			continue
		}
		ref, err := g.ref(s)
		if err != nil {
			return nil, err
		}
		if excludeTests && IsTestPath(ref.File) {
			continue
		}
		out = append(out, ref)
	}
	return out, nil
}

// refsTo agrupa por arquivo as ocorrências resolvidas para a definição.
func (g *Graph) refsTo(sym store.Symbol, opts ResolveOptions) (RefsResult, error) {
	limit := opts.MaxRefs
	if limit <= 0 {
		limit = defaultResolveRefs
	}
	refs, err := g.store.RefsTo(sym.ID)
	if err != nil {
		return RefsResult{}, err
	}
	result := RefsResult{Name: sym.Name, Files: []RefFile{}, Summary: map[string]int{}, ByKind: map[string]int{}}
	groups := map[string]*RefFile{}
	var order []string
	for _, r := range refs {
		file, _, err := g.store.FileByID(r.FileID)
		if err != nil {
			return RefsResult{}, err
		}
		if opts.ExcludeTests && IsTestPath(file.Path) {
			continue
		}
		result.Total++
		result.Summary[store.Resolved]++
		result.ByKind[r.Kind]++
		if result.Total > limit {
			result.Truncated = true
			continue
		}
		ref := Reference{Line: r.Line, Kind: r.Kind}
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
	return result, nil
}

type edgeQuery func(symbolID int64, kind string) ([]store.Symbol, error)

// related expande callers (edges que chegam) ou callees (edges que saem)
// até depth níveis, sem revisitar símbolos num ciclo. Produção vem antes
// de teste.
func (g *Graph) related(symbolID int64, depth int, callers, excludeTests bool, visited map[int64]bool) ([]Related, error) {
	out := []Related{}
	if depth <= 0 {
		return out, nil
	}
	query := edgeQuery(g.store.Callees)
	if callers {
		query = g.store.Callers
	}
	symbols, err := query(symbolID, store.EdgeCalls)
	if err != nil {
		return nil, err
	}
	for _, s := range symbols {
		ref, err := g.ref(s)
		if err != nil {
			return nil, err
		}
		if excludeTests && IsTestPath(ref.File) {
			continue
		}
		rel := Related{SymbolRef: ref}
		if !visited[s.ID] {
			visited[s.ID] = true
			next, err := g.related(s.ID, depth-1, callers, excludeTests, visited)
			if err != nil {
				return nil, err
			}
			if callers {
				rel.Callers = next
			} else {
				rel.Callees = next
			}
		}
		out = append(out, rel)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := IsTestPath(out[i].File), IsTestPath(out[j].File)
		return !ti && tj
	})
	return out, nil
}

func (g *Graph) hierarchy(def *Definition) error {
	var err error
	if def.Extends, err = g.infos(g.store.Callees, def.ID, store.EdgeExtends); err != nil {
		return err
	}
	if def.Implements, err = g.infos(g.store.Callees, def.ID, store.EdgeImplements); err != nil {
		return err
	}
	if def.ExtendedBy, err = g.infos(g.store.Callers, def.ID, store.EdgeExtends); err != nil {
		return err
	}
	if def.ImplementedBy, err = g.infos(g.store.Callers, def.ID, store.EdgeImplements); err != nil {
		return err
	}
	if def.AnnotatedBy, err = g.infos(g.store.Callees, def.ID, store.EdgeAnnotatedBy); err != nil {
		return err
	}
	return nil
}

func (g *Graph) infos(query edgeQuery, symbolID int64, kind string) ([]SymbolRef, error) {
	symbols, err := query(symbolID, kind)
	if err != nil {
		return nil, err
	}
	var out []SymbolRef
	for _, s := range symbols {
		ref, err := g.ref(s)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// ref monta a forma curta de um símbolo. O nome é o qualificado quando
// difere do simples (`Owner.addVisit`, `com.acme.X`), que é como o agente
// deve citá-lo.
func (g *Graph) ref(sym store.Symbol) (SymbolRef, error) {
	file, ok, err := g.store.FileByID(sym.FileID)
	if err != nil {
		return SymbolRef{}, err
	}
	if !ok {
		return SymbolRef{}, fmt.Errorf("symbol %d points to missing file %d", sym.ID, sym.FileID)
	}
	return SymbolRef{ID: sym.ID, Name: sym.QualifiedName, Kind: sym.Kind, File: file.Path, StartLine: sym.StartLine, EndLine: sym.EndLine}, nil
}

func (g *Graph) info(sym store.Symbol) (SymbolInfo, error) {
	file, ok, err := g.store.FileByID(sym.FileID)
	if err != nil {
		return SymbolInfo{}, err
	}
	if !ok {
		return SymbolInfo{}, fmt.Errorf("symbol %d points to missing file %d", sym.ID, sym.FileID)
	}
	qualified := sym.QualifiedName
	if qualified == sym.Name {
		qualified = ""
	}
	return SymbolInfo{
		ID: sym.ID, Name: sym.Name, QualifiedName: qualified, Kind: sym.Kind,
		Container: sym.Container, Exported: sym.Exported, JSX: sym.JSX, Signature: sym.Signature,
		Annotations: sym.Annotations, File: file.Path, StartLine: sym.StartLine, EndLine: sym.EndLine,
	}, nil
}

// lines lê e cacheia as linhas de um arquivo do repositório.
func (g *Graph) lines(rel string) ([]string, error) {
	if cached, ok := g.files[rel]; ok {
		return cached, nil
	}
	data, err := os.ReadFile(filepath.Join(g.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	g.files[rel] = lines
	return lines, nil
}

// slice devolve as linhas [start, end] (1-based, inclusivas), limitadas ao
// arquivo.
func slice(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// relative aceita caminho absoluto ou relativo à raiz e devolve o relativo
// com "/", como gravado no índice.
func (g *Graph) relative(file string) string {
	if filepath.IsAbs(file) {
		if rel, err := filepath.Rel(g.root, file); err == nil {
			file = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(file))
}

// isStale diz se o arquivo em disco difere do indexado.
func (g *Graph) isStale(f store.File) bool {
	info, err := os.Stat(filepath.Join(g.root, filepath.FromSlash(f.Path)))
	if err != nil {
		return true
	}
	fp := hasher.FingerprintOf(info)
	return fp.Size != f.Size || fp.ModTime != f.ModTime
}
