package golang

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// builtins nunca resolvem para um símbolo do repositório.
var builtins = map[string]bool{
	"len": true, "cap": true, "append": true, "make": true, "new": true, "panic": true, "recover": true,
	"print": true, "println": true, "copy": true, "delete": true, "close": true, "min": true, "max": true,
	"clear": true, "complex": true, "real": true, "imag": true, "error": true, "string": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true,
	"uint32": true, "uint64": true, "uintptr": true, "byte": true, "rune": true, "float32": true, "float64": true,
	"bool": true, "any": true, "comparable": true, "nil": true, "true": true, "false": true, "iota": true,
}

// scopes são os nós que limitam onde uma declaração dentro de função vale.
var scopes = []string{"block", "if_statement", "for_statement", "expression_switch_statement", "type_switch_statement",
	"select_statement", "expression_case", "type_case", "default_case", "communication_case"}

// collectDeclarations registra parâmetros e variáveis de função com o
// escopo de cada um: o mesmo nome em duas funções (ou em dois blocos)
// guarda dois tipos, e cada uso enxerga só a declaração visível ali.
func (x *extraction) collectDeclarations(root parser.Node) {
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "parameter_declaration", "variadic_parameter_declaration":
			x.parameter(n)
		case "short_var_declaration":
			if lists := n.ChildrenOf("expression_list"); len(lists) == 2 {
				x.declareAll(n, lists[0].ChildrenOf("identifier"), "", lists[1].NamedChildren())
			}
		case "var_spec", "const_spec":
			typ := simpleType(n.Child("type_identifier", "pointer_type", "slice_type", "map_type", "qualified_type", "generic_type"))
			x.declareAll(n, n.ChildrenOf("identifier"), typ, n.Child("expression_list").NamedChildren())
		case "receive_statement":
			if left := n.Child("expression_list"); !left.IsNil() && !n.AnonChild(":=").IsNil() {
				x.declareAll(n, left.ChildrenOf("identifier"), "", nil) // `case v := <-ch:`
			}
		case "range_clause":
			x.rangeClause(n)
		case "type_switch_statement":
			x.typeSwitch(n)
		}
		return true
	})
}

// parameter declara os nomes de um parâmetro no escopo da função; os de um
// tipo função (`type H func(c *Ctx)`) não declaram nada.
func (x *extraction) parameter(n parser.Node) {
	fn := n.Parent().Parent()
	if !fn.Is("function_declaration", "method_declaration", "func_literal") {
		return
	}
	typ := simpleType(lastNamed(n))
	if typ != "" && n.Is("variadic_parameter_declaration") {
		typ += "[]" // `cmds ...*Command` é um []*Command dentro da função
	}
	for _, id := range n.ChildrenOf("identifier") {
		x.declare(id.Text(), local{from: n.StartByte(), start: fn.StartByte(), end: fn.EndByte(), typ: typ})
	}
}

// declareAll declara `a, b := x, y` (ou `var a T = x`) no escopo que contém
// a declaração, valendo do fim dela em diante: `c := c.Root()` ainda lê o c
// de fora. O tipo vem do que está escrito ou do valor; sem tipo, fica a
// cadeia que define a local, que o resolvedor segue por retornos e campos.
func (x *extraction) declareAll(decl parser.Node, names []parser.Node, typ string, values []parser.Node) {
	scope := decl.Ancestor(scopes...)
	if scope.IsNil() {
		return // topo do arquivo: já é símbolo
	}
	for i, id := range names {
		l := local{from: decl.EndByte(), start: scope.StartByte(), end: scope.EndByte(), typ: typ}
		if l.typ == "" && i < len(values) {
			l.typ = x.valueType(values[i])
			if l.typ == "" && isChain(values[i]) {
				l.expr = values[i]
			}
		}
		x.declare(id.Text(), l)
	}
}

// rangeClause declara `for k, v := range xs` no escopo do for; quando xs tem
// tipo `T[]` conhecido v recebe T, senão v é um elemento da expressão.
func (x *extraction) rangeClause(n parser.Node) {
	left := n.Child("expression_list")
	if left.IsNil() || n.AnonChild(":=").IsNil() {
		return
	}
	loop, right := n.Parent(), lastNamed(n)
	for i, id := range left.ChildrenOf("identifier") {
		l := local{from: n.EndByte(), start: loop.StartByte(), end: loop.EndByte()}
		if i == 1 {
			if t, ok := strings.CutSuffix(x.valueType(right), "[]"); ok && t != "" {
				l.typ = t
			} else if isChain(right) {
				l.expr, l.elem = right, true // `range c.commands`
			}
		}
		x.declare(id.Text(), l)
	}
}

// typeSwitch declara o v de `switch v := x.(type)` em cada case: com um tipo
// só (`case *Pet:`) v tem esse tipo; com vários, v é local sem tipo.
func (x *extraction) typeSwitch(n parser.Node) {
	alias := n.Child("expression_list").NamedChildren()
	if len(alias) != 1 {
		return
	}
	for _, c := range n.ChildrenOf("type_case", "default_case") {
		types := c.NamedChildren()
		if len(types) > 0 && types[len(types)-1].Is("statement_list") {
			types = types[:len(types)-1]
		}
		l := local{from: c.StartByte(), start: c.StartByte(), end: c.EndByte()}
		if len(types) == 1 {
			l.typ = simpleType(types[0])
		}
		x.declare(alias[0].Text(), l)
	}
}

// maxSubstitution limita definições encadeadas (`a := b.X(); c := a.Y()`).
const maxSubstitution = 8

func isChain(expr parser.Node) bool {
	return expr.Is("call_expression", "selector_expression", "index_expression", "identifier")
}

func (x *extraction) declare(name string, l local) {
	if name != "_" {
		x.locals[name] = append(x.locals[name], l)
	}
}

// localAt devolve a declaração visível na posição: a do escopo mais interno
// que contém o uso e já terminou antes dele.
func (x *extraction) localAt(name string, at int) (local, bool) {
	var best local
	found := false
	for _, l := range x.locals[name] {
		if at < l.from || at < l.start || at >= l.end {
			continue
		}
		if !found || l.start > best.start || l.start == best.start && l.from > best.from {
			best, found = l, true
		}
	}
	return best, found
}

func (x *extraction) isLocal(n parser.Node) bool {
	_, ok := x.localAt(n.Text(), n.StartByte())
	return ok
}

// typeOf é o tipo do nome na posição do nó: o da declaração local visível
// ou, sem ela, o da variável de pacote deste arquivo.
func (x *extraction) typeOf(n parser.Node) (string, bool) {
	if l, ok := x.localAt(n.Text(), n.StartByte()); ok {
		return l.typ, true
	}
	return x.varTypes[n.Text()], false
}

// valueType é o tipo de uma expressão quando ele está escrito nela:
// `Pet{}`, `&Pet{}`, `pkg.T{}`, `x.(*Pet)`, literais; `f()` e `pkg.F()` viram
// `call:f` / `call:pkg.F`, que o resolvedor troca pelo tipo de retorno.
func (x *extraction) valueType(v parser.Node) string {
	switch v.Type() {
	case "composite_literal":
		return simpleType(v.Child("type_identifier", "qualified_type", "generic_type"))
	case "unary_expression":
		return x.valueType(lastNamed(v))
	case "type_assertion_expression":
		return simpleType(lastNamed(v))
	case "call_expression":
		fn := v.NamedChildren()[0]
		switch fn.Type() {
		case "identifier":
			if fn.Text() == "new" || fn.Text() == "make" {
				return simpleType(v.Child("argument_list").NamedChildren()[0]) // `new(T)`, `make([]T, n)`
			}
			if !builtins[fn.Text()] && !x.isLocal(fn) {
				return "call:" + fn.Text()
			}
		case "selector_expression":
			kids := fn.NamedChildren()
			if len(kids) == 2 && kids[0].Is("identifier") && x.imports[kids[0].Text()] && !x.isLocal(kids[0]) {
				return "call:" + kids[0].Text() + "." + kids[1].Text()
			}
		}
	case "interpreted_string_literal", "raw_string_literal":
		return "string"
	case "int_literal":
		return "int"
	case "float_literal":
		return "float64"
	case "true", "false":
		return "bool"
	case "nil":
		return "null"
	case "identifier":
		if t, local := x.typeOf(v); t != "" || local || builtins[v.Text()] {
			return t
		}
		return "var:" + v.Text() // variável de pacote: o resolvedor lê o tipo dela
	case "selector_expression":
		kids := v.NamedChildren()
		if len(kids) == 2 && kids[0].Is("identifier") && x.imports[kids[0].Text()] && !x.isLocal(kids[0]) {
			return "var:" + kids[0].Text() + "." + kids[1].Text() // `pricing.Zero`
		}
	}
	return ""
}

func (x *extraction) collectRefs(root parser.Node) {
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "call_expression":
			x.call(n)
		case "selector_expression":
			x.selector(n)
		case "composite_literal":
			x.composite(n)
		case "type_identifier":
			x.typeRef(n)
		case "qualified_type":
			if parent := n.Parent(); parent.Is("field_declaration") && len(parent.ChildrenOf("field_identifier")) == 0 || parent.Is("composite_literal", "type_elem") {
				return false // campo embutido (já é extends) ou literal composto (já é new)
			}
			x.ref(extract.RefType, n.Child("type_identifier").Text(), n, n.Child("package_identifier").Text(), PackageReceiver)
			return false
		case "identifier":
			x.identifierRef(n)
		}
		return true
	})
}

func (x *extraction) call(n parser.Node) {
	fn := n.NamedChildren()[0]
	switch fn.Type() {
	case "identifier":
		if builtins[fn.Text()] || x.isLocal(fn) {
			return
		}
		x.withArguments(x.ref(extract.RefCall, fn.Text(), fn, "", ""), n)
	case "selector_expression":
		kids := fn.NamedChildren()
		if len(kids) != 2 || !kids[1].Is("field_identifier") {
			return
		}
		receiver, receiverType, path := x.receiverChain(kids[0])
		if predeclaredTypes[receiverType] {
			return // `err.Error()`: método de tipo predeclarado, nunca aponta para o repositório
		}
		ref := x.ref(extract.RefMethod, kids[1].Text(), kids[1], receiver, receiverType)
		ref.ReceiverPath = path
		x.withArguments(ref, n)
	}
}

// predeclaredTypes são os tipos do universo Go: um receptor desses não tem
// membros no repositório.
var predeclaredTypes = map[string]bool{
	"error": true, "any": true, "string": true, "bool": true, "byte": true, "rune": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

// selector registra `a.b` fora de posição de chamada como acesso a campo.
func (x *extraction) selector(n parser.Node) {
	parent := n.Parent()
	if parent.Is("call_expression") && parent.NamedChildren()[0].StartByte() == n.StartByte() {
		return
	}
	kids := n.NamedChildren()
	if len(kids) != 2 || !kids[1].Is("field_identifier") {
		return
	}
	receiver, receiverType, path := x.receiverChain(kids[0])
	if receiver == "" && receiverType == "" && len(path) == 0 {
		return
	}
	x.ref(extract.RefProperty, kids[1].Text(), kids[1], receiver, receiverType).ReceiverPath = path
}

// receiverChain decompõe o operando: alias de pacote, variável com tipo,
// cadeia `a.b().c`, índice `xs[i]` (passo "get", que devolve o elemento).
func (x *extraction) receiverChain(object parser.Node) (string, string, []string) {
	switch object.Type() {
	case "identifier":
		name := object.Text()
		l, isLocal := x.localAt(name, object.StartByte())
		if !isLocal && x.imports[name] {
			return name, PackageReceiver, nil
		}
		if !l.expr.IsNil() && x.substDepth < maxSubstitution {
			x.substDepth++
			receiver, receiverType, path := x.receiverChain(l.expr)
			x.substDepth--
			if receiver != "" || receiverType != "" || len(path) > 0 {
				if l.elem {
					path = append(path, "get")
				}
				return receiver, receiverType, path
			}
		}
		if isLocal || builtins[name] {
			return name, l.typ, nil
		}
		if t := x.varTypes[name]; t != "" {
			return name, t, nil
		}
		return name, "var:" + name, nil // variável de pacote: o resolvedor lê o tipo dela
	case "selector_expression":
		kids := object.NamedChildren()
		if len(kids) != 2 {
			return "", "", nil
		}
		receiver, receiverType, path := x.receiverChain(kids[0])
		if receiver == "" && receiverType == "" && len(path) == 0 {
			return "", "", nil
		}
		return receiver, receiverType, append(path, kids[1].Text())
	case "call_expression":
		fn := object.NamedChildren()[0]
		switch fn.Type() {
		case "identifier":
			if builtins[fn.Text()] || x.isLocal(fn) {
				return "", "", nil
			}
			return "", "", []string{fn.Text()}
		case "selector_expression":
			kids := fn.NamedChildren()
			if len(kids) != 2 {
				return "", "", nil
			}
			receiver, receiverType, path := x.receiverChain(kids[0])
			if receiver == "" && receiverType == "" && len(path) == 0 {
				return "", "", nil
			}
			return receiver, receiverType, append(path, kids[1].Text())
		}
	case "index_expression":
		receiver, receiverType, path := x.receiverChain(object.NamedChildren()[0])
		if receiver == "" && receiverType == "" && len(path) == 0 {
			return "", "", nil
		}
		return receiver, receiverType, append(path, "get")
	case "composite_literal":
		// `(MsgPack{data}).Render(w)`: o receptor é um valor do tipo do literal.
		if t := simpleType(object.Child("type_identifier", "qualified_type", "generic_type")); t != "" {
			return t, t, nil
		}
	case "parenthesized_expression", "unary_expression", "type_assertion_expression":
		return x.receiverChain(object.NamedChildren()[0])
	}
	return "", "", nil
}

func (x *extraction) composite(n parser.Node) {
	typ := n.Child("type_identifier", "qualified_type", "generic_type")
	if typ.IsNil() {
		return
	}
	if typ.Is("qualified_type") {
		x.withArguments(x.ref(extract.RefNew, typ.Child("type_identifier").Text(), typ, typ.Child("package_identifier").Text(), PackageReceiver), n)
		return
	}
	name := simpleType(typ)
	if name == "" {
		return
	}
	x.withArguments(x.ref(extract.RefNew, name, typ, "", ""), n)
}

func (x *extraction) typeRef(n parser.Node) {
	parent := n.Parent()
	if builtins[n.Text()] || parent.Is("type_spec", "qualified_type", "composite_literal") {
		return
	}
	if parent.Is("field_declaration") && len(parent.ChildrenOf("field_identifier")) == 0 {
		return // campo embutido: já é extends
	}
	if parent.Is("type_elem") {
		return
	}
	x.ref(extract.RefType, n.Text(), n, "", "")
}

// identifierRef registra usos de nomes de topo (variáveis, funções passadas
// como valor); locais, builtins e posições de declaração ficam de fora.
func (x *extraction) identifierRef(n parser.Node) {
	name := n.Text()
	if builtins[name] || x.isLocal(n) || x.imports[name] || name == "_" {
		return
	}
	parent := n.Parent()
	if x.compositeKey(n) {
		return
	}
	switch parent.Type() {
	case "function_declaration", "method_declaration", "parameter_declaration", "variadic_parameter_declaration",
		"var_spec", "const_spec", "type_spec", "field_declaration", "keyed_element", "import_spec", "package_clause",
		"call_expression", "selector_expression", "labeled_statement", "goto_statement", "break_statement",
		"continue_statement", "range_clause", "func_literal", "type_parameter_declaration":
		return
	case "expression_list":
		// Nomes sendo declarados: `a := …`, `for k, v := range`, `switch v := x.(type)`, `case v := <-ch`.
		decl := parent.Parent()
		first := decl.NamedChildren()[0].StartByte() == parent.StartByte()
		if decl.Is("type_switch_statement") || first && decl.Is("short_var_declaration", "range_clause", "receive_statement") {
			return
		}
	}
	x.ref(extract.RefIdentifier, name, n, "", "")
}

// compositeKey trata `Cmd{Use: "x"}` e `[]Cmd{{Use: "y"}}`: a chave é um
// campo do tipo do literal, registrado como acesso a campo. Chaves de map
// (`map[K]V{KeyA: 1}`) são identificadores comuns e voltam false.
func (x *extraction) compositeKey(n parser.Node) bool {
	element := n.Parent()
	keyed := element.Parent()
	if !element.Is("literal_element") || !keyed.Is("keyed_element") || keyed.NamedChildren()[0].StartByte() != element.StartByte() {
		return false
	}
	literal := n.Ancestor("composite_literal")
	if literal.IsNil() {
		return false
	}
	typ := literal.Child("type_identifier", "qualified_type", "generic_type", "slice_type", "array_type", "map_type", "struct_type")
	if typ.Is("map_type") {
		return false
	}
	name := strings.TrimSuffix(simpleType(typ), "[]")
	if name == "" {
		return true // struct anônima: sem tipo para resolver a chave
	}
	x.ref(extract.RefProperty, n.Text(), n, "", name) // `cobra.Command{Use: …}`: o resolvedor acha o tipo pelo import
	return true
}

func (x *extraction) withArguments(ref *extract.Ref, call parser.Node) {
	args := call.Child("argument_list", "literal_value")
	if args.IsNil() {
		return
	}
	ref.Arity = 0
	for _, a := range args.NamedChildren() {
		if a.Is("keyed_element", "literal_element") {
			ref.Arity = -1 // literal composto: não são argumentos posicionais
			ref.ArgTypes = nil
			return
		}
		ref.Arity++
		ref.ArgTypes = append(ref.ArgTypes, extract.ArgType(x.valueType(a)))
	}
}

func (x *extraction) ref(kind, name string, at parser.Node, receiver, receiverType string) *extract.Ref {
	x.result.Refs = append(x.result.Refs, extract.Ref{
		Name: name, Kind: kind, Line: at.StartLine(), Col: at.StartCol(),
		Receiver: receiver, ReceiverType: receiverType,
		Container: extract.ContainerOf(x.result.Symbols, at.StartByte()), Arity: -1,
	})
	return &x.result.Refs[len(x.result.Refs)-1]
}
