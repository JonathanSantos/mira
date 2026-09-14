package python

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// builtins são nomes do runtime que nunca resolvem para o repositório.
var builtins = map[string]bool{
	"print": true, "len": true, "range": true, "str": true, "int": true, "float": true, "bool": true,
	"list": true, "dict": true, "set": true, "tuple": true, "isinstance": true, "issubclass": true,
	"super": true, "object": true, "type": true, "enumerate": true, "zip": true, "map": true,
	"filter": true, "sorted": true, "reversed": true, "any": true, "all": true, "sum": true, "min": true,
	"max": true, "abs": true, "round": true, "open": true, "iter": true, "next": true, "getattr": true,
	"setattr": true, "hasattr": true, "id": true, "hash": true, "repr": true, "format": true, "input": true,
	"Exception": true, "ValueError": true, "TypeError": true, "KeyError": true, "IndexError": true,
	"RuntimeError": true, "NotImplementedError": true, "AttributeError": true, "StopIteration": true,
	"None": true, "True": true, "False": true, "self": true, "cls": true, "bytes": true, "frozenset": true,
	"property": true, "staticmethod": true, "classmethod": true, "dataclass": true, "vars": true, "callable": true,
}

// collectDeclarations preenche locals e varTypes: parâmetros (anotados ou
// não), variáveis de função, alvos de for/with/except, e atributos
// `self.x: T` / `self.x = T()`.
func (x *extraction) collectDeclarations(root parser.Node) {
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "parameters", "lambda_parameters":
			for _, p := range n.NamedChildren() {
				x.parameter(p)
			}
		case "assignment", "augmented_assignment":
			x.declareAssignment(n)
		case "for_statement", "for_in_clause":
			x.declareTargets(n.NamedChildren()[0])
		case "as_pattern":
			if target := n.Child("as_pattern_target"); !target.IsNil() {
				x.declareTargets(target.NamedChildren()[0])
			}
		}
		return true
	})
}

func (x *extraction) parameter(p parser.Node) {
	switch p.Type() {
	case "identifier":
		x.locals[p.Text()] = true
	case "typed_parameter", "typed_default_parameter", "default_parameter":
		id := p.Child("identifier")
		if splat := p.Child("list_splat_pattern", "dictionary_splat_pattern"); id.IsNil() && !splat.IsNil() {
			id = splat.Child("identifier") // `**kwargs: t.Any`
		}
		if id.IsNil() {
			return
		}
		x.locals[id.Text()] = true
		if t := typeText(p.Child("type")); t != "" {
			x.varTypes[id.Text()] = t
		} else if t := x.valueType(lastNamed(p)); p.Is("default_parameter") && t != "" {
			x.varTypes[id.Text()] = t // `count=1`: o default diz o tipo
		}
	case "list_splat_pattern", "dictionary_splat_pattern":
		if id := p.Child("identifier"); !id.IsNil() {
			x.locals[id.Text()] = true
		}
	}
}

func (x *extraction) declareTargets(target parser.Node) {
	switch target.Type() {
	case "identifier":
		x.locals[target.Text()] = true
	case "pattern_list", "tuple_pattern", "list_pattern", "expression_list", "tuple":
		for _, c := range target.NamedChildren() {
			x.declareTargets(c)
		}
	}
}

// declareAssignment: dentro de função, o alvo é local (com tipo quando é
// `x: T = …` ou `x = T(…)`); `self.x` recebe o tipo como atributo da
// classe que envolve.
func (x *extraction) declareAssignment(a parser.Node) {
	kids := a.NamedChildren()
	if len(kids) == 0 {
		return
	}
	target := kids[0]
	typ := typeText(a.Child("type"))
	if typ == "" {
		typ = x.valueType(lastNamed(a))
	}
	inFunction := !a.Ancestor("function_definition").IsNil()
	switch target.Type() {
	case "identifier":
		if inFunction {
			x.locals[target.Text()] = true
		}
		if typ != "" {
			x.varTypes[target.Text()] = typ
		}
	case "attribute":
		obj := target.NamedChildren()[0]
		if obj.Is("identifier") && (obj.Text() == "self" || obj.Text() == "cls") {
			if class := x.enclosingClass(a); class != "" {
				x.instanceAttribute(a, class, lastNamed(target), typ)
			}
		}
	case "pattern_list", "tuple_pattern":
		if inFunction {
			x.declareTargets(target)
		}
	}
}

// valueType é o tipo de uma expressão quando ele está escrito nela:
// `Pet()`, `mod.Pet()`, literais.
func (x *extraction) valueType(v parser.Node) string {
	switch v.Type() {
	case "call":
		fn := v.NamedChildren()[0]
		switch {
		case fn.Is("identifier") && isUpper(fn.Text()) && !builtins[fn.Text()]:
			return fn.Text()
		case fn.Is("identifier") && !builtins[fn.Text()] && !x.locals[fn.Text()]:
			return "call:" + fn.Text()
		case fn.Is("attribute") && isUpper(lastNamed(fn).Text()):
			return extract.Collapse(fn.Text())
		case fn.Is("attribute") && fn.NamedChildren()[0].Is("identifier") && (x.modules[fn.NamedChildren()[0].Text()] || x.imported[fn.NamedChildren()[0].Text()]):
			return "call:" + extract.Collapse(fn.Text())
		}
	case "string", "concatenated_string":
		return "str"
	case "integer":
		return "int"
	case "float":
		return "float"
	case "true", "false":
		return "bool"
	case "none":
		return "null"
	case "list", "list_comprehension":
		return "list"
	case "dictionary", "dictionary_comprehension":
		return "dict"
	case "identifier":
		if t := x.varTypes[v.Text()]; t != "" {
			return t
		}
		if !x.locals[v.Text()] && !builtins[v.Text()] && !x.modules[v.Text()] {
			return "var:" + v.Text() // variável de módulo ou importada: o resolvedor lê o tipo
		}
	case "attribute":
		kids := v.NamedChildren()
		if len(kids) == 2 && kids[0].Is("identifier") && (x.modules[kids[0].Text()] || x.imported[kids[0].Text()]) {
			return "var:" + kids[0].Text() + "." + kids[1].Text() // `money_mod.ZERO`
		}
	case "parenthesized_expression", "await":
		return x.valueType(lastNamed(v))
	}
	return ""
}

// instanceAttribute registra `self.x = …` como campo da classe (uma vez
// por nome), com o tipo quando anotado ou construído: é onde a maioria dos
// atributos Python nasce.
func (x *extraction) instanceAttribute(a parser.Node, class string, attr parser.Node, typ string) {
	if x.fieldTypes[class] == nil {
		x.fieldTypes[class] = map[string]string{}
	}
	if typ != "" {
		x.fieldTypes[class][attr.Text()] = typ
	}
	qualified := class + "." + attr.Text()
	for _, s := range x.result.Symbols {
		if s.QualifiedName == qualified {
			return
		}
	}
	sig := attr.Text()
	if typ != "" {
		sig += ": " + typ
	}
	x.result.Symbols = append(x.result.Symbols, extract.Symbol{
		Name: attr.Text(), QualifiedName: qualified, Kind: extract.KindField, Container: class, Signature: sig,
		Exported: !strings.HasPrefix(attr.Text(), "_"), ExportName: attr.Text(),
		StartLine: a.StartLine(), EndLine: a.EndLine(), StartByte: a.StartByte(), EndByte: a.EndByte(), NameLine: attr.StartLine(),
		ReturnHint: typ,
	})
}

func (x *extraction) enclosingClass(n parser.Node) string {
	class := n.Ancestor("class_definition")
	if class.IsNil() {
		return ""
	}
	return class.Child("identifier").Text()
}

func (x *extraction) collectRefs(root parser.Node) {
	// O próprio nome importado é uma ref, para `refs Pet` mostrar a linha do import.
	for _, im := range x.result.Imports {
		if im.ImportedName != "" {
			x.result.Refs = append(x.result.Refs, extract.Ref{Name: im.ImportedName, Kind: extract.RefImport, Line: im.Line, Col: 1, Container: -1, Arity: -1})
		}
	}
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "call":
			x.call(n)
		case "attribute":
			x.attribute(n)
		case "decorator":
			x.decorator(n)
			return false
		case "class_definition":
			x.bases(n)
		case "type":
			x.typeRef(n)
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
		if builtins[fn.Text()] || x.locals[fn.Text()] {
			return
		}
		x.withArguments(x.ref(extract.RefCall, fn.Text(), fn, "", ""), n)
	case "attribute":
		kids := fn.NamedChildren()
		if len(kids) != 2 {
			return
		}
		receiver, receiverType, path := x.receiverChain(kids[0], n)
		ref := x.ref(extract.RefMethod, kids[1].Text(), kids[1], receiver, receiverType)
		ref.ReceiverPath = path
		x.withArguments(ref, n)
	}
}

// attribute registra `a.b` fora de posição de chamada como acesso a
// atributo.
func (x *extraction) attribute(n parser.Node) {
	parent := n.Parent()
	if parent.Is("call") && parent.NamedChildren()[0].StartByte() == n.StartByte() {
		return
	}
	if parent.Is("decorator", "import_from_statement", "dotted_name", "type") {
		return
	}
	kids := n.NamedChildren()
	if len(kids) != 2 {
		return
	}
	receiver, receiverType, path := x.receiverChain(kids[0], n)
	if receiver == "" && receiverType == "" && len(path) == 0 {
		return
	}
	x.ref(extract.RefProperty, kids[1].Text(), kids[1], receiver, receiverType).ReceiverPath = path
}

// receiverChain decompõe o objeto: self/cls (a classe que envolve), módulo
// importado, variável com tipo, cadeia `a.b().c`, índice `xs[i]` (passo
// "get", que devolve o elemento).
func (x *extraction) receiverChain(object, at parser.Node) (string, string, []string) {
	switch object.Type() {
	case "identifier":
		name := object.Text()
		switch {
		case name == "self" || name == "cls":
			return "this", x.enclosingClass(at), nil
		case x.modules[name] || x.imported[name]:
			// Módulo importado ou nome vindo de `from a import b`: o resolvedor
			// decide se é um submódulo ou um símbolo (classe com métodos estáticos).
			return name, ModuleReceiver, nil
		}
		return name, x.varTypes[name], nil
	case "attribute":
		kids := object.NamedChildren()
		if len(kids) != 2 {
			return "", "", nil
		}
		if kids[0].Is("identifier") && (kids[0].Text() == "self" || kids[0].Text() == "cls") {
			// `self.repo.save()`: o tipo do atributo, se conhecido.
			class := x.enclosingClass(at)
			field := kids[1].Text()
			return field, x.fieldTypes[class][field], nil
		}
		receiver, receiverType, path := x.receiverChain(kids[0], at)
		if receiver == "" && receiverType == "" && len(path) == 0 {
			return "", "", nil
		}
		return receiver, receiverType, append(path, kids[1].Text())
	case "call":
		fn := object.NamedChildren()[0]
		switch fn.Type() {
		case "identifier":
			if builtins[fn.Text()] || x.locals[fn.Text()] {
				return "", "", nil
			}
			return "", "", []string{fn.Text()}
		case "attribute":
			kids := fn.NamedChildren()
			if len(kids) != 2 {
				return "", "", nil
			}
			receiver, receiverType, path := x.receiverChain(kids[0], at)
			if receiver == "" && receiverType == "" && len(path) == 0 {
				return "", "", nil
			}
			return receiver, receiverType, append(path, kids[1].Text())
		}
	case "subscript":
		receiver, receiverType, path := x.receiverChain(object.NamedChildren()[0], at)
		if receiver == "" && receiverType == "" && len(path) == 0 {
			return "", "", nil
		}
		return receiver, receiverType, append(path, "get")
	case "parenthesized_expression", "await":
		return x.receiverChain(lastNamed(object), at)
	}
	return "", "", nil
}

func (x *extraction) decorator(n parser.Node) {
	target := n.NamedChildren()[0]
	if target.Is("call") {
		target = target.NamedChildren()[0]
	}
	switch target.Type() {
	case "identifier":
		if !builtins[target.Text()] {
			x.ref(extract.RefAnnotation, target.Text(), target, "", "")
		}
	case "attribute":
		x.ref(extract.RefAnnotation, lastNamed(target).Text(), target, target.NamedChildren()[0].Text(), ModuleReceiver)
	}
}

// bases registra as superclasses como `extends`.
func (x *extraction) bases(class parser.Node) {
	args := class.Child("argument_list")
	if args.IsNil() {
		return
	}
	idx := -1
	name := class.Child("identifier").Text()
	for i, s := range x.result.Symbols {
		if s.Name == name && s.Kind == extract.KindClass && s.StartByte <= class.StartByte() && class.EndByte() <= s.EndByte {
			idx = i
		}
	}
	for _, base := range args.NamedChildren() {
		switch base.Type() {
		case "identifier":
			if builtins[base.Text()] {
				continue
			}
			x.ref(extract.RefExtends, base.Text(), base, "", "").Container = idx
		case "attribute":
			x.ref(extract.RefExtends, lastNamed(base).Text(), base, base.NamedChildren()[0].Text(), ModuleReceiver).Container = idx
		case "keyword_argument":
			// metaclass=… não é herança
		}
	}
}

func (x *extraction) typeRef(t parser.Node) {
	t.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "identifier":
			if !builtins[n.Text()] && !n.Parent().Is("attribute") {
				x.ref(extract.RefType, n.Text(), n, "", "")
			}
		case "attribute":
			x.ref(extract.RefType, lastNamed(n).Text(), n, n.NamedChildren()[0].Text(), ModuleReceiver)
			return false
		}
		return true
	})
}

// identifierRef registra usos de nomes de módulo ou importados fora de
// posições de declaração; locais e builtins ficam de fora.
func (x *extraction) identifierRef(n parser.Node) {
	name := n.Text()
	if builtins[name] || x.locals[name] || x.modules[name] {
		return
	}
	parent := n.Parent()
	switch parent.Type() {
	case "function_definition", "class_definition", "parameters", "typed_parameter", "default_parameter",
		"typed_default_parameter", "keyword_argument", "call", "attribute", "import_statement", "import_from_statement",
		"aliased_import", "dotted_name", "decorator", "list_splat_pattern", "dictionary_splat_pattern",
		"global_statement", "nonlocal_statement", "lambda_parameters", "as_pattern_target", "for_statement",
		"for_in_clause", "pattern_list", "tuple_pattern", "keyword_identifier":
		if parent.Is("call") && parent.NamedChildren()[0].StartByte() != n.StartByte() {
			break // argumento de chamada: é uso
		}
		if parent.Is("for_statement", "for_in_clause") && parent.NamedChildren()[0].StartByte() != n.StartByte() {
			break // o iterável, não o alvo
		}
		if parent.Is("keyword_argument") && parent.NamedChildren()[0].StartByte() != n.StartByte() {
			break // o valor, não o nome
		}
		return
	case "assignment", "augmented_assignment":
		if parent.NamedChildren()[0].StartByte() == n.StartByte() {
			return // alvo
		}
	}
	x.ref(extract.RefIdentifier, name, n, "", "")
}

func (x *extraction) withArguments(ref *extract.Ref, call parser.Node) {
	args := call.Child("argument_list")
	if args.IsNil() {
		return
	}
	ref.Arity = 0
	for _, a := range args.NamedChildren() {
		if a.Is("keyword_argument", "list_splat", "dictionary_splat") {
			ref.Arity = -1
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

func isUpper(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}
