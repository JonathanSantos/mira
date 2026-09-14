package resolve

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/store"
)

// narrowOverloads separa sobrecargas pela chamada: primeiro pelo número de
// argumentos, depois pelo tipo de cada um quando se conhece. `getPet(petId)`
// com petId int só pode ser `getPet(Integer)`. Quando nenhuma assinatura
// bate, devolve a lista que tinha (fica ambígua), em vez de chutar.
func (r *Resolver) narrowOverloads(ctx *fileContext, candidates []store.Symbol, ref store.Ref) []store.Symbol {
	byArity := narrowByArity(candidates, ref.Arity)
	if len(byArity) < 2 || len(ref.ArgTypes) == 0 {
		return byArity
	}
	var out []store.Symbol
	for _, c := range byArity {
		params, ok := extract.Params(c.Signature)
		if ok && r.argsFit(ctx, ref.ArgTypes, params) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return byArity
	}
	return out
}

func narrowByArity(candidates []store.Symbol, arity int) []store.Symbol {
	if arity < 0 || len(candidates) < 2 {
		return candidates
	}
	var out []store.Symbol
	for _, c := range candidates {
		minArgs, maxArgs, ok := extract.ParamBounds(c.Signature)
		if !ok || arity < minArgs || (maxArgs >= 0 && arity > maxArgs) {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return candidates
	}
	return out
}

// argsFit diz se cada argumento cabe no parâmetro da mesma posição (o rest
// absorve os que sobram). Uma subclasse cabe no parâmetro da superclasse.
func (r *Resolver) argsFit(ctx *fileContext, args []string, params []extract.Param) bool {
	for i, a := range args {
		var p extract.Param
		switch {
		case i < len(params):
			p = params[i]
		case len(params) > 0 && params[len(params)-1].Rest:
			p = params[len(params)-1]
		default:
			return false
		}
		if compatible(a, p.Type) {
			continue
		}
		if isTypeName(baseType(a)) && isTypeName(baseType(p.Type)) && r.isSubtype(ctx, baseType(a), baseType(p.Type)) {
			continue
		}
		return false
	}
	return true
}

// boxed liga primitivos Java às classes wrapper.
var boxed = map[string]string{
	"int": "Integer", "long": "Long", "double": "Double", "float": "Float",
	"boolean": "Boolean", "char": "Character", "short": "Short", "byte": "Byte",
}

// widens diz para quais tipos um literal numérico Java (`1`, `1.5`) cabe.
var widens = map[string]map[string]bool{
	"int":    {"long": true, "float": true, "double": true, "short": true, "byte": true, "Long": true, "Float": true, "Double": true, "Number": true, "Object": true},
	"long":   {"float": true, "double": true, "Float": true, "Double": true, "Number": true, "Object": true},
	"float":  {"double": true, "Double": true, "Number": true, "Object": true},
	"double": {"Number": true, "Object": true},
}

// literalKinds são os tipos que o extractor TS atribui a literais.
var literalKinds = map[string]bool{"string": true, "number": true, "boolean": true, "object": true, "array": true, "function": true}

// compatible é conservador: só rejeita quando os dois lados são conhecidos e
// claramente diferentes. Nomes de tipo (`Owner` vs `Person`) diferentes
// rejeitam, então uma subclasse passada a um parâmetro da superclasse não
// separa a sobrecarga; nesse caso a ref fica ambígua, nunca errada.
func compatible(arg, param string) bool {
	arg, param = strings.TrimSpace(arg), strings.TrimSpace(param)
	if arg == "" || param == "" {
		return true
	}
	if strings.Contains(param, "|") {
		for _, alt := range strings.Split(param, "|") {
			if compatible(arg, alt) {
				return true
			}
		}
		return false
	}
	p := baseType(param)
	switch {
	case p == "any" || p == "unknown" || p == "Object" || p == "T":
		return true
	case arg == "null":
		return boxed[p] == "" // qualquer tipo não primitivo
	case arg == p, boxed[arg] == p, boxed[p] == arg:
		return true
	case widens[arg][p]:
		return true
	case arg == "array":
		return strings.HasSuffix(param, "[]") || strings.HasPrefix(param, "Array<") || strings.HasPrefix(param, "readonly ")
	case arg == "function":
		return strings.Contains(param, "=>") || strings.HasPrefix(p, "(")
	case arg == "object":
		return strings.HasPrefix(param, "{") || isTypeName(p)
	case literalKinds[arg]:
		// `'email'` num parâmetro `FieldPath<T>`: um alias pode ser qualquer coisa.
		return isTypeName(p)
	}
	return false
}

// baseType tira generics, arrays e `readonly` de um tipo: `Map<K, V>` ->
// Map, `Money[]` -> Money.
func baseType(t string) string {
	t = strings.TrimPrefix(strings.TrimSpace(t), "readonly ")
	if i := strings.IndexAny(t, "<["); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// isTypeName diz se é um nome de tipo (classe, alias) e não um primitivo.
func isTypeName(t string) bool {
	return t != "" && t[0] >= 'A' && t[0] <= 'Z'
}
