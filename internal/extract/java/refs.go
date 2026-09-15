package java

import (
	"strings"
	"unicode"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

var typeNodeTypes = []string{
	"type_identifier", "generic_type", "scoped_type_identifier", "array_type",
	"integral_type", "floating_point_type", "boolean_type", "void_type",
}

// collectRefs registra invocações, `new`, tipos, anotações, extends e
// implements.
func (x *extraction) collectRefs(root parser.Node) {
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "method_invocation":
			x.invocation(n)
		case "object_creation_expression":
			x.creation(n)
		case "method_reference":
			x.methodReference(n)
		case "type_identifier":
			x.typeRef(n)
		case "field_access":
			x.fieldAccess(n)
		case "identifier":
			x.bareFieldRef(n)
		case "annotation", "marker_annotation":
			if name := n.Child("identifier", "scoped_identifier"); !name.IsNil() {
				x.ref(extract.RefAnnotation, lastSegment(name.Text()), name, "", "")
			}
		}
		return true
	})
}

// fieldAccess registra `obj.x` / `this.x` como acesso a campo, também quando
// é o objeto de uma chamada: em `this.owners.save()`, invocation registra só
// o método.
func (x *extraction) fieldAccess(n parser.Node) {
	kids := n.NamedChildren()
	if len(kids) < 2 || !kids[len(kids)-1].Is("identifier") {
		return
	}
	object, field := kids[0], kids[len(kids)-1]
	receiver, receiverType, path := x.receiverChain(object, n)
	x.ref(extract.RefProperty, field.Text(), field, receiver, receiverType).ReceiverPath = path
}

// receiverChain decompõe o objeto de uma chamada ou acesso em (receiver,
// tipo, caminho de membros): `owner.getPet(id).addVisit()` vira ("owner",
// "Owner", [getPet]); `getPet(id).x` vira ("this", tipo, [getPet]).
func (x *extraction) receiverChain(object, at parser.Node) (string, string, []string) {
	switch object.Type() {
	case "field_access":
		kids := object.NamedChildren()
		if len(kids) == 2 && !kids[0].Is("this") && kids[1].Is("identifier") {
			receiver, receiverType, path := x.receiverChain(kids[0], at)
			if receiver == "" && receiverType == "" {
				return "", "", nil
			}
			return receiver, receiverType, append(path, kids[1].Text())
		}
	case "method_invocation":
		ids := object.ChildrenOf("identifier")
		if len(ids) == 0 {
			return "", "", nil
		}
		name := ids[len(ids)-1]
		inner := object.NamedChildren()[0]
		if inner.StartByte() == name.StartByte() {
			return "this", x.enclosingType(at), []string{name.Text()}
		}
		receiver, receiverType, path := x.receiverChain(inner, at)
		if receiver == "" && receiverType == "" {
			return "", "", nil
		}
		return receiver, receiverType, append(path, name.Text())
	case "parenthesized_expression":
		return x.receiverChain(lastNamed(object), at)
	}
	receiver, receiverType := x.receiverOf(object, at)
	return receiver, receiverType, nil
}

// bareFieldRef registra um identificador solto que é campo do tipo que o
// envolve (`telephone` dentro de um método de Owner) como `this.telephone`.
func (x *extraction) bareFieldRef(n parser.Node) {
	parent := n.Parent()
	switch parent.Type() {
	case "field_access", "method_invocation", "method_reference":
		// Só o objeto pode ser campo (`owners.findById()`, `owners.size`,
		// `owners::findById`); o outro identifier é o membro.
		if parent.NamedChildren()[0].StartByte() != n.StartByte() {
			return
		}
		if !parent.Is("field_access") && len(parent.ChildrenOf("identifier")) < 2 {
			return // `findById(x)`: o identifier é o próprio método
		}
	case "scoped_identifier", "variable_declarator", "formal_parameter",
		"class_declaration", "interface_declaration", "enum_declaration", "record_declaration",
		"annotation_type_declaration", "method_declaration", "constructor_declaration", "enum_constant",
		"import_declaration", "package_declaration", "annotation", "marker_annotation", "element_value_pair",
		"lambda_expression", "inferred_parameters", "catch_formal_parameter", "labeled_statement",
		"break_statement", "continue_statement", "spread_parameter":
		return
	}
	name := n.Text()
	if x.declaredLocal(name, n) {
		return
	}
	owner, _, isField := x.fieldOf(name, n)
	if !isField {
		return
	}
	x.ref(extract.RefProperty, name, n, "this", owner)
}

// fieldOf acha o campo no tipo que envolve a posição ou, dentro de uma classe
// interna, nos tipos de fora: devolve o tipo dono e o tipo do campo.
func (x *extraction) fieldOf(name string, at parser.Node) (owner, fieldType string, ok bool) {
	for decl := at.Ancestor(typeDeclarations...); !decl.IsNil(); decl = decl.Ancestor(typeDeclarations...) {
		id := decl.Child("identifier")
		if id.IsNil() {
			continue
		}
		if t, found := x.fieldTypes[id.Text()][name]; found {
			return id.Text(), t, true
		}
	}
	return "", "", false
}

// declaredLocal diz se o nome é parâmetro ou variável local no escopo da
// posição, caso em que ele esconde um campo homônimo.
func (x *extraction) declaredLocal(name string, at parser.Node) bool {
	for scope := at.Ancestor(scopeTypes...); !scope.IsNil(); scope = scope.Ancestor(scopeTypes...) {
		if _, ok := x.localTypesOf(scope)[name]; ok {
			return true
		}
	}
	return false
}

// invocation trata `m()`, `obj.m()`, `this.f.m()`, `Type.m()` e `a.b().m()`.
func (x *extraction) invocation(n parser.Node) {
	ids := n.ChildrenOf("identifier")
	if len(ids) == 0 {
		return
	}
	// O nome do método é o último identifier direto; o primeiro, se houver
	// outro, é o objeto.
	name := ids[len(ids)-1]
	if args := n.Child("argument_list"); !args.IsNil() && name.StartByte() > args.StartByte() {
		return
	}
	receiver, receiverType := "", ""
	var path []string
	object := n.NamedChildren()[0]
	if object.StartByte() != name.StartByte() {
		receiver, receiverType, path = x.receiverChain(object, n)
	}
	ref := x.ref(extract.RefMethod, name.Text(), name, receiver, receiverType)
	ref.ReceiverPath = path
	x.withArguments(ref, n)
}

// receiverOf devolve (receiver, tipo declarado) do objeto de uma chamada.
// Um identifier capitalizado sem declaração é tratado como nome de tipo
// (chamada estática).
func (x *extraction) receiverOf(object, at parser.Node) (string, string) {
	switch object.Type() {
	case "identifier":
		name := object.Text()
		if t := x.declaredType(name, at); t != "" {
			return name, t
		}
		if isUpper(name) {
			return name, name
		}
		return name, ""
	case "this":
		return "this", x.enclosingType(at)
	case "field_access":
		kids := object.NamedChildren()
		if len(kids) == 2 && kids[0].Is("this") {
			field := kids[1].Text()
			return field, x.fieldTypes[x.enclosingType(at)][field]
		}
	case "object_creation_expression":
		// `new T().m()`: o tipo do receiver é o próprio T.
		typ := simpleTypeName(object.Child("type_identifier", "scoped_type_identifier", "generic_type"))
		return "new", typ
	}
	return "", ""
}

func (x *extraction) creation(n parser.Node) {
	typ := n.Child("type_identifier", "scoped_type_identifier", "generic_type")
	if typ.IsNil() {
		return
	}
	x.withArguments(x.ref(extract.RefNew, simpleTypeName(typ), typ, qualifierOf(typ), ""), n)
}

// qualifierOf devolve o prefixo de um tipo qualificado (`java.util` em
// `java.util.Map<K, V>`), que o resolvedor usa para marcar external.
func qualifierOf(typ parser.Node) string {
	if typ.Is("generic_type") {
		typ = typ.Child("scoped_type_identifier", "type_identifier")
	}
	if !typ.Is("scoped_type_identifier") {
		return ""
	}
	text := typ.Text()
	if i := strings.LastIndex(text, "."); i >= 0 {
		return text[:i]
	}
	return ""
}

func (x *extraction) methodReference(n parser.Node) {
	ids := n.ChildrenOf("identifier")
	if len(ids) != 2 {
		return
	}
	// `Visit::getDate` usa a classe Visit, além do método.
	if q := ids[0].Text(); isUpper(q) && !x.declaredLocal(q, n) {
		if _, _, isField := x.fieldOf(q, n); !isField {
			x.ref(extract.RefType, q, ids[0], "", "")
		}
	}
	receiver, receiverType := x.receiverOf(ids[0], n)
	x.ref(extract.RefMethod, ids[1].Text(), ids[1], receiver, receiverType)
}

func (x *extraction) typeRef(n parser.Node) {
	if n.Text() == "var" || isCreationType(n) {
		return
	}
	parent := n.Parent()
	switch parent.Type() {
	case "superclass":
		x.ref(extract.RefExtends, n.Text(), n, "", "")
		return
	case "type_list":
		kind := extract.RefImplements
		if parent.Parent().Is("extends_interfaces") {
			kind = extract.RefExtends
		}
		x.ref(kind, n.Text(), n, "", "")
		return
	case "scoped_type_identifier":
		// `Outer.Inner` ou `java.util.Map`: só o último segmento do
		// qualificador mais externo é o tipo referenciado.
		top := parent
		for top.Parent().Is("scoped_type_identifier") {
			top = top.Parent()
		}
		if lastNamed(top).StartByte() != n.StartByte() {
			return
		}
		x.ref(extract.RefType, n.Text(), n, qualifierOf(top), "")
		return
	case "generic_type":
		if parent.Parent().Is("superclass") {
			x.ref(extract.RefExtends, n.Text(), n, "", "")
			return
		}
		if parent.Parent().Is("type_list") {
			x.ref(extract.RefImplements, n.Text(), n, "", "")
			return
		}
	}
	x.ref(extract.RefType, n.Text(), n, "", "")
}

// isCreationType diz se o nó é o tipo instanciado em `new T()`, já
// registrado como RefNew por creation(). Sobe por scoped/generic_type.
func isCreationType(n parser.Node) bool {
	owner := n
	for p := owner.Parent(); p.Is("scoped_type_identifier", "generic_type"); p = p.Parent() {
		if p.Is("generic_type") && p.NamedChildren()[0].StartByte() != owner.StartByte() {
			return false // argumento de tipo, não o tipo criado
		}
		owner = p
	}
	return owner.Parent().Is("object_creation_expression")
}

// declaredType procura o tipo de uma variável: parâmetros e locais do
// método que envolve `at`, depois campos da classe.
func (x *extraction) declaredType(name string, at parser.Node) string {
	// Do escopo mais interno (lambda) para o mais externo (método).
	for scope := at.Ancestor(scopeTypes...); !scope.IsNil(); scope = scope.Ancestor(scopeTypes...) {
		if t, ok := x.localTypesOf(scope)[name]; ok {
			return t
		}
	}
	_, t, _ := x.fieldOf(name, at)
	return t
}

var scopeTypes = []string{"method_declaration", "constructor_declaration", "lambda_expression"}

func (x *extraction) localTypesOf(method parser.Node) map[string]string {
	if cached, ok := x.localTypes[method.StartByte()]; ok {
		return cached
	}
	types := map[string]string{}
	method.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "formal_parameter", "spread_parameter", "catch_formal_parameter":
			if id := n.Child("identifier"); !id.IsNil() {
				types[id.Text()] = simpleTypeName(n.Child(typeNodeTypes...))
			}
		case "local_variable_declaration", "enhanced_for_statement":
			typ := simpleTypeName(n.Child(typeNodeTypes...))
			for _, d := range n.ChildrenOf("variable_declarator") {
				if id := d.Child("identifier"); !id.IsNil() {
					types[id.Text()] = x.inferLocal(typ, d)
				}
			}
			if id := n.Child("identifier"); !id.IsNil() && n.Is("enhanced_for_statement") {
				types[id.Text()] = typ
			}
		}
		return true
	})
	x.localTypes[method.StartByte()] = types
	return types
}

// inferLocal cobre `var x = new T()`: sem tipo declarado, usa o do `new`.
func (x *extraction) inferLocal(declared string, declarator parser.Node) string {
	if declared != "" && declared != "var" {
		return declared
	}
	if value := lastNamed(declarator); value.Is("object_creation_expression") {
		return simpleTypeName(value.Child("type_identifier", "scoped_type_identifier", "generic_type"))
	}
	return ""
}

func (x *extraction) enclosingType(n parser.Node) string {
	decl := n.Ancestor(typeDeclarations...)
	if decl.IsNil() {
		return ""
	}
	name := decl.Child("identifier")
	if name.IsNil() {
		return ""
	}
	return name.Text()
}

// simpleTypeName reduz um nó de tipo ao identificador resolvível:
// `List<Item>` -> List, `Outer.Inner` -> Inner, `Money[]` -> Money.
func simpleTypeName(n parser.Node) string {
	if n.IsNil() {
		return ""
	}
	switch n.Type() {
	case "type_identifier":
		return n.Text()
	case "generic_type", "array_type":
		return simpleTypeName(n.Child("type_identifier", "scoped_type_identifier", "generic_type"))
	case "scoped_type_identifier":
		return lastNamed(n).Text()
	case "integral_type", "floating_point_type", "boolean_type":
		return n.Text() // `int`, `double`, `boolean`: casam com sobrecargas por boxing
	}
	return ""
}

func lastNamed(n parser.Node) parser.Node {
	kids := n.NamedChildren()
	if len(kids) == 0 {
		return parser.Node{}
	}
	return kids[len(kids)-1]
}

func lastSegment(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[i+1:]
		}
	}
	return name
}

func isUpper(name string) bool {
	return name != "" && unicode.IsUpper(rune(name[0]))
}

// ref registra o uso e devolve o ponteiro para o chamador completar (arity).
func (x *extraction) ref(kind, name string, at parser.Node, receiver, receiverType string) *extract.Ref {
	x.result.Refs = append(x.result.Refs, extract.Ref{
		Name:         name,
		Kind:         kind,
		Line:         at.StartLine(),
		Col:          at.StartCol(),
		Receiver:     receiver,
		ReceiverType: receiverType,
		Container:    extract.ContainerOf(x.result.Symbols, at.StartByte()),
		Arity:        -1,
	})
	return &x.result.Refs[len(x.result.Refs)-1]
}

// withArguments preenche aridade e tipos dos argumentos de uma invocação
// ou `new`.
func (x *extraction) withArguments(ref *extract.Ref, call parser.Node) {
	args := call.Child("argument_list")
	if args.IsNil() {
		return
	}
	ref.Arity = 0
	for _, a := range args.NamedChildren() {
		ref.Arity++
		ref.ArgTypes = append(ref.ArgTypes, x.argType(a))
	}
}

// argType é o tipo de um argumento quando dá para saber: literal, variável
// ou campo com tipo declarado, `new T()`, cast, `this`; "" quando não.
func (x *extraction) argType(n parser.Node) string {
	switch n.Type() {
	case "identifier":
		return x.declaredType(n.Text(), n)
	case "string_literal":
		return "String"
	case "decimal_integer_literal", "hex_integer_literal", "octal_integer_literal", "binary_integer_literal":
		if strings.HasSuffix(strings.ToLower(n.Text()), "l") {
			return "long"
		}
		return "int"
	case "decimal_floating_point_literal", "hex_floating_point_literal":
		if strings.HasSuffix(strings.ToLower(n.Text()), "f") {
			return "float"
		}
		return "double"
	case "true", "false":
		return "boolean"
	case "null_literal":
		return "null"
	case "character_literal":
		return "char"
	case "this":
		return x.enclosingType(n)
	case "object_creation_expression":
		return simpleTypeName(n.Child("type_identifier", "scoped_type_identifier", "generic_type"))
	case "cast_expression":
		return simpleTypeName(n.Child(typeNodeTypes...))
	case "parenthesized_expression":
		return x.argType(lastNamed(n))
	}
	return ""
}
