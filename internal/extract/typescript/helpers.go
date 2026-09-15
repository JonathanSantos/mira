package typescript

import (
	"strings"
	"unicode"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

func lastNamedChild(n parser.Node) parser.Node {
	kids := n.NamedChildren()
	if len(kids) == 0 {
		return parser.Node{}
	}
	return kids[len(kids)-1]
}

func exportNameOr(exportName, name string) string {
	if exportName != "" {
		return exportName
	}
	return name
}

// prefixed devolve a assinatura com "export"/"export default" na frente,
// já que o nó da declaração não inclui a keyword.
func prefixed(exported bool, exportName, sig string) string {
	if !exported {
		return sig
	}
	if exportName == "default" {
		return "export default " + sig
	}
	return "export " + sig
}

// signatureAfterDecorators pula os decorators que são filhos da própria
// declaração (caso de classes não exportadas).
func signatureAfterDecorators(decl, stop parser.Node) string {
	start := decl.StartByte()
	for _, c := range decl.Children() {
		if !c.Is("decorator") {
			start = c.StartByte()
			break
		}
	}
	text := decl.Text()[start-decl.StartByte():]
	if !stop.IsNil() && stop.StartByte() > start {
		text = text[:stop.StartByte()-start]
	}
	return extract.Collapse(text)
}

// functionBody devolve o corpo de uma função/arrow/classe: o bloco, ou a
// expressão depois de "=>" em arrow functions concisas.
func functionBody(fn parser.Node) parser.Node {
	if body := fn.Child("statement_block", "class_body"); !body.IsNil() {
		return body
	}
	if fn.Is("arrow_function") {
		return lastNamedChild(fn)
	}
	return parser.Node{}
}

// classDecorators acha os decorators de uma classe: filhos do
// export_statement quando exportada, da própria declaração quando não.
func classDecorators(decl, span parser.Node) []parser.Node {
	if span.StartByte() != decl.StartByte() {
		return span.ChildrenOf("decorator")
	}
	return decl.ChildrenOf("decorator")
}

// decoratorTexts devolve os decorators como aparecem no fonte, colapsados.
func decoratorTexts(decorators []parser.Node) []string {
	var out []string
	for _, d := range decorators {
		out = append(out, extract.Collapse(d.Text()))
	}
	return out
}

func keywordOf(decl parser.Node) string {
	for _, c := range decl.Children() {
		if !c.IsNamed() {
			return c.Text()
		}
	}
	return "const"
}

func hasAccessModifier(member parser.Node, modifiers ...string) bool {
	mod := member.Child("accessibility_modifier")
	if mod.IsNil() {
		return false
	}
	for _, m := range modifiers {
		if mod.Text() == m {
			return true
		}
	}
	return false
}

func isRequireCall(n parser.Node) bool {
	if !n.Is("call_expression") {
		return false
	}
	fn := n.Child("identifier")
	return !fn.IsNil() && fn.Text() == "require"
}

// returnsJSX diz se há JSX em qualquer ponto do nó (corpo de função ou
// expressão de arrow function).
func returnsJSX(n parser.Node) bool {
	found := false
	n.Walk(func(c parser.Node) bool {
		if found {
			return false
		}
		if c.Is("jsx_element", "jsx_self_closing_element", "jsx_fragment") {
			found = true
			return false
		}
		return true
	})
	return found
}

func isUpper(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper(rune(name[0]))
}

// typeName extrai o nome de tipo "resolvível" de uma anotação: um
// type_identifier direto ou o nome de um generic_type. Arrays, unions e
// tipos primitivos não apontam para um símbolo e devolvem "".
func typeName(n parser.Node) string {
	if n.IsNil() {
		return ""
	}
	if n.Is("type_annotation") {
		n = lastNamedChild(n)
	}
	switch n.Type() {
	case "type_identifier":
		return n.Text()
	case "generic_type":
		base := typeName(n.Child("type_identifier", "nested_type_identifier"))
		if tsContainers[base] {
			return extract.Collapse(n.Text()) // `Array<Svc>`: o elemento importa para cadeias
		}
		if args := n.Child("type_arguments").NamedChildren(); sameMembers[base] && len(args) > 0 {
			return typeName(args[0]) // `Readonly<Svc>`, `Pick<Svc, "a">`: os membros são os de Svc
		}
		return base
	case "nested_type_identifier":
		return lastNamedChild(n).Text()
	case "union_type":
		// `Svc | null`, `Svc | undefined`: sem o nulo, os membros são os de Svc.
		var kept []parser.Node
		for _, member := range n.NamedChildren() {
			if member.Text() != "null" && member.Text() != "undefined" {
				kept = append(kept, member)
			}
		}
		if len(kept) == 1 {
			return typeName(kept[0])
		}
	case "readonly_type":
		return typeName(lastNamedChild(n)) // `readonly Item[]`
	case "lookup_type":
		// `App["scene"]`: o tipo do membro, que o resolvedor segue.
		if kids := n.NamedChildren(); len(kids) == 2 {
			base, index := typeName(kids[0]), strings.TrimSpace(kids[1].Text())
			if base != "" && len(index) >= 2 && (index[0] == '"' || index[0] == '\'') && index[len(index)-1] == index[0] {
				return base + `["` + index[1:len(index)-1] + `"]`
			}
		}
	case "array_type":
		if inner := typeName(lastNamedChild(n)); inner != "" {
			return inner + "[]"
		}
	}
	return ""
}

// tsContainers são os genéricos do runtime cujo elemento o resolvedor sabe
// seguir (`Array<Svc>`, `Promise<Svc>`).
var tsContainers = map[string]bool{"Array": true, "ReadonlyArray": true, "Promise": true, "Set": true}

// sameMembers são os utilitários do TypeScript cujos membros são os do
// primeiro argumento de tipo (ou um subconjunto deles).
var sameMembers = map[string]bool{"Readonly": true, "Partial": true, "Required": true, "Pick": true, "Omit": true, "NonNullable": true}

func moduleOf(stringNode parser.Node) string {
	if stringNode.IsNil() {
		return ""
	}
	return strings.Trim(stringNode.Text(), "`'\"")
}
