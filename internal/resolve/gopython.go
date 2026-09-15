package resolve

import (
	"bufio"
	"bytes"
	"go/build"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/extract/golang"
	"github.com/JonathanSantos/mira/internal/extract/python"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/store"
)

// isPackageReceiver diz se o ReceiverType marca um alias de import (Go) ou
// um módulo (Python) em vez de um tipo.
func isPackageReceiver(receiverType string) bool {
	return receiverType == golang.PackageReceiver || receiverType == python.ModuleReceiver
}

// goModule lê o caminho do módulo em go.mod, uma vez.
func (r *Resolver) goModule() string {
	if r.goMod != nil {
		return *r.goMod
	}
	module := ""
	if data, err := os.ReadFile(filepath.Join(r.root, "go.mod")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, "module ") {
				module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
				break
			}
		}
	}
	r.goMod = &module
	return module
}

// resolveGoImport mapeia um caminho de import do próprio módulo para o
// diretório indexado; qualquer outro caminho é external.
func (r *Resolver) resolveGoImport(im store.Import) outcome {
	module := r.goModule()
	dir := ""
	switch {
	case module != "" && im.Module == module:
		dir = "."
	case module != "" && strings.HasPrefix(im.Module, module+"/"):
		dir = strings.TrimPrefix(im.Module, module+"/")
	default:
		return external
	}
	files, err := r.filesInPackage(dir, -1)
	if err != nil || len(files) == 0 {
		return external
	}
	return outcome{resolution: store.Resolved, file: &files[0]}
}

// resolveGoName procura um nome de topo no arquivo e depois no resto do
// pacote (diretório).
// pythonEnclosingDef acha a definição aninhada (`def` ou `class` dentro de
// uma função) visível na linha: a da função mais interna que contém o uso.
// Homônimas em funções diferentes não se misturam; duas no mesmo escopo
// ficam ambíguas.
func pythonEnclosingDef(symbols []store.Symbol, name string, line int) (outcome, bool) {
	var found []store.Symbol
	bestSize := 0
	for i := range symbols {
		s := symbols[i]
		if s.Name != name || s.Container == "" || line == 0 {
			continue
		}
		scope := pythonScopeOf(symbols, s)
		if scope == nil || line < scope.StartLine || line > scope.EndLine {
			continue
		}
		switch size := scope.EndLine - scope.StartLine; {
		case len(found) == 0 || size < bestSize:
			found, bestSize = []store.Symbol{s}, size
		case size == bestSize:
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		return unresolved, false
	}
	return pick(found, unresolved), true
}

// pythonScopeOf é a função ou o método que declara s, achado pelo nome
// qualificado; nil quando s é membro de classe.
func pythonScopeOf(symbols []store.Symbol, s store.Symbol) *store.Symbol {
	parent := strings.TrimSuffix(s.QualifiedName, "."+s.Name)
	for i := range symbols {
		if p := &symbols[i]; p.QualifiedName == parent && (p.Kind == extract.KindFunction || p.Kind == extract.KindMethod) {
			return p
		}
	}
	return nil
}

// pythonImportScope é o intervalo de linhas da função ou classe mais interna
// que contém o import; local é false para import no topo do arquivo.
func pythonImportScope(symbols []store.Symbol, line int) (start, end int, local bool) {
	for _, s := range symbols {
		if s.Kind != extract.KindFunction && s.Kind != extract.KindMethod && s.Kind != extract.KindClass {
			continue
		}
		if line < s.StartLine || line > s.EndLine {
			continue
		}
		if !local || s.EndLine-s.StartLine < end-start {
			start, end, local = s.StartLine, s.EndLine, true
		}
	}
	return start, end, local
}

func (r *Resolver) resolveGoName(ctx *fileContext, name string) outcome {
	for _, s := range ctx.symbols {
		if s.Name == name && s.Container == "" {
			return resolved(s)
		}
	}
	same, err := r.symbolsInPackage(ctx.file.Package, name)
	if err != nil {
		return unresolved
	}
	return pick(r.preferDefaultBuild(same), unresolved)
}

// preferDefaultBuild desempata uma definição repetida em arquivos com build
// tags opostas (`binding.go` e `binding_nomsgpack.go`): ficam as que o
// `go build` padrão compila nesta máquina, se alguma for; senão todas.
func (r *Resolver) preferDefaultBuild(symbols []store.Symbol) []store.Symbol {
	if len(symbols) < 2 {
		return symbols
	}
	var kept []store.Symbol
	for _, s := range symbols {
		if r.inDefaultBuild(s.FileID) {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return symbols
	}
	return kept
}

// inDefaultBuild diz se o build padrão compila o arquivo: build tags e
// sufixos como _windows.go, lidos do disco. Na dúvida, compila.
func (r *Resolver) inDefaultBuild(fileID int64) bool {
	ok, _ := remember(r.lookups.defaultBuild, fileID, func() (bool, error) {
		f, found, err := r.fileByID(fileID)
		if err != nil || !found || f.Lang != lang.Go {
			return true, nil
		}
		abs := filepath.Join(r.root, filepath.FromSlash(f.Path))
		match, err := build.Default.MatchFile(filepath.Dir(abs), filepath.Base(abs))
		return err != nil || match, nil
	})
	return ok
}

// packageMember resolve `alias.Name` (Go) ou `mod.Name` / `mod.sub.Name`
// (Python) pelo import do alias: o alvo é um símbolo exportado do pacote
// ou do módulo.
func (r *Resolver) packageMember(ctx *fileContext, alias string, path []string, name string) outcome {
	var im *store.Import
	for i := range ctx.imports {
		if ctx.imports[i].LocalName == alias && !ctx.imports[i].IsWildcard {
			im = &ctx.imports[i]
			break
		}
	}
	if im == nil {
		return unresolved
	}
	out := ctx.importOutcome[im.ID]
	if out.resolution == store.External {
		return external
	}
	if ctx.file.Lang == lang.Go {
		return r.goPackageSymbol(out, name)
	}
	module := im.Module
	if im.ImportedName != "" {
		// `from a import b as c`: c é um símbolo de a (classe, função) ou o
		// submódulo a.b.
		if out.symbol != nil {
			return r.memberOrInherited(ctx, *out.symbol, store.Ref{Name: name, Kind: extract.RefProperty, Arity: -1})
		}
		if out.file != nil && len(path) == 0 {
			return r.symbolInFile(*out.file, name)
		}
		module = joinModule(module, im.ImportedName)
	}
	if len(path) > 0 {
		module = joinModule(module, strings.Join(path, "."))
	}
	file := r.pythonModuleFile(ctx, module)
	if file == nil && out.file != nil && len(path) == 0 {
		return r.symbolInFile(*out.file, name)
	}
	if file == nil {
		return unresolved
	}
	return r.symbolInFile(file.ID, name)
}

func (r *Resolver) goPackageSymbol(pkg outcome, name string) outcome {
	if pkg.file == nil {
		return unresolved
	}
	f, ok, err := r.fileByID(*pkg.file)
	if err != nil || !ok {
		return unresolved
	}
	symbols, err := r.symbolsInPackage(f.Package, name)
	if err != nil {
		return unresolved
	}
	var exported []store.Symbol
	for _, s := range symbols {
		if s.Exported {
			exported = append(exported, s)
		}
	}
	return pick(r.preferDefaultBuild(exported), unresolved)
}

// symbolInFile acha um nome de topo no arquivo; se o arquivo só o
// re-exporta (`from .person import Person` num __init__.py), segue o
// import, até maxReexportDepth.
func (r *Resolver) symbolInFile(fileID int64, name string) outcome {
	return r.exportOf(fileID, name, 0)
}

const maxReexportDepth = 5

// exportOf acha um nome de topo no arquivo ou segue o import que o traz
// (re-export em __init__.py), um nível por vez até maxReexportDepth. Lê só os
// imports do próprio arquivo, sem montar o contexto dele: montar resolveria
// todos os imports daquele arquivo, e cada um seguiria outros re-exports com
// a profundidade zerada. Em pacotes com `from . import x` dentro de
// __init__.py (o flask) isso virava recursão combinatória: 89 s e 10 GB.
func (r *Resolver) exportOf(fileID int64, name string, depth int) outcome {
	symbols, err := r.symbolsOfFile(fileID)
	if err != nil {
		return unresolved
	}
	var found []store.Symbol
	for _, s := range symbols {
		if s.Name == name && s.Container == "" {
			found = append(found, s)
		}
	}
	if len(found) > 0 || depth >= maxReexportDepth {
		return pick(found, unresolved)
	}
	file, ok, err := r.fileByID(fileID)
	if err != nil || !ok || file.Lang != lang.Python {
		return unresolved
	}
	imports, err := r.importsOfFile(fileID)
	if err != nil {
		return unresolved
	}
	local := &fileContext{file: file}
	for _, im := range imports {
		if im.IsWildcard || im.LocalName != name {
			continue
		}
		out := r.resolvePythonImportAt(local, im, depth+1)
		if out.symbol != nil || out.resolution == store.External {
			return out
		}
		if out.file != nil {
			return r.exportOf(*out.file, im.ImportedName, depth+1)
		}
	}
	for _, im := range imports {
		if !im.IsWildcard {
			continue
		}
		if out := r.resolvePythonImportAt(local, im, depth+1); out.file != nil {
			if found := r.exportOf(*out.file, name, depth+1); found.symbol != nil {
				return found
			}
		}
	}
	return unresolved
}

// resolvePythonImport localiza o arquivo de um módulo: relativo ao
// arquivo (pontos) ou absoluto a partir da raiz, de src/ e dos diretórios
// acima do arquivo. `from m import x` resolve x dentro do módulo, ou como
// submódulo m/x.py.
func (r *Resolver) resolvePythonImport(ctx *fileContext, im store.Import) outcome {
	return r.resolvePythonImportAt(ctx, im, 0)
}

// resolvePythonImportAt é resolvePythonImport dentro de uma cadeia de
// re-exports: a profundidade segue adiante em vez de recomeçar.
func (r *Resolver) resolvePythonImportAt(ctx *fileContext, im store.Import, depth int) outcome {
	file := r.pythonModuleFile(ctx, im.Module)
	if im.ImportedName == "" || im.IsWildcard {
		if file == nil {
			return r.pythonMissing(im.Module)
		}
		return outcome{resolution: store.Resolved, file: &file.ID}
	}
	if file != nil {
		if out := r.exportOf(file.ID, im.ImportedName, depth); out.symbol != nil {
			return out
		}
	}
	if sub := r.pythonModuleFile(ctx, joinModule(im.Module, im.ImportedName)); sub != nil {
		return outcome{resolution: store.Resolved, file: &sub.ID}
	}
	if file != nil {
		return outcome{resolution: store.Resolved, file: &file.ID} // módulo achado, nome não: sem símbolo
	}
	return r.pythonMissing(im.Module)
}

// joinModule anexa um nome a um módulo: `from . import nodes` é `.nodes`,
// não `..nodes` (que seria o pacote pai).
func joinModule(module, name string) string {
	if strings.HasSuffix(module, ".") {
		return module + name
	}
	return module + "." + name
}

// pythonMissing: um import relativo que não existe é um erro do código
// (unresolved); um absoluto que não está no índice é uma dependência.
func (r *Resolver) pythonMissing(module string) outcome {
	if strings.HasPrefix(module, ".") {
		return unresolved
	}
	return external
}

// pythonModuleFile devolve o arquivo indexado de um módulo, ou nil.
func (r *Resolver) pythonModuleFile(ctx *fileContext, module string) *store.File {
	dots := 0
	for dots < len(module) && module[dots] == '.' {
		dots++
	}
	rest := strings.ReplaceAll(module[dots:], ".", "/")
	var bases []string
	dir := path.Dir(ctx.file.Path)
	if dots > 0 {
		for i := 1; i < dots; i++ {
			dir = path.Dir(dir)
		}
		bases = []string{dir}
	} else {
		bases = []string{".", "src"}
		for d := path.Dir(ctx.file.Path); d != "." && d != "/"; d = path.Dir(d) {
			bases = append(bases, d)
		}
	}
	for _, base := range bases {
		target := rest
		if base != "." {
			target = path.Join(base, rest)
		}
		candidates := []string{target + ".py", path.Join(target, "__init__.py")}
		if rest == "" {
			candidates = []string{path.Join(base, "__init__.py")}
		}
		for _, c := range candidates {
			if f := r.fileByPath(path.Clean(c)); f != nil {
				return f
			}
		}
	}
	return nil
}

// resolvePythonName: o import feito na função (ou classe) que contém o uso,
// o símbolo de topo do arquivo, o import de topo (`from a import b`), senão
// unresolved. Builtins não chegam aqui. Com line 0 (nome de tipo), só o topo.
func (r *Resolver) resolvePythonName(ctx *fileContext, name string, line int) outcome {
	var inner *store.Import
	innerSize := 0
	for i := range ctx.imports {
		im := &ctx.imports[i]
		if im.LocalName != name || im.IsWildcard {
			continue
		}
		start, end, local := pythonImportScope(ctx.symbols, im.Line)
		if !local || line < start || line > end {
			continue
		}
		if inner == nil || end-start < innerSize {
			inner, innerSize = im, end-start
		}
	}
	if inner != nil {
		return ctx.importOutcome[inner.ID]
	}
	if out, ok := pythonEnclosingDef(ctx.symbols, name, line); ok {
		return out
	}
	for _, s := range ctx.symbols {
		if s.Name == name && s.Container == "" {
			return resolved(s)
		}
	}
	for _, im := range ctx.imports {
		if im.LocalName != name || im.IsWildcard {
			continue
		}
		if _, _, local := pythonImportScope(ctx.symbols, im.Line); !local {
			return ctx.importOutcome[im.ID]
		}
	}
	for _, im := range ctx.imports {
		if !im.IsWildcard {
			continue
		}
		if out := ctx.importOutcome[im.ID]; out.file != nil {
			if found := r.symbolInFile(*out.file, name); found.symbol != nil {
				return found
			}
		}
	}
	return unresolved
}
