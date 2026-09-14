package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/extract/golang"
	"github.com/JonathanSantos/mira/internal/extract/java"
	"github.com/JonathanSantos/mira/internal/extract/python"
	"github.com/JonathanSantos/mira/internal/extract/typescript"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
	"github.com/JonathanSantos/mira/internal/store"
)

// SkeletonInfo é o esqueleto de uma função: o que ela devolve, o que lança,
// o que declara e o que usa de fora, agrupado por origem, sem o corpo. A
// estrutura vem de um parse do arquivo na hora da consulta; os usos vêm do
// índice.
type SkeletonInfo struct {
	Lines          int             `json:"lines"`
	Branches       int             `json:"branches"`
	Loops          int             `json:"loops"`
	NestedCount    int             `json:"nested_functions,omitempty"`
	Async          bool            `json:"async,omitempty"`
	Returns        []extract.Point `json:"returns"`
	Throws         []extract.Point `json:"throws,omitempty"`
	DeclaredThrows []string        `json:"declared_throws,omitempty"`
	Locals         []extract.Point `json:"locals,omitempty"`
	Nested         []SymbolRef     `json:"nested,omitempty"` // funções aninhadas indexadas (Outer.inner), até maxNested
	NestedTotal    int             `json:"nested_total,omitempty"`
	Uses           Uses            `json:"uses"`
	Stale          bool            `json:"stale,omitempty"`
	Note           string          `json:"note,omitempty"`
}

// Use é um nome usado pela função, com as linhas em que aparece e, quando
// resolvido, onde está definido.
type Use struct {
	Name       string `json:"name"`                 // como citar: Owner.addVisit, BindingResult.hasErrors, fetch
	Kind       string `json:"kind"`                 // call, method, new, jsx, property, identifier
	Lines      []int  `json:"lines"`                // ocorrências dentro da função (até maxUseLines)
	Count      int    `json:"count"`                // total de ocorrências
	File       string `json:"file,omitempty"`       // arquivo do alvo, quando resolvido
	Line       int    `json:"line,omitempty"`       // linha do alvo
	Resolution string `json:"resolution,omitempty"` // ambiguous ou unresolved
	Chain      bool   `json:"chain,omitempty"`      // nome é a cadeia como está no código (`a.b().c`), não um qualificado
}

// Uses separa os usos pela origem do alvo, que é o que decide o próximo
// passo do agente: mesmo arquivo (snippet), outro arquivo (resolve), pacote
// externo (não há o que abrir) ou não resolvido.
type Uses struct {
	SameFile   []Use `json:"same_file,omitempty"`
	OtherFiles []Use `json:"other_files,omitempty"`
	External   []Use `json:"external,omitempty"`
	Outer      []Use `json:"outer,omitempty"` // variáveis, campos e propriedades de fora do escopo
	Unresolved []Use `json:"unresolved,omitempty"`
}

const (
	maxUseLines  = 3
	maxNested    = 80 // uma factory de 2.000 linhas tem ~50 closures; listar todas custa ~1,5 KB
	skeletonNote = "skeleton applies to functions, methods and constructors; use symbols for a class outline"
)

// callKinds viram "uses"; readKinds viram "outer". Tipos, imports e
// anotações ficam de fora: já estão na assinatura ou não guiam navegação.
var (
	callKinds = map[string]bool{extract.RefCall: true, extract.RefMethod: true, extract.RefNew: true, extract.RefJSX: true}
	readKinds = map[string]bool{extract.RefIdentifier: true, extract.RefProperty: true}
)

func isFunctionKind(kind string) bool {
	return kind == extract.KindFunction || kind == extract.KindMethod || kind == extract.KindConstructor
}

// skeleton monta o esqueleto de um símbolo do índice. Nunca falha por
// causa do conteúdo: um símbolo que não é função devolve só a nota.
func (g *Graph) skeleton(sym store.Symbol, file store.File) (*SkeletonInfo, error) {
	if !isFunctionKind(sym.Kind) {
		return &SkeletonInfo{Note: skeletonNote}, nil
	}
	info := &SkeletonInfo{Stale: g.isStale(file)}
	if info.Stale {
		info.Note = "file changed since last index; run index to refresh structure and uses"
		return info, nil
	}
	structure, err := g.structure(sym, file)
	if err != nil {
		return nil, err
	}
	info.Lines, info.Branches, info.Loops, info.NestedCount, info.Async = structure.Lines, structure.Branches, structure.Loops, structure.Nested, structure.Async
	info.Returns, info.Throws, info.DeclaredThrows, info.Locals = structure.Returns, structure.Throws, structure.DeclaredThrows, structure.Locals
	if info.Returns == nil {
		info.Returns = []extract.Point{}
	}
	if info.Nested, info.NestedTotal, err = g.nestedSymbols(sym); err != nil {
		return nil, err
	}
	if info.Uses, err = g.uses(sym, file, structure.Declared); err != nil {
		return nil, err
	}
	return info, nil
}

// structure reparseia o arquivo e reencontra o nó do símbolo pelo span.
func (g *Graph) structure(sym store.Symbol, file store.File) (extract.Skeleton, error) {
	src, err := os.ReadFile(filepath.Join(g.root, filepath.FromSlash(file.Path)))
	if err != nil {
		return extract.Skeleton{}, fmt.Errorf("reading %s: %w", file.Path, err)
	}
	tree, err := g.parser().ParseFile(file.Path, src)
	if err != nil {
		return extract.Skeleton{}, err
	}
	defer tree.Release()
	node := nodeForSpan(tree.Root(), sym.StartByte, sym.EndByte)
	var sk extract.Skeleton
	var ok bool
	switch file.Lang {
	case lang.Java:
		sk, ok = java.Skeleton(node)
	case lang.Go:
		sk, ok = golang.Skeleton(node)
	case lang.Python:
		sk, ok = python.Skeleton(node)
	default:
		sk, ok = typescript.Skeleton(node)
	}
	if !ok {
		return extract.Skeleton{}, fmt.Errorf("%s in %s: could not find the function node for the indexed span", sym.QualifiedName, file.Path)
	}
	return sk, nil
}

// nodeForSpan acha o nó gravado para o símbolo. Quando o menor nó que cobre
// o span é um bloco (decorators são irmãos do método na gramática TS), o
// símbolo é o filho que termina onde o span termina.
func nodeForSpan(root parser.Node, start, end int) parser.Node {
	n := root.NamedDescendantForByteRange(start, end)
	if !n.Is("class_body", "program", "statement_block", "block") {
		return n
	}
	for _, c := range n.NamedChildren() {
		if c.StartByte() >= start && c.EndByte() == end {
			return c
		}
	}
	return n
}

// nestedSymbols lista as funções aninhadas indexadas dentro do símbolo
// (até maxNested) e quantas há no total.
func (g *Graph) nestedSymbols(sym store.Symbol) ([]SymbolRef, int, error) {
	symbols, err := g.store.SymbolsOfFile(sym.FileID)
	if err != nil {
		return nil, 0, err
	}
	var out []SymbolRef
	total := 0
	for _, s := range symbols {
		if s.ID == sym.ID || s.Container != sym.Name || s.StartByte < sym.StartByte || s.EndByte > sym.EndByte {
			continue
		}
		total++
		if len(out) == maxNested {
			continue
		}
		ref, err := g.ref(s)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ref)
	}
	return out, total, nil
}

// uses agrupa as refs feitas de dentro da função por (nome, kind) e pela
// origem do alvo. declared filtra o que é uso de parâmetro ou local.
func (g *Graph) uses(sym store.Symbol, file store.File, declared map[string]bool) (Uses, error) {
	refs, err := g.store.RefsInContainer(sym.ID)
	if err != nil {
		return Uses{}, err
	}
	// Um identificador já usado por nome (`EVENTS`) torna redundante a
	// propriedade não resolvida sobre ele (`EVENTS.SUBMIT`).
	named := map[string]bool{}
	for _, r := range refs {
		if r.Kind == extract.RefIdentifier {
			named[r.Name] = true
		}
	}
	groups := map[string]*Use{}
	var order []string
	origin := map[string]string{}
	for _, r := range refs {
		if !callKinds[r.Kind] && !readKinds[r.Kind] || hidden(r, declared) {
			continue
		}
		if r.Kind == extract.RefProperty && r.ResolvedSymbolID == nil && named[firstSegment(r.Receiver)] {
			continue
		}
		use, where, err := g.useOf(r, file)
		if err != nil {
			return Uses{}, err
		}
		key := where + "\x00" + use.Kind + "\x00" + use.Name
		existing, ok := groups[key]
		if !ok {
			groups[key] = &use
			origin[key] = where
			order = append(order, key)
			continue
		}
		existing.Count++
		if len(existing.Lines) < maxUseLines && existing.Lines[len(existing.Lines)-1] != r.Line {
			existing.Lines = append(existing.Lines, r.Line)
		}
	}
	if err := g.fieldReceivers(sym, file, refs, declared, groups, &order, origin); err != nil {
		return Uses{}, err
	}
	var out Uses
	for _, key := range order {
		use := *groups[key]
		switch origin[key] {
		case "same":
			out.SameFile = append(out.SameFile, use)
		case "other":
			out.OtherFiles = append(out.OtherFiles, use)
		case "external":
			out.External = append(out.External, use)
		case "outer":
			out.Outer = append(out.Outer, use)
		default:
			out.Unresolved = append(out.Unresolved, use)
		}
	}
	sortUses(out.OtherFiles)
	return out, nil
}

// hidden diz se a ref é ruído para o esqueleto: acesso a parâmetro ou
// local (`e.preventDefault()`, `names.forEach`), ou membro de uma expressão
// complexa sem alvo (`get(x).size`), que não dá nada para citar.
func hidden(r store.Ref, declared map[string]bool) bool {
	receiver := firstSegment(r.Receiver)
	resolved := r.ResolvedSymbolID != nil || r.Resolution == store.External
	if len(r.ReceiverPath) > 0 {
		// `order.first().price`: o que se lê é o resultado da cadeia, não o
		// parâmetro; só some quando não resolveu e nem o receiver se sabe.
		return !resolved && receiver == ""
	}
	if receiver != "" && receiver != "this" && receiver != "new" && declared[receiver] {
		// Ler campos de um parâmetro (`props.values`) é entrada, não estado de
		// fora. Chamar um método nele interessa quando o tipo é conhecido
		// (`result.rejectValue()`); `e.preventDefault()` sem tipo não.
		if readKinds[r.Kind] {
			return true
		}
		return !resolved && (r.ReceiverType == "" || isMarkerType(r.ReceiverType))
	}
	if receiver == "" && !resolved && (r.Kind == extract.RefMethod || r.Kind == extract.RefProperty) {
		return true
	}
	return r.Kind == extract.RefIdentifier && declared[r.Name]
}

// fieldReceivers põe em "outer" os campos da classe usados como receptor
// de chamadas (`owners.findById()`), que nenhuma ref registra como leitura
// mas são o estado de fora que o método toca.
func (g *Graph) fieldReceivers(sym store.Symbol, file store.File, refs []store.Ref, declared map[string]bool,
	groups map[string]*Use, order *[]string, origin map[string]string) error {
	if sym.Container == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, r := range refs {
		receiver := firstSegment(r.Receiver)
		if !callKinds[r.Kind] || receiver == "" || receiver == "this" || receiver == "new" || declared[receiver] || isUpper(receiver) {
			continue
		}
		fields, err := g.store.SymbolsInContainer(file.ID, sym.Container, receiver)
		if err != nil {
			return err
		}
		field := store.Symbol{}
		for _, f := range fields {
			if f.Kind == extract.KindField || f.Kind == extract.KindProperty {
				field = f
				break
			}
		}
		if field.ID == 0 {
			continue
		}
		key := "outer\x00field\x00" + field.QualifiedName
		if existing, ok := groups[key]; ok {
			existing.Count++
			if len(existing.Lines) < maxUseLines && existing.Lines[len(existing.Lines)-1] != r.Line {
				existing.Lines = append(existing.Lines, r.Line)
			}
			continue
		}
		if !seen[key] {
			seen[key] = true
			groups[key] = &Use{Name: field.QualifiedName, Kind: "field", Lines: []int{r.Line}, Count: 1, Line: field.StartLine}
			origin[key] = "outer"
			*order = append(*order, key)
		}
	}
	return nil
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func isUpper(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

func firstSegment(receiver string) string {
	if i := strings.Index(receiver, "."); i >= 0 {
		return receiver[:i]
	}
	return receiver
}

// useOf converte uma ref em Use e diz a que grupo ela pertence.
func (g *Graph) useOf(r store.Ref, file store.File) (Use, string, error) {
	use := Use{Name: r.Name, Kind: r.Kind, Lines: []int{r.Line}, Count: 1}
	if r.ResolvedSymbolID != nil {
		target, ok, err := g.store.SymbolByID(*r.ResolvedSymbolID)
		if err != nil {
			return Use{}, "", err
		}
		if ok {
			targetFile, _, err := g.store.FileByID(target.FileID)
			if err != nil {
				return Use{}, "", err
			}
			use.Name, use.Line = target.QualifiedName, target.StartLine
			if r.Kind == extract.RefNew {
				use.Name = "new " + lastSegment(target.QualifiedName)
			}
			if targetFile.ID != file.ID {
				use.File = targetFile.Path
			}
			if readKinds[r.Kind] {
				return use, "outer", nil
			}
			if targetFile.ID == file.ID {
				return use, "same", nil
			}
			return use, "other", nil
		}
	}
	use.Name = displayName(r)
	use.Chain = len(r.ReceiverPath) > 0
	switch r.Resolution {
	case store.External:
		if readKinds[r.Kind] {
			return use, "outer", nil
		}
		return use, "external", nil
	case store.Ambiguous:
		use.Resolution = store.Ambiguous
	default:
		use.Resolution = store.Unresolved
	}
	if readKinds[r.Kind] {
		// Leitura de algo que não é local nem resolvido: estado capturado da
		// closure ou campo (`_formState.errors`); vale mostrar como "de fora".
		return use, "outer", nil
	}
	return use, "unresolved", nil
}

// displayName é o nome de um uso não resolvido como o agente vai citá-lo:
// `Tipo.metodo` quando o tipo do receptor é conhecido, `obj.metodo` quando
// só o receptor é, `new T` e `<Comp>` para criação e JSX.
func displayName(r store.Ref) string {
	switch {
	case r.Kind == extract.RefNew:
		return "new " + r.Name
	case r.Kind == extract.RefJSX:
		return "<" + r.Name + ">"
	case len(r.ReceiverPath) > 0:
		// Cadeia: como está no código (`order.items.reduce`), nunca o tipo.
		head := r.Receiver
		if head == "" {
			head = r.ReceiverPath[0] + "()"
			return head + "." + strings.Join(append(r.ReceiverPath[1:], r.Name), ".")
		}
		return head + "." + strings.Join(append(append([]string{}, r.ReceiverPath...), r.Name), ".")
	case r.ReceiverType != "" && !isMarkerType(r.ReceiverType):
		return r.ReceiverType + "." + r.Name
	case r.Receiver != "":
		return r.Receiver + "." + r.Name // alias de pacote, `x := f()`: como está no código
	}
	return r.Name
}

// isMarkerType reconhece ReceiverTypes que não são nomes de tipo: alias de
// pacote/módulo e pistas `call:`/`var:`.
func isMarkerType(t string) bool {
	return t == golang.PackageReceiver || t == python.ModuleReceiver || strings.HasPrefix(t, "call:") || strings.HasPrefix(t, "var:")
}

func sortUses(uses []Use) {
	sort.SliceStable(uses, func(i, j int) bool {
		if uses[i].File != uses[j].File {
			return uses[i].File < uses[j].File
		}
		return uses[i].Line < uses[j].Line
	})
}

// parser é criado sob demanda: só consultas com esqueleto precisam dele.
func (g *Graph) parser() *parser.Parser {
	if g.ts == nil {
		g.ts = parser.New()
	}
	return g.ts
}

// Short é o nome curto de um uso para o texto: qualificado reduzido a dois
// segmentos.
func (u Use) Short() string {
	if strings.HasPrefix(u.Name, "new ") || u.Chain {
		return u.Name
	}
	parts := strings.Split(u.Name, ".")
	if len(parts) <= 2 {
		return u.Name
	}
	return strings.Join(parts[len(parts)-2:], ".")
}
