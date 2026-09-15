package typescript

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// builtins são globais do runtime que nunca resolvem para um símbolo do
// repositório; ficam fora das refs para não virar ruído "unresolved".
var builtins = map[string]bool{
	"require": true, "module": true, "exports": true, "console": true,
	"window": true, "document": true, "process": true, "globalThis": true,
	"Math": true, "JSON": true, "Object": true, "Array": true, "Number": true,
	"String": true, "Boolean": true, "Promise": true, "Date": true, "Error": true,
	"Map": true, "Set": true, "Symbol": true, "parseInt": true, "parseFloat": true,
	"WeakMap": true, "WeakSet": true, "RegExp": true, "Proxy": true, "Reflect": true,
	"TypeError": true, "RangeError": true, "setTimeout": true, "clearTimeout": true,
	"setInterval": true, "clearInterval": true, "queueMicrotask": true, "structuredClone": true,
	"fetch": true, "isNaN": true, "Buffer": true,
}

// collectRefs percorre a árvore inteira registrando usos de nomes.
func (x *extraction) collectRefs(root parser.Node) {
	x.importRefs()
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "call_expression":
			x.callRef(n)
		case "member_expression":
			x.propertyRef(n)
		case "new_expression":
			if ctor := n.Child("identifier"); !ctor.IsNil() && !builtins[ctor.Text()] {
				x.withArguments(x.ref(extract.RefNew, ctor.Text(), ctor, "", ""), n)
			}
		case "type_identifier":
			x.typeRef(n)
		case "identifier":
			x.identifierRef(n)
		case "shorthand_property_identifier":
			// `return { register, handleSubmit }`: o objeto retornado expõe a
			// closure; sem esta ref, quem só expõe nunca aparece no grafo.
			x.shorthandRef(n)
		case "jsx_opening_element", "jsx_self_closing_element":
			if name := n.Child("identifier"); !name.IsNil() && isUpper(name.Text()) {
				x.ref(extract.RefJSX, name.Text(), name, "", "")
			}
		case "decorator":
			x.decoratorRef(n)
		case "extends_clause":
			for _, c := range n.ChildrenOf("identifier") {
				x.ref(extract.RefExtends, c.Text(), c, "", "")
			}
		}
		return true
	})
}

// importRefs registra o nome importado como referência na linha do import,
// para que `refs X` mostre quem importa X.
func (x *extraction) importRefs() {
	for _, im := range x.result.Imports {
		if im.IsReexport || im.ImportedName == "" || im.ImportedName == "*" {
			continue
		}
		name := im.ImportedName
		if name == "default" {
			name = im.LocalName
		}
		x.result.Refs = append(x.result.Refs, extract.Ref{
			Name: name, Kind: extract.RefImport, Line: im.Line, Col: 1, Container: -1,
		})
	}
}

func (x *extraction) callRef(n parser.Node) {
	fn := n.NamedChildren()[0]
	switch fn.Type() {
	case "identifier":
		if builtins[fn.Text()] || x.isLocal(fn) {
			return
		}
		kind := extract.RefCall
		if n.Parent().Is("decorator") {
			kind = extract.RefAnnotation
		}
		x.withArguments(x.ref(kind, fn.Text(), fn, "", ""), n)
	case "member_expression":
		x.memberCall(fn)
	}
}

// propertyRef registra `obj.x` / `this.x` fora de posição de chamada como
// acesso a propriedade, para que `refs x` cubra campos e não só métodos.
func (x *extraction) propertyRef(member parser.Node) {
	parent := member.Parent()
	if parent.Is("call_expression") && parent.NamedChildren()[0].StartByte() == member.StartByte() {
		return // é o alvo de uma chamada: memberCall cuida
	}
	kids := member.NamedChildren()
	if len(kids) < 2 {
		return
	}
	object, property := kids[0], kids[len(kids)-1]
	if !property.Is("property_identifier") || (object.Is("identifier") && builtins[object.Text()]) {
		return
	}
	if parent.Is("member_expression") && object.Is("identifier") && x.isLocalOnly(object.Text()) {
		return
	}
	receiver, receiverType, path := x.receiverChain(object)
	x.ref(extract.RefProperty, property.Text(), property, receiver, receiverType).ReceiverPath = path
}

// memberCall registra `obj.m()` com o receiver e, quando conhecido, o tipo
// declarado do receiver, que é o que permite resolver o método.
func (x *extraction) memberCall(member parser.Node) {
	kids := member.NamedChildren()
	if len(kids) < 2 {
		return
	}
	object, property := kids[0], kids[len(kids)-1]
	if !property.Is("property_identifier") || (object.Is("identifier") && builtins[object.Text()]) {
		return
	}
	// `f.bind(…)`, `f.call(…)`, `f.apply(…)` são chamadas (indiretas) de f;
	// registrar `bind` como método só esconderia o chamador de f.
	if object.Is("identifier") && indirectCalls[property.Text()] && !x.isLocal(object) {
		x.ref(extract.RefCall, object.Text(), object, "", "")
		return
	}
	receiver, receiverType, path := x.receiverChain(object)
	ref := x.ref(extract.RefMethod, property.Text(), property, receiver, receiverType)
	ref.ReceiverPath = path
	x.withArguments(ref, member.Parent())
}

// receiverChain decompõe o objeto em (receiver, tipo, caminho de membros):
// `this.svc.get(id).total()` vira ("svc", "OrderService", [get]);
// `useForm().register` vira ("", "", [useForm]), uma cadeia que começa numa
// função solta.
func (x *extraction) receiverChain(object parser.Node) (string, string, []string) {
	switch object.Type() {
	case "member_expression":
		kids := object.NamedChildren()
		if len(kids) == 2 && !kids[0].Is("this") && kids[1].Is("property_identifier") {
			receiver, receiverType, path := x.receiverChain(kids[0])
			if receiver == "" && receiverType == "" && len(path) == 0 {
				return "", "", nil
			}
			return receiver, receiverType, append(path, kids[1].Text())
		}
	case "call_expression":
		fn := object.NamedChildren()[0]
		switch fn.Type() {
		case "identifier":
			if builtins[fn.Text()] || x.isLocal(fn) {
				return "", "", nil
			}
			return "", "", []string{fn.Text()}
		case "member_expression":
			kids := fn.NamedChildren()
			if len(kids) != 2 || !kids[1].Is("property_identifier") {
				return "", "", nil
			}
			receiver, receiverType, path := x.receiverChain(kids[0])
			if receiver == "" && receiverType == "" && len(path) == 0 {
				return "", "", nil
			}
			return receiver, receiverType, append(path, kids[1].Text())
		}
		return "", "", nil
	case "parenthesized_expression", "non_null_expression", "as_expression":
		return x.receiverChain(lastNamedChild(object))
	}
	receiver, receiverType := x.receiverOf(object)
	return receiver, receiverType, nil
}

var indirectCalls = map[string]bool{"bind": true, "call": true, "apply": true}

// receiverOf devolve (nome do receiver, tipo declarado) para o objeto de
// uma chamada de método. Um nome capitalizado sem declaração é tratado
// como tipo (chamada estática ou namespace importado).
func (x *extraction) receiverOf(object parser.Node) (string, string) {
	switch object.Type() {
	case "identifier":
		name := object.Text()
		if t, ok := x.typeOf(object); ok {
			return name, t
		}
		if x.imported[name] || isUpper(name) {
			return name, name
		}
		return name, ""
	case "this":
		return "this", x.enclosingClass(object)
	case "super":
		// `super.m()`: o membro é procurado a partir da classe estendida.
		return "super", superclass(object)
	case "member_expression":
		kids := object.NamedChildren()
		if len(kids) == 2 && kids[0].Is("this") && kids[1].Is("property_identifier") {
			field := kids[1].Text()
			return field, x.varTypes[field]
		}
	case "new_expression":
		// `new T().m()`: o tipo do receiver é o próprio T.
		if ctor := object.Child("identifier"); !ctor.IsNil() {
			return "new", ctor.Text()
		}
	}
	return "", ""
}

// superclass é a classe estendida pela classe que contém n (`class Eraser
// extends AnimatedTrail`); "" quando o extends não é um nome simples.
func superclass(n parser.Node) string {
	class := n.Ancestor("class_declaration", "abstract_class_declaration", "class")
	return class.Child("class_heritage").Child("extends_clause").Child("identifier").Text()
}

func (x *extraction) enclosingClass(n parser.Node) string {
	class := n.Ancestor("class_declaration", "abstract_class_declaration", "class")
	if class.IsNil() {
		return ""
	}
	name := class.Child("type_identifier", "identifier")
	if name.IsNil() {
		return ""
	}
	return name.Text()
}

func (x *extraction) typeRef(n parser.Node) {
	if aliasBase(n) {
		// `type A = B & { … }`: A herda os membros de B.
		x.ref(extract.RefExtends, n.Text(), n, "", "")
		return
	}
	parent := n.Parent()
	switch parent.Type() {
	case "class_declaration", "abstract_class_declaration", "interface_declaration",
		"type_alias_declaration", "type_parameter", "enum_declaration":
		return // é o nome sendo declarado
	case "implements_clause":
		x.ref(extract.RefImplements, n.Text(), n, "", "")
		return
	case "extends_type_clause":
		x.ref(extract.RefExtends, n.Text(), n, "", "")
		return
	}
	x.ref(extract.RefType, n.Text(), n, "", "")
}

// aliasBase diz se o tipo está onde dá membros a um alias: `type A = B`,
// `B & { … }`, `B | C`, `Readonly<B>`. O nome do alias e os tipos dentro dos
// membros (`{ items: Item[] }`) não contam.
func aliasBase(n parser.Node) bool {
	cur := n
	if cur.Parent().Is("nested_type_identifier") {
		cur = cur.Parent() // `ns.Base`
	}
	for {
		parent := cur.Parent()
		switch parent.Type() {
		case "type_alias_declaration":
			return lastNamedChild(parent).StartByte() == cur.StartByte()
		case "intersection_type", "union_type", "parenthesized_type", "generic_type", "type_arguments":
			cur = parent
		default:
			return false
		}
	}
}

// identifierRef registra identificadores em posição de uso. Posições de
// declaração (nomes de função, parâmetros, imports…) e locais sem
// definição de topo ficam de fora.
func (x *extraction) identifierRef(n parser.Node) {
	name := n.Text()
	if builtins[name] {
		return
	}
	parent := n.Parent()
	switch parent.Type() {
	case "import_specifier", "import_clause", "namespace_import", "import_require_clause",
		"export_specifier", "namespace_export",
		"function_declaration", "generator_function_declaration", "function_expression",
		"required_parameter", "optional_parameter", "rest_parameter",
		"catch_clause", "type_parameter", "internal_module", "module", "labeled_statement",
		"method_definition", "enum_declaration", "jsx_opening_element", "jsx_self_closing_element",
		"jsx_closing_element", "jsx_namespace_name", "pair_pattern", "object_pattern", "array_pattern",
		"call_expression", "new_expression", "decorator", "extends_clause":
		return
	case "variable_declarator":
		if parent.NamedChildren()[0].StartByte() == n.StartByte() {
			return
		}
	case "arrow_function":
		if n.StartByte() < parent.AnonChild("=>").StartByte() {
			return
		}
	case "member_expression":
		if parent.NamedChildren()[0].StartByte() != n.StartByte() {
			return
		}
		if parent.Parent().Is("call_expression") && parent.Parent().NamedChildren()[0].StartByte() == parent.StartByte() {
			return // já registrado como chamada de método
		}
	case "for_in_statement":
		if parent.NamedChildren()[0].StartByte() == n.StartByte() {
			return
		}
	}
	if x.isLocal(n) {
		return
	}
	x.ref(extract.RefIdentifier, name, n, "", "")
}

// isLocalOnly diz se o nome é só uma variável/parâmetro de função, sem
// definição de topo nem import: uma ref a ele nunca resolveria para nada.
func (x *extraction) isLocalOnly(name string) bool {
	return x.locals[name] && !x.imported[name] && !x.isTopLevel(name)
}

// isLocal diz se o identificador, nessa posição, é uma variável ou um
// parâmetro: o nome só existe como local no arquivo, ou uma declaração local
// cujo escopo contém o uso esconde o import ou o símbolo de topo de mesmo
// nome (`let i = 0` fora e `(field, i) => i` dentro). Uma local que é ela
// mesma um símbolo, como a arrow function aninhada, não esconde nada.
func (x *extraction) isLocal(id parser.Node) bool {
	if x.isLocalOnly(id.Text()) {
		return true
	}
	at := id.StartByte()
	for _, l := range x.localTypes[id.Text()] {
		if l.start <= at && at < l.end && !x.declaresSymbol(id.Text(), l.pos) {
			return true
		}
	}
	return false
}

// declaresSymbol diz se a declaração do nome na posição pos é um símbolo.
func (x *extraction) declaresSymbol(name string, pos int) bool {
	for _, s := range x.result.Symbols {
		if s.Name == name && s.StartByte <= pos && pos < s.EndByte {
			return true
		}
	}
	return false
}

// isTopLevel diz se existe um símbolo com esse nome no arquivo, de topo ou
// aninhado: uma função interna também merece refs.
func (x *extraction) shorthandRef(n parser.Node) {
	name := n.Text()
	if builtins[name] || x.isLocal(n) {
		return
	}
	x.ref(extract.RefIdentifier, name, n, "", "")
}

func (x *extraction) isTopLevel(name string) bool {
	for _, s := range x.result.Symbols {
		if s.Name == name {
			return true
		}
	}
	return false
}

// decoratorRef trata `@Input` (identifier direto); `@Injectable()` passa
// por callRef, que reconhece o pai decorator.
func (x *extraction) decoratorRef(n parser.Node) {
	if id := n.Child("identifier"); !id.IsNil() {
		x.ref(extract.RefAnnotation, id.Text(), id, "", "")
	}
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

// arguments conta os argumentos de uma chamada ou `new` e o tipo de cada um
// quando dá para saber; -1 e nil quando há spread ou não há lista.
func (x *extraction) arguments(call parser.Node) (int, []string) {
	args := call.Child("arguments")
	if args.IsNil() {
		if call.Is("new_expression") {
			return 0, nil // `new Foo` sem parênteses
		}
		return -1, nil
	}
	var types []string
	for _, a := range args.NamedChildren() {
		if a.Is("spread_element") {
			return -1, nil
		}
		types = append(types, extract.ArgType(x.argType(a)))
	}
	return len(types), types
}

// argType é o tipo de um argumento: literal, variável com tipo declarado
// no arquivo, `new T()`, `x as T`; "" quando não se sabe.
func (x *extraction) argType(n parser.Node) string {
	switch n.Type() {
	case "identifier":
		t, _ := x.typeOf(n)
		return t
	case "string", "template_string":
		return "string"
	case "number":
		return "number"
	case "true", "false":
		return "boolean"
	case "null", "undefined":
		return "null"
	case "object":
		return "object"
	case "array":
		return "array"
	case "arrow_function", "function_expression":
		return "function"
	case "new_expression":
		return n.Child("identifier").Text()
	case "as_expression", "satisfies_expression":
		return typeName(lastNamedChild(n))
	case "parenthesized_expression":
		return x.argType(lastNamedChild(n))
	case "this":
		return x.enclosingClass(n)
	}
	return ""
}

func (x *extraction) withArguments(ref *extract.Ref, call parser.Node) {
	ref.Arity, ref.ArgTypes = x.arguments(call)
}
