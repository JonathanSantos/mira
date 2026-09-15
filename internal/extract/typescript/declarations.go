package typescript

import "github.com/JonathanSantos/mira/internal/parser"

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
				x.declareLocal(kids[0], n, "")
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
// num parâmetro (`({ a }) => …`) eles valem na função inteira.
func (x *extraction) pattern(n parser.Node) {
	scope := n.Ancestor("statement_block", "arrow_function", "function_expression", "formal_parameters")
	if scope.IsNil() {
		return
	}
	if scope.Is("formal_parameters") {
		scope = scope.Parent()
	}
	x.bindNames(n, scope)
}

// bindNames declara os nomes que um pattern cria. Num valor default
// (`{ onSubmit = noop }`) só o lado esquerdo declara: o default é um uso.
func (x *extraction) bindNames(n, scope parser.Node) {
	n.Walk(func(c parser.Node) bool {
		switch c.Type() {
		case "shorthand_property_identifier_pattern", "identifier":
			x.locals[c.Text()] = true
			x.declareLocal(c, scope, "")
		case "object_assignment_pattern", "assignment_pattern":
			if kids := c.NamedChildren(); len(kids) > 0 {
				x.bindNames(kids[0], scope)
			}
			return false
		}
		return true
	})
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
