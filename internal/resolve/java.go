package resolve

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/store"
)

// javaLang são tipos de java.lang, visíveis sem import; qualquer outro nome
// sem símbolo indexado fica unresolved em vez de external.
var javaLang = map[string]bool{
	"String": true, "Object": true, "Integer": true, "Long": true, "Double": true, "Float": true,
	"Boolean": true, "Character": true, "Byte": true, "Short": true, "Void": true, "Math": true,
	"System": true, "Exception": true, "RuntimeException": true, "Throwable": true, "Error": true,
	"Iterable": true, "Comparable": true, "Runnable": true, "Thread": true, "StringBuilder": true,
	"Override": true, "Deprecated": true, "SuppressWarnings": true, "FunctionalInterface": true,
	"Class": true, "Enum": true, "Record": true, "Number": true, "CharSequence": true,
	"IllegalArgumentException": true, "IllegalStateException": true, "NullPointerException": true,
	"UnsupportedOperationException": true, "AutoCloseable": true, "Cloneable": true, "SafeVarargs": true,
}

func (r *Resolver) resolveJavaImport(im store.Import) outcome {
	if im.IsWildcard {
		return r.javaWildcardImport(im)
	}
	kinds := javaTypeKinds
	if im.Kind == extract.ImportJavaStatic {
		kinds = map[string]bool{extract.KindMethod: true, extract.KindField: true}
	}
	candidates, err := r.qualified(im.Module, kinds)
	if err != nil {
		return unresolved
	}
	return pick(candidates, external)
}

// javaWildcardImport marca `a.b.*` como resolvido se a package (ou o tipo,
// no caso static) existe no índice; senão external.
func (r *Resolver) javaWildcardImport(im store.Import) outcome {
	if im.Kind == extract.ImportJavaStatic {
		types, err := r.qualified(im.Module, javaTypeKinds)
		if err != nil || len(types) == 0 {
			return external
		}
		return outcome{resolution: store.Resolved, file: &types[0].FileID}
	}
	exists, err := r.packageExists(im.Module)
	if err != nil || !exists {
		return external
	}
	return outcome{resolution: store.Resolved}
}

// resolveJavaName aplica: tipo do próprio arquivo -> import explícito ->
// mesma package -> wildcards (ambíguo se mais de um bate) -> java.lang ->
// unresolved.
func (r *Resolver) resolveJavaName(ctx *fileContext, name string, kinds map[string]bool) outcome {
	var local []store.Symbol
	for _, s := range ctx.symbols {
		if s.Name == name && kinds[s.Kind] {
			local = append(local, s)
		}
	}
	if len(local) > 0 {
		return resolved(local[0])
	}
	for _, im := range ctx.imports {
		if !im.IsWildcard && im.Kind == extract.ImportJava && im.LocalName == name {
			return ctx.importOutcome[im.ID]
		}
	}
	if ctx.file.Package != "" {
		same, err := r.qualified(ctx.file.Package+"."+name, kinds)
		if err == nil && len(same) > 0 {
			return pick(same, unresolved)
		}
	}
	var viaWildcard []store.Symbol
	for _, im := range ctx.imports {
		if !im.IsWildcard || im.Kind != extract.ImportJava {
			continue
		}
		found, err := r.qualified(im.Module+"."+name, kinds)
		if err == nil {
			viaWildcard = append(viaWildcard, found...)
		}
	}
	if len(viaWildcard) > 0 {
		return pick(viaWildcard, unresolved)
	}
	if javaLang[name] {
		return external
	}
	return unresolved
}

// resolveJavaQualified trata tipos escritos com o pacote na frente
// (`java.util.Map`, `com.acme.pricing.Money`).
func (r *Resolver) resolveJavaQualified(qualifier, name string) outcome {
	if isJDKPackage(qualifier) {
		return external
	}
	found, err := r.qualified(qualifier+"."+name, javaTypeKinds)
	if err != nil {
		return unresolved
	}
	return pick(found, unresolved)
}

func isJDKPackage(pkg string) bool {
	return strings.HasPrefix(pkg, "java.") || strings.HasPrefix(pkg, "javax.") || strings.HasPrefix(pkg, "jakarta.")
}

// staticImport resolve `m()` via `import static a.b.C.m` ou `import static a.b.C.*`.
func (r *Resolver) staticImport(ctx *fileContext, name string) outcome {
	for _, im := range ctx.imports {
		if im.Kind != extract.ImportJavaStatic {
			continue
		}
		if !im.IsWildcard && im.LocalName == name {
			return ctx.importOutcome[im.ID]
		}
		if im.IsWildcard {
			found, err := r.qualified(im.Module+"."+name, map[string]bool{extract.KindMethod: true})
			if err == nil && len(found) > 0 {
				return pick(found, unresolved)
			}
		}
	}
	return unresolved
}

func (r *Resolver) qualified(name string, kinds map[string]bool) ([]store.Symbol, error) {
	all, err := r.symbolsByQualifiedName(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	var out []store.Symbol
	for _, s := range all {
		if kinds[s.Kind] {
			out = append(out, s)
		}
	}
	return out, nil
}
