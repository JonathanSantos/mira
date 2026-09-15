package typescript

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/parser"
)

// functionScopes são os nós cujo corpo é o escopo de parâmetros e de `var`.
var functionScopes = []string{"arrow_function", "function_expression", "function_declaration",
	"generator_function", "generator_function_declaration", "method_definition"}

// collectDeclarations preenche locals (parâmetros e variáveis de função),
// localTypes (o tipo de cada um, com escopo) e varTypes (variáveis de topo e
// campos de classe -> tipo declarado).
func (x *extraction) collectDeclarations(root parser.Node) {
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "required_parameter", "optional_parameter", "rest_parameter":
			x.parameter(n)
		case "arrow_function":
			// `x => …`: o parâmetro é o primeiro filho, sem formal_parameters; em
			// `(a) => b` o identifier direto é o corpo, não um parâmetro.
			if kids := n.NamedChildren(); len(kids) > 0 && kids[0].Is("identifier") {
				x.locals[kids[0].Text()] = true
				x.declareLocal(kids[0], n, x.callbackElement(n))
			}
		case "variable_declarator":
			x.declarator(n)
		case "catch_clause":
			if id := n.Child("identifier"); !id.IsNil() {
				x.locals[id.Text()] = true
				x.declareLocal(id, n, "")
			}
		case "for_in_statement":
			// `for (const item of items)`: sem tipo, mas esconde outro item de fora.
			if id := n.Child("identifier"); !id.IsNil() {
				x.declareLocal(id, n, "")
			}
		case "public_field_definition", "field_definition":
			x.fieldType(n)
		case "object_pattern", "array_pattern":
			x.pattern(n)
		}
		return true
	})
}

func (x *extraction) parameter(n parser.Node) {
	id := n.Child("identifier")
	if id.IsNil() {
		return
	}
	x.locals[id.Text()] = true
	t := typeName(n.Child("type_annotation"))
	if params := n.Parent(); t == "" && params.NamedChildren()[0].StartByte() == n.StartByte() {
		t = x.callbackElement(params.Parent())
	}
	x.declareLocal(id, n.Ancestor(functionScopes...), t)
	if t != "" && (!n.Child("accessibility_modifier").IsNil() || !n.AnonChild("readonly").IsNil()) {
		x.varTypes[id.Text()] = t // `constructor(private svc: Svc)` também declara o campo `this.svc`
	}
}

func (x *extraction) declarator(n parser.Node) {
	id := declaratorName(n)
	if id.IsNil() {
		return // desestruturação: pattern cuida dos nomes
	}
	if !n.Ancestor("statement_block", "arrow_function", "function_expression", "class_body").IsNil() {
		x.locals[id.Text()] = true
	}
	t := declaredType(n)
	scope := n.Ancestor("statement_block", "for_statement", "for_in_statement")
	if n.Parent().Is("variable_declaration") {
		scope = n.Ancestor(functionScopes...) // `var` vale na função inteira
	}
	if !scope.IsNil() {
		x.declareLocal(id, scope, t)
		return
	}
	if t != "" {
		x.varTypes[id.Text()] = t
	}
}

// declaredType é o tipo escrito na declaração ou o que o valor diz: `new T()`
// vira T e `f()` vira `call:f`, que o resolvedor troca pelo tipo de retorno
// (anotado ou inferido) de f.
func declaredType(n parser.Node) string {
	if t := typeName(n.Child("type_annotation")); t != "" {
		return t
	}
	value := lastNamedChild(n)
	if value.Is("await_expression") {
		value = lastNamedChild(value)
	}
	switch {
	case value.Is("new_expression"):
		return value.Child("identifier").Text()
	case value.Is("call_expression"):
		if fn := value.NamedChildren()[0]; fn.Is("identifier") && !builtins[fn.Text()] {
			return "call:" + fn.Text()
		}
	}
	return ""
}

func (x *extraction) fieldType(n parser.Node) {
	name := n.Child("property_identifier")
	if name.IsNil() {
		return
	}
	if t := typeName(n.Child("type_annotation")); t != "" {
		x.varTypes[name.Text()] = t
		return
	}
	if value := lastNamedChild(n); value.Is("new_expression") {
		if ctor := value.Child("identifier"); !ctor.IsNil() {
			x.varTypes[name.Text()] = ctor.Text()
		}
	}
}

// pattern marca como locais os nomes de um destructuring dentro de função;
// num parâmetro (`({ a }) => …`) eles valem na função inteira. Quando a
// origem tem tipo, cada nome leva o tipo do seu membro:
// `const { field } = useController()` dá a field `call:useController["field"]`.
func (x *extraction) pattern(n parser.Node) {
	if n.Parent().Is(patternTypes...) {
		return // parte de um pattern maior, que já declara os nomes
	}
	scope := n.Ancestor("statement_block", "arrow_function", "function_expression", "formal_parameters")
	if scope.IsNil() {
		return
	}
	if scope.Is("formal_parameters") {
		scope = scope.Parent()
	}
	x.bindNames(n, scope, x.patternSource(n))
}

var patternTypes = []string{"pair_pattern", "object_pattern", "array_pattern", "object_assignment_pattern", "assignment_pattern", "rest_pattern"}

// patternSource é o tipo do valor desestruturado: a anotação do parâmetro ou
// da variável, a chamada (`call:f`) ou o tipo de uma variável conhecida.
func (x *extraction) patternSource(n parser.Node) string {
	parent := n.Parent()
	switch parent.Type() {
	case "required_parameter", "optional_parameter":
		return typeName(parent.Child("type_annotation"))
	case "variable_declarator":
		if t := declaredType(parent); t != "" {
			return t
		}
		if value := lastNamedChild(parent); value.Is("identifier") && value.StartByte() != n.StartByte() {
			t, _ := x.typeOf(value)
			return t
		}
	}
	return ""
}

// bindNames declara os nomes que um pattern cria, com o tipo do membro de
// source quando ele é conhecido. Num valor default (`{ onSubmit = noop }`) só
// o lado esquerdo declara: o default é um uso.
func (x *extraction) bindNames(n, scope parser.Node, source string) {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		x.locals[n.Text()] = true
		x.declareLocal(n, scope, source)
	case "object_pattern":
		for _, c := range n.NamedChildren() {
			switch c.Type() {
			case "shorthand_property_identifier_pattern":
				x.bindNames(c, scope, memberOf(source, c.Text()))
			case "pair_pattern":
				x.bindNames(lastNamedChild(c), scope, memberOf(source, propertyKey(c.NamedChildren()[0])))
			case "object_assignment_pattern":
				if left := c.NamedChildren()[0]; left.Is("shorthand_property_identifier_pattern") {
					x.bindNames(left, scope, memberOf(source, left.Text()))
				} else {
					x.bindNames(left, scope, "")
				}
			case "rest_pattern":
				x.bindNames(lastNamedChild(c), scope, "")
			}
		}
	case "array_pattern":
		for _, c := range n.NamedChildren() {
			x.bindNames(c, scope, "")
		}
	case "assignment_pattern":
		x.bindNames(n.NamedChildren()[0], scope, source)
	case "rest_pattern":
		x.bindNames(lastNamedChild(n), scope, "")
	}
}

// memberOf é o tipo derivado do membro name de source (`Props["app"]`).
func memberOf(source, name string) string {
	if source == "" || name == "" {
		return ""
	}
	return source + `["` + name + `"]`
}

// propertyKey é o nome de uma chave de pattern (`a` ou `'a'`); chave
// computada (`[k]`) não tem nome.
func propertyKey(key parser.Node) string {
	switch key.Type() {
	case "property_identifier":
		return key.Text()
	case "string":
		return strings.Trim(key.Text(), `"'`)
	}
	return ""
}

// elementCallbacks são métodos de array cujo callback recebe o elemento no
// primeiro parâmetro.
var elementCallbacks = map[string]bool{"forEach": true, "map": true, "filter": true, "find": true, "findIndex": true,
	"findLast": true, "findLastIndex": true, "some": true, "every": true, "flatMap": true}

// callbackElement é o tipo do primeiro parâmetro sem anotação de um callback
// de array (`items.forEach((item) => …)`): o elemento do tipo de items.
func (x *extraction) callbackElement(fn parser.Node) string {
	args := fn.Parent()
	if !fn.Is("arrow_function", "function_expression") || !args.Is("arguments") || args.NamedChildren()[0].StartByte() != fn.StartByte() {
		return ""
	}
	call := args.Parent()
	if !call.Is("call_expression") {
		return ""
	}
	callee := call.NamedChildren()[0]
	if !callee.Is("member_expression") {
		return ""
	}
	kids := callee.NamedChildren()
	if object := kids[0]; object.Is("identifier") && elementCallbacks[kids[len(kids)-1].Text()] {
		if t, _ := x.typeOf(object); t != "" {
			return t + "[number]"
		}
	}
	return ""
}

func (x *extraction) declareLocal(id, scope parser.Node, typ string) {
	if scope.IsNil() {
		return
	}
	x.localTypes[id.Text()] = append(x.localTypes[id.Text()], localType{start: scope.StartByte(), end: scope.EndByte(), pos: id.StartByte(), typ: typ})
}

// typeOf é o tipo de um identificador na posição: o da declaração do escopo
// mais interno que o contém ou, sem declaração local, o do nome no arquivo.
// ok diz se o nome foi declarado, mesmo sem tipo: uma local sem tipo esconde
// a variável de topo com o mesmo nome.
func (x *extraction) typeOf(id parser.Node) (string, bool) {
	at := id.StartByte()
	var best localType
	found := false
	for _, l := range x.localTypes[id.Text()] {
		if at < l.start || at >= l.end {
			continue
		}
		if !found || l.start > best.start || l.start == best.start && l.pos <= at && l.pos > best.pos {
			best, found = l, true
		}
	}
	if found {
		return best.typ, true
	}
	t, ok := x.varTypes[id.Text()]
	return t, ok
}
