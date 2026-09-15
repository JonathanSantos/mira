package resolve

import (
	"path"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/store"
)

// sourceExts são as extensões tentadas, em ordem, ao resolver um módulo
// relativo sem extensão. index.* cobre diretórios (barrels).
var sourceExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}

// assetExts são imports que nunca apontam para código: ficam external sem
// erro.
var assetExts = map[string]bool{
	".css": true, ".scss": true, ".sass": true, ".less": true, ".svg": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".json": true, ".ico": true, ".woff": true, ".woff2": true, ".ttf": true,
	".mp3": true, ".mp4": true, ".html": true, ".md": true, ".txt": true,
}

// resolveModule localiza o arquivo de um especificador de módulo.
// Devolve (file, external): file nil e external false = relativo mas não
// encontrado. Um especificador não relativo passa pelo tsconfig (`paths`,
// `baseUrl`) antes de cair em external.
func (r *Resolver) resolveModule(fromPath, module string) (*store.File, bool) {
	if !isRelative(module) {
		if r.tsconfig == nil {
			r.tsconfig = loadTSConfig(r.root)
		}
		for _, rewritten := range r.tsconfig.rewrite(module) {
			for _, candidate := range moduleCandidates(rewritten) {
				if f := r.fileByPath(candidate); f != nil {
					return f, false
				}
			}
		}
		return nil, true
	}
	target := path.Join(path.Dir(fromPath), module)
	if assetExts[path.Ext(target)] {
		return nil, true
	}
	for _, candidate := range moduleCandidates(target) {
		if f := r.fileByPath(candidate); f != nil {
			return f, false
		}
	}
	return nil, false
}

func isRelative(module string) bool {
	return strings.HasPrefix(module, "./") || strings.HasPrefix(module, "../") || module == "." || module == ".."
}

// moduleCandidates lista os caminhos possíveis: o próprio, com cada
// extensão, sem a extensão .js/.jsx (ESM que importa .ts como .js) e
// index.* dentro do diretório.
func moduleCandidates(target string) []string {
	out := []string{target}
	base := target
	if ext := path.Ext(target); ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
		base = strings.TrimSuffix(target, ext)
	}
	for _, ext := range sourceExts {
		out = append(out, base+ext)
	}
	for _, ext := range sourceExts {
		out = append(out, path.Join(target, "index"+ext))
	}
	return out
}

func (r *Resolver) resolveTSImport(ctx *fileContext, im store.Import) outcome {
	file, isExternal := r.resolveModule(ctx.file.Path, im.Module)
	if isExternal {
		return external
	}
	if file == nil {
		return unresolved
	}
	if im.ImportedName == "" || im.ImportedName == "*" {
		return outcome{resolution: store.Resolved, file: &file.ID}
	}
	out := r.lookupExport(file.ID, im.ImportedName, map[exportKey]bool{})
	if out.file == nil {
		out.file = &file.ID
	}
	return out
}

type exportKey struct {
	file int64
	name string
}

// lookupExport acha o símbolo que um arquivo exporta sob um nome, seguindo
// re-exports (`export { a } from`, `export * from`) até a definição real.
// visited protege contra ciclos de barrel.
func (r *Resolver) lookupExport(fileID int64, name string, visited map[exportKey]bool) outcome {
	key := exportKey{fileID, name}
	if visited[key] {
		return unresolved
	}
	visited[key] = true

	direct, err := r.symbolsByExportName(fileID, name)
	if err != nil {
		return unresolved
	}
	if len(direct) > 0 {
		// Overloads/declarações repetidas do mesmo nome são a mesma entidade.
		return resolved(direct[0])
	}
	file, ok, err := r.fileByID(fileID)
	if err != nil || !ok {
		return unresolved
	}
	imports, err := r.importsOfFile(fileID)
	if err != nil {
		return unresolved
	}
	var found []store.Symbol
	sawExternal := false
	for _, im := range imports {
		if !im.IsReexport {
			continue
		}
		if !im.IsWildcard && im.LocalName != name {
			continue
		}
		if im.IsWildcard && name == "default" {
			continue // `export *` nunca re-exporta o default
		}
		target, isExternal := r.resolveModule(file.Path, im.Module)
		if isExternal {
			sawExternal = true
			continue
		}
		if target == nil {
			continue
		}
		lookup := name
		if !im.IsWildcard {
			lookup = im.ImportedName
		}
		out := r.lookupExport(target.ID, lookup, visited)
		if out.symbol != nil {
			found = append(found, *out.symbol)
		}
	}
	if len(found) == 0 && sawExternal {
		return external
	}
	return pick(found, unresolved)
}

// resolveTSName aplica as regras: definido no arquivo -> local; importado
// -> segue o import; senão, várias definições no repo -> ambíguo, uma ou
// nenhuma -> unresolved (sem import não há como saber).
func (r *Resolver) resolveTSName(ctx *fileContext, name string) outcome {
	var local, nested []store.Symbol
	for _, s := range ctx.symbols {
		if s.Name != name {
			continue
		}
		if s.Container == "" {
			local = append(local, s)
		} else if s.Kind == extract.KindFunction {
			nested = append(nested, s)
		}
	}
	if len(local) > 0 {
		return resolved(local[0])
	}
	for _, im := range ctx.imports {
		if !im.IsReexport && im.LocalName == name {
			if len(nested) > 0 {
				return ambiguous // o import ou a função aninhada: depende do escopo do uso
			}
			return ctx.importOutcome[im.ID]
		}
	}
	// Função aninhada (closure, helper de describe/it) do mesmo arquivo: sem
	// escopo real, a única com esse nome é a resposta; homônimas ficam ambíguas.
	if len(nested) > 0 {
		return pick(nested, unresolved)
	}
	candidates, err := r.symbolsByName(name)
	if err != nil {
		return unresolved
	}
	var exported []store.Symbol
	for _, c := range candidates {
		if c.Exported && c.Container == "" && c.Kind != extract.KindProperty {
			exported = append(exported, c)
		}
	}
	if len(exported) > 1 {
		return ambiguous
	}
	return unresolved
}
