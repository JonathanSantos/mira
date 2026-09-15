// Package resolve liga refs e imports a símbolos reais e gera as edges do
// grafo. Roda depois da escrita dos arquivos, só sobre os arquivos afetados.
package resolve

import (
	"fmt"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/store"
)

// Stats conta o resultado de uma rodada de resolução.
type Stats struct {
	Files      int `json:"files"`
	Resolved   int `json:"resolved"`
	Ambiguous  int `json:"ambiguous"`
	External   int `json:"external"`
	Unresolved int `json:"unresolved"`
	Edges      int `json:"edges"`
}

// Resolver resolve um conjunto de arquivos. Os caches vivem só durante uma
// chamada de ResolveFiles.
type Resolver struct {
	derived int // tipos derivados (`T["a"]`) em resolução, para cortar ciclos
	root    string
	store   *store.Store
	files   map[string]*store.File // cache path -> file (nil = não existe)
	// contexts guarda o contexto de arquivos consultados de fora (tipos pai
	// de uma cadeia de herança), com os imports já resolvidos em memória.
	contexts map[int64]*fileContext
	tsconfig *tsConfig // carregado na primeira necessidade
	lookups  lookups   // respostas do banco nesta rodada
	goMod    *string   // caminho do módulo Go, lido uma vez
}

func New(root string, s *store.Store) *Resolver {
	return &Resolver{root: root, store: s, files: map[string]*store.File{}, contexts: map[int64]*fileContext{}, lookups: newLookups()}
}

// maxInheritanceDepth limita a subida por extends/implements.
const maxInheritanceDepth = 8

// maxChainDepth limita quantos membros de uma cadeia `a.b().c().d()` são
// seguidos pelo tipo de retorno.
const maxChainDepth = 4

// outcome é o resultado de resolver um nome.
type outcome struct {
	resolution string
	symbol     *store.Symbol
	file       *int64
}

func resolved(sym store.Symbol) outcome {
	s := sym
	return outcome{resolution: store.Resolved, symbol: &s, file: &s.FileID}
}

var (
	unresolved = outcome{resolution: store.Unresolved}
	external   = outcome{resolution: store.External}
	ambiguous  = outcome{resolution: store.Ambiguous}
)

// pick reduz uma lista de candidatos ao outcome: 1 = resolvido, vários =
// ambíguo, nenhum = o fallback dado. Nunca chuta entre candidatos.
func pick(candidates []store.Symbol, none outcome) outcome {
	switch len(candidates) {
	case 0:
		return none
	case 1:
		return resolved(candidates[0])
	default:
		return ambiguous
	}
}

// ResolveFiles re-resolve cada arquivo do conjunto, em uma transação por
// arquivo.
func (r *Resolver) ResolveFiles(fileIDs []int64) (Stats, error) {
	var stats Stats
	r.contexts = map[int64]*fileContext{}
	r.lookups = newLookups()
	for _, id := range fileIDs {
		file, ok, err := r.fileByID(id)
		if err != nil {
			return stats, err
		}
		if !ok {
			continue
		}
		if err := r.resolveFile(file, &stats); err != nil {
			return stats, fmt.Errorf("resolving %s: %w", file.Path, err)
		}
		stats.Files++
	}
	return stats, nil
}

// fileContext é tudo que a resolução de um arquivo precisa ter em mãos.
type fileContext struct {
	file    store.File
	symbols []store.Symbol
	imports []store.Import
	refs    []store.Ref
	// importOutcome guarda, por id de import, o resultado da resolução do
	// import, para que as refs reaproveitem.
	importOutcome map[int64]outcome
}

// loadContext lê símbolos, imports e refs de um arquivo e resolve os
// imports em memória, sem gravar nada.
func (r *Resolver) loadContext(file store.File) (*fileContext, error) {
	symbols, err := r.symbolsOfFile(file.ID)
	if err != nil {
		return nil, err
	}
	imports, err := r.importsOfFile(file.ID)
	if err != nil {
		return nil, err
	}
	refs, err := r.store.RefsOfFile(file.ID)
	if err != nil {
		return nil, err
	}
	ctx := &fileContext{file: file, symbols: symbols, imports: imports, refs: refs, importOutcome: map[int64]outcome{}}
	for i := range ctx.imports {
		ctx.importOutcome[ctx.imports[i].ID] = r.resolveImport(ctx, ctx.imports[i])
	}
	r.contexts[file.ID] = ctx
	return ctx, nil
}

// contextFor devolve o contexto de outro arquivo (o de um tipo pai), que
// pode ainda não ter sido resolvido nesta rodada.
func (r *Resolver) contextFor(fileID int64) (*fileContext, error) {
	if ctx, ok := r.contexts[fileID]; ok {
		return ctx, nil
	}
	file, ok, err := r.fileByID(fileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("file %d not found", fileID)
	}
	return r.loadContext(file)
}

func (r *Resolver) resolveFile(file store.File, stats *Stats) error {
	ctx, err := r.loadContext(file)
	if err != nil {
		return err
	}
	tx, err := r.store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.ClearResolution(file.ID); err != nil {
		return err
	}
	for _, im := range ctx.imports {
		out := ctx.importOutcome[im.ID]
		if err := tx.SetImportResolution(im.ID, out.file, symbolID(out), out.resolution); err != nil {
			return err
		}
	}
	for _, ref := range ctx.refs {
		out := r.resolveRef(ctx, ref)
		if err := tx.SetRefResolution(ref.ID, symbolID(out), out.resolution); err != nil {
			return err
		}
		count(stats, out.resolution)
		if edge, ok := edgeFor(ref, out); ok {
			if err := tx.InsertEdge(edge); err != nil {
				return err
			}
			stats.Edges++
		}
	}
	return tx.Commit()
}

func (r *Resolver) resolveImport(ctx *fileContext, im store.Import) outcome {
	switch ctx.file.Lang {
	case lang.Go:
		return r.resolveGoImport(im)
	case lang.Python:
		return r.resolvePythonImport(ctx, im)
	case lang.Java:
		return r.resolveJavaImport(im)
	}
	return r.resolveTSImport(ctx, im)
}

func (r *Resolver) resolveRef(ctx *fileContext, ref store.Ref) outcome {
	if ref.Kind == extract.RefImport {
		return r.importRef(ctx, ref)
	}
	if ref.Kind == extract.RefMethod || ref.Kind == extract.RefProperty {
		return r.resolveMember(ctx, ref)
	}
	if isPackageReceiver(ref.ReceiverType) {
		return r.packageMember(ctx, ref.Receiver, ref.ReceiverPath, ref.Name) // `pkg.T{}`, `mod.Base`
	}
	switch ctx.file.Lang {
	case lang.Go:
		return r.resolveGoName(ctx, ref.Name)
	case lang.Python:
		return r.resolvePythonName(ctx, ref.Name, ref.Line)
	case lang.Java:
		if ref.Receiver != "" {
			return r.resolveJavaQualified(ref.Receiver, ref.Name)
		}
		return r.resolveJavaName(ctx, ref.Name, javaTypeKinds)
	}
	return r.resolveTSName(ctx, ref.Name)
}

// importRef copia o resultado do import correspondente ao nome importado
// (ou re-exportado, em `export { a } from './m'`).
func (r *Resolver) importRef(ctx *fileContext, ref store.Ref) outcome {
	for _, im := range ctx.imports {
		if im.Line != ref.Line {
			continue
		}
		if im.ImportedName == ref.Name || (im.ImportedName == "default" && im.LocalName == ref.Name) {
			return ctx.importOutcome[im.ID]
		}
	}
	return unresolved
}

// resolveMember trata `recv.m()` e `recv.x`: acha o tipo do receiver e
// procura o membro dentro dele. Se o tipo estende algo fora do índice e o
// membro não está nele, o membro é herdado da lib: external.
func (r *Resolver) resolveMember(ctx *fileContext, ref store.Ref) outcome {
	if len(ref.ReceiverPath) > 0 {
		return r.chainedMember(ctx, ref)
	}
	if ref.Receiver == "" && ref.ReceiverType == "" {
		return r.unqualifiedMethod(ctx, ref)
	}
	if isPackageReceiver(ref.ReceiverType) {
		return r.packageMember(ctx, ref.Receiver, ref.ReceiverPath, ref.Name) // `svc.New()`, `np.sum()`
	}
	if ref.ReceiverType == "" {
		return unresolved
	}
	cur, out := r.receiverPoint(ctx, ref.ReceiverType, ref.Name)
	if cur.typeSym == nil {
		if cur.elem != "" {
			return external // `items.push(x)`: membro de um container do runtime
		}
		return out
	}
	return r.memberOrInherited(ctx, *cur.typeSym, ref)
}

// receiverPoint transforma o ReceiverType de uma ref no ponto de partida
// de uma cadeia: um tipo do repositório, um container do runtime, ou o
// resultado de `call:f` (tipo de retorno de f, anotado ou inferido).
func (r *Resolver) receiverPoint(ctx *fileContext, receiverType, member string) (chainPoint, outcome) {
	if base, derived, ok := typePath(receiverType); ok {
		return r.derivedPoint(ctx, base, derived)
	}
	if target, ok := hintTarget(receiverType); ok {
		// `call:f` / `var:X`: o tipo vem do que f devolve ou do que X guarda.
		sym := r.hintSymbol(ctx, target)
		if sym.symbol == nil {
			return chainPoint{}, sym
		}
		return r.typeAfter(*sym.symbol)
	}
	if elem, ok := containerElement(receiverType); ok {
		return chainPoint{elem: elem, elemFile: ctx.file.ID}, external
	}
	typeOut := r.resolveTypeName(ctx, receiverType)
	if typeOut.symbol != nil {
		return chainPoint{typeSym: typeOut.symbol}, typeOut
	}
	// Namespace import (`import * as ns`): o receiver é um módulo, não um tipo.
	if typeOut.file != nil && ctx.file.Lang.IsTS() && member != "" {
		return chainPoint{}, r.lookupExport(*typeOut.file, member, map[exportKey]bool{})
	}
	return chainPoint{}, typeOut
}

// member procura o membro declarado no próprio tipo, separando sobrecargas
// pela chamada.
func (r *Resolver) member(ctx *fileContext, typeSym store.Symbol, ref store.Ref) outcome {
	members, err := r.membersOf(typeSym, ref.Name)
	if err != nil {
		return unresolved
	}
	candidates := r.narrowOverloads(ctx, memberCandidates(members, ref.Kind, typeSym.Kind), ref)
	if ctx.file.Lang == lang.Go {
		candidates = r.preferDefaultBuild(candidates)
	}
	return pick(candidates, unresolved)
}

// membersOf lista os membros de um tipo com o nome: no arquivo do tipo, ou
// em todos os arquivos do pacote quando é Go (métodos se espalham).
func (r *Resolver) membersOf(typeSym store.Symbol, name string) ([]store.Symbol, error) {
	typeCtx, err := r.contextFor(typeSym.FileID)
	if err != nil {
		return nil, err
	}
	if typeCtx.file.Lang == lang.Go {
		return r.membersInPackage(typeCtx.file.Package, typeSym.Name, name)
	}
	if typeSym.Kind == extract.KindProperty {
		// Objeto literal aninhado (`customData?: { data: X }`): os membros
		// ficam sob o nome qualificado da propriedade.
		return r.symbolsInContainer(typeSym.FileID, typeSym.QualifiedName, name)
	}
	return r.symbolsInContainer(typeSym.FileID, typeSym.Name, name)
}

// memberOrInherited procura no tipo e, se não há, na cadeia de herança.
func (r *Resolver) memberOrInherited(ctx *fileContext, typeSym store.Symbol, ref store.Ref) outcome {
	out := r.member(ctx, typeSym, ref)
	if out.resolution == store.Unresolved {
		return r.inheritedMember(ctx, typeSym, ref, map[int64]bool{}, 0)
	}
	return out
}

// chainedMember segue `a.b().c()`: resolve o tipo do receiver, o membro de
// cada passo e o tipo que ele declara (retorno ou tipo do campo), até
// chegar ao tipo onde o nome final é procurado. Um tipo intermediário fora
// do índice (`Optional`, `Page`) torna o resultado external; um tipo não
// declarado (TS sem anotação) deixa unresolved.
func (r *Resolver) chainedMember(ctx *fileContext, ref store.Ref) outcome {
	path := ref.ReceiverPath
	if len(path) > maxChainDepth {
		return unresolved
	}
	var cur chainPoint
	if ref.Receiver == "" {
		// Cadeia que começa numa função solta: `useForm().register`.
		fn := r.freeCall(ctx, path[0])
		if fn.symbol == nil {
			return fn
		}
		next, out := r.typeAfter(*fn.symbol)
		if next.empty() {
			return out
		}
		cur, path = next, path[1:]
	} else {
		if ref.ReceiverType == "" {
			return unresolved
		}
		base, out := r.receiverPoint(ctx, ref.ReceiverType, "")
		if base.empty() {
			return out
		}
		cur = base
	}
	for _, name := range path {
		if cur.typeSym == nil {
			// Container do runtime (List<X>, X[], Optional<X>): só os passos
			// que devolvem o elemento levam a algum lugar do repositório.
			next, out := r.containerStep(cur, name)
			if next.empty() {
				return out
			}
			cur = next
			continue
		}
		step := store.Ref{Name: name, Kind: extract.RefProperty, Arity: -1}
		found := r.memberOrInherited(ctx, *cur.typeSym, step)
		if found.resolution == store.Ambiguous {
			// Sobrecargas sem argumentos conhecidos: se todas declaram o mesmo
			// tipo (`getPet(...)` sempre devolve Pet), a cadeia segue.
			next, out := r.commonType(*cur.typeSym, step)
			if next == nil {
				return out
			}
			cur = chainPoint{typeSym: next}
			continue
		}
		if found.symbol == nil {
			return found
		}
		next, out := r.typeAfter(*found.symbol)
		if next.empty() {
			return out
		}
		cur = next
	}
	if cur.typeSym == nil {
		return external // membro de um container do runtime (`list.size()`)
	}
	return r.memberOrInherited(ctx, *cur.typeSym, ref)
}

// chainPoint é onde uma cadeia está: num tipo do repositório, ou dentro de
// um container do runtime cujo elemento (`X` em `List<X>`) ainda pode
// levar de volta ao repositório.
type chainPoint struct {
	typeSym  *store.Symbol
	elem     string // nome do tipo do elemento, quando é container
	elemFile int64  // arquivo em que o nome do elemento deve ser resolvido
}

func (c chainPoint) empty() bool { return c.typeSym == nil && c.elem == "" }

// typeAfter devolve o ponto da cadeia depois de passar por um membro: o
// tipo que ele declara ou infere, ou o container com o elemento.
func (r *Resolver) typeAfter(sym store.Symbol) (chainPoint, outcome) {
	ctx, err := r.contextFor(sym.FileID)
	if err != nil {
		return chainPoint{}, unresolved
	}
	callable := sym.Kind == extract.KindMethod || sym.Kind == extract.KindFunction
	declared := extract.DeclaredType(sym.Signature, sym.Name, callable, ctx.file.Lang)
	if declared == "" && sym.ReturnHint != "" {
		return r.hintedType(ctx, sym.ReturnHint, 0) // sem anotação: o que foi inferido
	}
	if (declared == "" || strings.HasPrefix(declared, "{")) && sym.Kind == extract.KindProperty && ctx.file.Lang.IsTS() {
		// `customData?: { data: X }` (ou sem anotação): o tipo é o objeto
		// literal, e a própria propriedade guarda os membros dele.
		return chainPoint{typeSym: &sym}, unresolved
	}
	if base, member, ok := typePath(memberType(declared)); ok {
		return r.derivedPoint(ctx, base, member) // `scene: App["scene"]`
	}
	if elem, ok := containerElement(declared); ok {
		return chainPoint{elem: elem, elemFile: sym.FileID}, external
	}
	name := baseType(memberType(declared))
	if name == "" {
		return chainPoint{}, unresolved
	}
	return r.typePoint(ctx, name)
}

// typePoint resolve um nome de tipo num contexto e devolve o ponto.
func (r *Resolver) typePoint(ctx *fileContext, name string) (chainPoint, outcome) {
	out := r.resolveTypeName(ctx, name)
	if out.symbol != nil {
		return chainPoint{typeSym: out.symbol}, out
	}
	if out.resolution == store.External {
		return chainPoint{}, external
	}
	return chainPoint{}, unresolved
}

// hintTarget separa `call:f` e `var:X` do nome alvo.
func hintTarget(hint string) (string, bool) {
	if t, ok := strings.CutPrefix(hint, "call:"); ok {
		return t, true
	}
	if t, ok := strings.CutPrefix(hint, "var:"); ok {
		return t, true
	}
	return "", false
}

// typePath separa o último passo de um tipo derivado: `T["a"]` é o membro a
// de T e `T[number]`, o elemento. base é o resto, que pode ter outros passos
// (`call:f["a"]["b"]`). `T[]` não é derivado: é container.
func typePath(t string) (base, member string, ok bool) {
	t = strings.TrimSpace(t)
	open := strings.LastIndex(t, "[")
	if open <= 0 || !strings.HasSuffix(t, "]") {
		return "", "", false
	}
	inner := strings.TrimSpace(t[open+1 : len(t)-1])
	if inner == "number" {
		return t[:open], "", true
	}
	if len(inner) >= 2 && (inner[0] == '"' || inner[0] == '\'') && inner[len(inner)-1] == inner[0] {
		return t[:open], inner[1 : len(inner)-1], true
	}
	return "", "", false
}

// derivedPoint é o ponto de um tipo derivado: o tipo do membro de base ou,
// com member "", o elemento de base. Um tipo que se refere a si mesmo
// (`type T = { a: T["a"] }`) para em maxChainDepth.
func (r *Resolver) derivedPoint(ctx *fileContext, base, member string) (chainPoint, outcome) {
	if r.derived > maxChainDepth {
		return chainPoint{}, unresolved
	}
	r.derived++
	defer func() { r.derived-- }()
	cur, out := r.receiverPoint(ctx, base, "")
	if member == "" {
		if cur.elem == "" {
			return chainPoint{}, unresolved
		}
		return r.elementPoint(cur)
	}
	if cur.typeSym == nil {
		return chainPoint{}, out
	}
	found := r.memberOrInherited(ctx, *cur.typeSym, store.Ref{Name: member, Kind: extract.RefProperty, Arity: -1})
	if found.symbol == nil {
		return chainPoint{}, found
	}
	return r.typeAfter(*found.symbol)
}

// hintSymbol resolve o alvo de uma pista: `pkg.Name` pelo import, um nome
// solto como função ou nome de topo do arquivo/pacote.
func (r *Resolver) hintSymbol(ctx *fileContext, target string) outcome {
	if i := strings.Index(target, "."); i > 0 {
		return r.packageMember(ctx, target[:i], nil, target[i+1:])
	}
	if out := r.freeCall(ctx, target); out.symbol != nil {
		return out
	}
	return r.resolveTypeName(ctx, target)
}

// hintedType segue o ReturnHint de um símbolo sem tipo declarado: um nome
// de tipo, ou `call:f` / `var:X`, um nível por vez até maxChainDepth.
func (r *Resolver) hintedType(ctx *fileContext, hint string, depth int) (chainPoint, outcome) {
	if hint == "" || depth > maxChainDepth {
		return chainPoint{}, unresolved
	}
	if base, member, ok := typePath(memberType(hint)); ok {
		return r.derivedPoint(ctx, base, member)
	}
	target, isHint := hintTarget(hint)
	if !isHint {
		if elem, ok := containerElement(hint); ok {
			return chainPoint{elem: elem, elemFile: ctx.file.ID}, external
		}
		return r.typePoint(ctx, baseType(hint))
	}
	sym := r.hintSymbol(ctx, target)
	if sym.symbol == nil {
		return chainPoint{}, sym
	}
	symCtx, err := r.contextFor(sym.symbol.FileID)
	if err != nil {
		return chainPoint{}, unresolved
	}
	callable := sym.symbol.Kind == extract.KindMethod || sym.symbol.Kind == extract.KindFunction
	if declared := extract.DeclaredType(sym.symbol.Signature, sym.symbol.Name, callable, symCtx.file.Lang); declared != "" {
		if base, member, ok := typePath(memberType(declared)); ok {
			return r.derivedPoint(symCtx, base, member)
		}
		if elem, ok := containerElement(declared); ok {
			return chainPoint{elem: elem, elemFile: sym.symbol.FileID}, external
		}
		return r.typePoint(symCtx, baseType(declared))
	}
	return r.hintedType(symCtx, sym.symbol.ReturnHint, depth+1)
}

// containerBases são tipos do runtime cujo elemento interessa: List<X>
// devolve X em get(), Optional<X> em orElseThrow(), X[] em find().
var containerBases = map[string]bool{
	"List": true, "ArrayList": true, "Set": true, "Collection": true, "Iterable": true, "Iterator": true,
	"Optional": true, "Stream": true, "Page": true, "Array": true, "ReadonlyArray": true, "Promise": true,
}

// unwrapSteps devolvem o elemento; keepSteps devolvem outro container do
// mesmo elemento.
var (
	unwrapSteps = map[string]bool{
		"get": true, "orElseThrow": true, "orElse": true, "orElseGet": true, "next": true,
		"getFirst": true, "getLast": true, "first": true, "last": true, "at": true, "pop": true,
		"shift": true, "find": true, "then": true,
	}
	keepSteps = map[string]bool{
		"stream": true, "iterator": true, "filter": true, "sorted": true, "distinct": true, "limit": true,
		"skip": true, "findFirst": true, "findAny": true, "getContent": true, "toList": true, "reversed": true,
		"subList": true, "slice": true, "concat": true, "reverse": true, "sort": true, "values": true,
	}
)

// containerElement lê o elemento de `List<X>`, `X[]`, `Promise<X>`; ok é
// false para tipos que não são containers conhecidos. `Promise<X>` entrega
// X direto: quem encadeia depois de uma promise já deu await.
func containerElement(declared string) (string, bool) {
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(declared), "readonly "))
	if strings.HasSuffix(t, "[]") {
		return strings.TrimSuffix(t, "[]"), true
	}
	open := strings.Index(t, "<")
	if open < 0 || !strings.HasSuffix(t, ">") || !containerBases[t[:open]] {
		return "", false
	}
	args := splitTypeArgs(t[open+1 : len(t)-1])
	if len(args) == 0 {
		return "", false
	}
	return strings.TrimSpace(args[len(args)-1]), true
}

// sameMemberUtilities são os utilitários do TypeScript cujos membros são os
// do primeiro argumento: `Readonly<Svc>` e `Pick<Svc, "a">` levam a Svc.
var sameMemberUtilities = map[string]bool{"Readonly": true, "Partial": true, "Required": true, "Pick": true, "Omit": true, "NonNullable": true}

// memberType reduz um tipo declarado ao tipo cujos membros valem: tira
// `| null` e `| undefined` e desembrulha `Readonly<Svc>` e `Pick<Svc, "a">`,
// também aninhados. Uma union de dois tipos de verdade fica como está.
func memberType(t string) string {
	t = strings.TrimSpace(t)
	if parts := splitUnion(t); len(parts) > 1 {
		var kept []string
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "null" && p != "undefined" {
				kept = append(kept, p)
			}
		}
		if len(kept) != 1 {
			return t
		}
		return memberType(kept[0])
	}
	open := strings.Index(t, "<")
	if open < 0 || !strings.HasSuffix(t, ">") || !sameMemberUtilities[t[:open]] {
		return t
	}
	args := splitTypeArgs(t[open+1 : len(t)-1])
	if len(args) == 0 {
		return t
	}
	return memberType(args[0])
}

// splitUnion separa `A | B<C | D>` nos membros de topo [A, B<C | D>].
func splitUnion(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '<', '(', '{', '[':
			depth++
		case '>', ')', '}', ']':
			depth--
		case '|':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// splitTypeArgs separa `K, List<V>` em [K, List<V>].
func splitTypeArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// containerStep avança dentro de um container: um passo que desembrulha
// resolve o elemento; um que mantém o container segue; qualquer outro é
// do runtime.
func (r *Resolver) containerStep(cur chainPoint, name string) (chainPoint, outcome) {
	switch {
	case unwrapSteps[name]:
		return r.elementPoint(cur)
	case keepSteps[name]:
		return cur, external
	}
	return chainPoint{}, external
}

// elementPoint é o ponto do elemento de um container: outro container
// (`List<Item[]>`) ou o tipo do elemento.
func (r *Resolver) elementPoint(cur chainPoint) (chainPoint, outcome) {
	ctx, err := r.contextFor(cur.elemFile)
	if err != nil {
		return chainPoint{}, unresolved
	}
	if elem, ok := containerElement(cur.elem); ok {
		return chainPoint{elem: elem, elemFile: cur.elemFile}, external
	}
	return r.typePoint(ctx, baseType(cur.elem))
}

// commonType devolve o tipo declarado quando todas as sobrecargas de um
// membro declaram o mesmo; senão ambíguo.
func (r *Resolver) commonType(typeSym store.Symbol, step store.Ref) (*store.Symbol, outcome) {
	members, err := r.symbolsInContainer(typeSym.FileID, typeSym.Name, step.Name)
	if err != nil {
		return nil, unresolved
	}
	var common *store.Symbol
	for _, m := range memberCandidates(members, step.Kind, typeSym.Kind) {
		next, _ := r.typeAfter(m)
		if next.typeSym == nil || (common != nil && next.typeSym.ID != common.ID) {
			return nil, ambiguous
		}
		common = next.typeSym
	}
	if common == nil {
		return nil, ambiguous
	}
	return common, resolved(*common)
}

// freeCall resolve o nome de uma função chamada sem receiver.
func (r *Resolver) freeCall(ctx *fileContext, name string) outcome {
	switch ctx.file.Lang {
	case lang.Java:
		return r.staticImport(ctx, name)
	case lang.Go:
		return r.resolveGoName(ctx, name)
	case lang.Python:
		return r.resolvePythonName(ctx, name, 0)
	}
	return r.resolveTSName(ctx, name)
}

// resolveTypeName resolve um nome de tipo no contexto do arquivo; `pkg.T`
// (Go e Python) passa pelo import.
func (r *Resolver) resolveTypeName(ctx *fileContext, name string) outcome {
	if i := strings.Index(name, "."); i > 0 && (ctx.file.Lang == lang.Go || ctx.file.Lang == lang.Python) {
		return r.packageMember(ctx, name[:i], nil, name[i+1:])
	}
	switch ctx.file.Lang {
	case lang.Java:
		return r.resolveJavaName(ctx, name, javaTypeKinds)
	case lang.Go:
		return r.resolveGoName(ctx, name)
	case lang.Python:
		return r.resolvePythonName(ctx, name, 0)
	}
	return r.resolveTSName(ctx, name)
}

// aliasMember procura o membro em todas as bases de um alias (`A & B`,
// `A | B`, `Readonly<A>`), não só na primeira que responder: o `type` de
// cada lado de uma union são declarações diferentes, e escolher uma seria
// chute. A mesma declaração alcançada por vários lados (base comum) resolve.
func (r *Resolver) aliasMember(ctx *fileContext, parents []store.Symbol, hasExternal bool, ref store.Ref, visited map[int64]bool, depth int) outcome {
	var found *store.Symbol
	for _, parent := range parents {
		out := r.member(ctx, parent, ref)
		if out.resolution == store.Unresolved {
			out = r.inheritedMember(ctx, parent, ref, visited, depth+1)
		}
		if out.resolution == store.External {
			hasExternal = true
		}
		if out.resolution == store.Ambiguous || out.symbol != nil && found != nil && found.ID != out.symbol.ID {
			return ambiguous
		}
		if out.symbol != nil {
			found = out.symbol
		}
	}
	switch {
	case found != nil:
		return resolved(*found)
	case hasExternal:
		return external
	}
	return unresolved
}

// inheritedMember sobe a cadeia extends/implements do tipo procurando o
// membro. Um pai fora do índice (JpaRepository, React.Component) torna o
// membro external quando nada foi achado no repositório.
func (r *Resolver) inheritedMember(ctx *fileContext, typeSym store.Symbol, ref store.Ref, visited map[int64]bool, depth int) outcome {
	if depth > maxInheritanceDepth || visited[typeSym.ID] {
		return unresolved
	}
	visited[typeSym.ID] = true
	parents, hasExternal := r.parentTypes(typeSym)
	if typeSym.Kind == extract.KindType {
		return r.aliasMember(ctx, parents, hasExternal, ref, visited, depth)
	}
	for _, parent := range parents {
		if out := r.member(ctx, parent, ref); out.resolution != store.Unresolved {
			return out
		}
		if out := r.inheritedMember(ctx, parent, ref, visited, depth+1); out.resolution != store.Unresolved {
			return out
		}
	}
	if hasExternal {
		return external
	}
	return unresolved
}

// parentTypes resolve os nomes das cláusulas extends/implements do tipo no
// contexto do arquivo dele, agora, porque esse arquivo pode ainda não ter
// sido resolvido nesta rodada. Devolve também se algum pai é externo.
func (r *Resolver) parentTypes(typeSym store.Symbol) ([]store.Symbol, bool) {
	ctx, err := r.contextFor(typeSym.FileID)
	if err != nil {
		return nil, false
	}
	var parents []store.Symbol
	hasExternal := false
	for _, ref := range ctx.refs {
		if ref.ContainerSymbol == nil || *ref.ContainerSymbol != typeSym.ID ||
			(ref.Kind != extract.RefExtends && ref.Kind != extract.RefImplements) {
			continue
		}
		out := r.resolveRef(ctx, ref)
		switch {
		case out.symbol != nil:
			parents = append(parents, *out.symbol)
		case out.resolution == store.External:
			hasExternal = true
		}
	}
	return parents, hasExternal
}

// memberCandidates filtra o que pode ser alvo da ref: chamadas apontam para
// métodos (e componentes de record, que geram accessors); acessos a
// propriedade apontam para campos/propriedades e também para métodos, já
// que `this.handler` pode ser um método passado como callback.
func memberCandidates(members []store.Symbol, refKind, containerKind string) []store.Symbol {
	var out []store.Symbol
	for _, m := range members {
		callable := m.Kind == extract.KindMethod || m.Kind == extract.KindConstructor ||
			(containerKind == extract.KindRecord && m.Kind == extract.KindField)
		data := m.Kind == extract.KindField || m.Kind == extract.KindProperty
		if refKind == extract.RefProperty && (data || callable) || refKind != extract.RefProperty && callable {
			out = append(out, m)
		}
	}
	return out
}

// unqualifiedMethod trata `m()` em Java: método do próprio tipo ou import
// static. Em TS uma chamada sem receiver já chega como RefCall.
func (r *Resolver) unqualifiedMethod(ctx *fileContext, ref store.Ref) outcome {
	if ctx.file.Lang != lang.Java {
		// Só Java chama método sem receptor (this implícito). Nas outras
		// linguagens um receptor vazio é uma expressão que o extrator não
		// entendeu (`(T{}).M()`, `"".join(xs)`): buscar pelo nome seria chute.
		return unresolved
	}
	if ref.ContainerSymbol != nil {
		if own := r.ownMethod(ctx, *ref.ContainerSymbol, ref); own.resolution != store.Unresolved {
			return own
		}
	}
	return r.staticImport(ctx, ref.Name)
}

// ownMethod procura um método com o nome no tipo que envolve a ref
// (sobrecargas separadas pela aridade) e, se não há, nos tipos pai.
func (r *Resolver) ownMethod(ctx *fileContext, containerID int64, ref store.Ref) outcome {
	typeName := ""
	for _, s := range ctx.symbols {
		if s.ID != containerID {
			continue
		}
		typeName = s.Container
		if isType(s.Kind) {
			typeName = s.Name
		}
	}
	if typeName == "" {
		return unresolved
	}
	var candidates []store.Symbol
	var typeSym *store.Symbol
	for i, s := range ctx.symbols {
		if s.Name == typeName && isType(s.Kind) && typeSym == nil {
			typeSym = &ctx.symbols[i]
		}
		if s.Container == typeName && s.Name == ref.Name && (s.Kind == extract.KindMethod || s.Kind == extract.KindConstructor) {
			candidates = append(candidates, s)
		}
	}
	out := pick(r.narrowOverloads(ctx, candidates, ref), unresolved)
	if out.resolution == store.Unresolved && typeSym != nil {
		return r.inheritedMember(ctx, *typeSym, ref, map[int64]bool{}, 0)
	}
	return out
}

// isSubtype diz se o tipo `child` estende ou implementa `parent` dentro do
// índice, resolvendo `child` no contexto do arquivo.
func (r *Resolver) isSubtype(ctx *fileContext, child, parent string) bool {
	out := r.resolveTypeName(ctx, child)
	if out.symbol == nil {
		return false
	}
	visited := map[int64]bool{}
	var walk func(sym store.Symbol, depth int) bool
	walk = func(sym store.Symbol, depth int) bool {
		if depth > maxInheritanceDepth || visited[sym.ID] {
			return false
		}
		visited[sym.ID] = true
		parents, _ := r.parentTypes(sym)
		for _, p := range parents {
			if p.Name == parent || walk(p, depth+1) {
				return true
			}
		}
		return false
	}
	return walk(*out.symbol, 0)
}

func symbolID(out outcome) *int64 {
	if out.symbol == nil {
		return nil
	}
	return &out.symbol.ID
}

func count(stats *Stats, resolution string) {
	switch resolution {
	case store.Resolved:
		stats.Resolved++
	case store.Ambiguous:
		stats.Ambiguous++
	case store.External:
		stats.External++
	default:
		stats.Unresolved++
	}
}

// edgeFor deriva a edge do kind da ref, quando ela resolveu e tem container.
func edgeFor(ref store.Ref, out outcome) (store.Edge, bool) {
	if out.symbol == nil || ref.ContainerSymbol == nil {
		return store.Edge{}, false
	}
	var kind string
	switch ref.Kind {
	case extract.RefCall, extract.RefNew, extract.RefMethod, extract.RefJSX:
		// Renderizar <Button /> é chamar Button: entra em callers.
		kind = store.EdgeCalls
	case extract.RefType, extract.RefIdentifier, extract.RefProperty:
		kind = store.EdgeReferences
	case extract.RefExtends:
		kind = store.EdgeExtends
	case extract.RefImplements:
		kind = store.EdgeImplements
	case extract.RefAnnotation:
		kind = store.EdgeAnnotatedBy
	default:
		return store.Edge{}, false
	}
	return store.Edge{From: *ref.ContainerSymbol, To: out.symbol.ID, Kind: kind}, true
}

func isType(kind string) bool {
	switch kind {
	case extract.KindClass, extract.KindInterface, extract.KindEnum, extract.KindRecord, extract.KindAnnotation, extract.KindNamespace:
		return true
	}
	return false
}

var javaTypeKinds = map[string]bool{
	extract.KindClass: true, extract.KindInterface: true, extract.KindEnum: true,
	extract.KindRecord: true, extract.KindAnnotation: true,
}

func (r *Resolver) fileByPath(path string) *store.File {
	if cached, ok := r.files[path]; ok {
		return cached
	}
	f, ok, err := r.store.FileByPath(path)
	if err != nil || !ok {
		r.files[path] = nil
		return nil
	}
	r.files[path] = &f
	return &f
}
