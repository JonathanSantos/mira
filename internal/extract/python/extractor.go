// Package python extrai símbolos, refs e imports de arquivos Python.
// Métodos ficam com Container = classe; funções aninhadas viram
// `Externa.interna`; bases de classe viram refs `extends`.
package python

import (
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

type Extractor struct{}

func New() *Extractor { return &Extractor{} }

// ModuleReceiver marca no ReceiverType que o receiver é um módulo
// importado (`np.sum()`), não uma variável com tipo.
const ModuleReceiver = "module"

type extraction struct {
	result     extract.Result
	modules    map[string]bool              // nome local que é um módulo importado
	imported   map[string]bool              // nomes trazidos por `from x import y`
	locals     map[string]bool              // parâmetros e variáveis de função
	varTypes   map[string]string            // variável/parâmetro -> tipo anotado ou construído
	fieldTypes map[string]map[string]string // classe -> atributo -> tipo
}

func (e *Extractor) Extract(tree *parser.Tree) extract.Result {
	x := &extraction{
		modules: map[string]bool{}, imported: map[string]bool{}, locals: map[string]bool{},
		varTypes: map[string]string{}, fieldTypes: map[string]map[string]string{},
	}
	root := tree.Root()
	x.collectImports(root)
	x.collectSymbols(root, "", "")
	x.collectDeclarations(root)
	// Atributos de instância entram durante as declarações: ordena por
	// posição antes das refs, que guardam índices de container.
	sort.SliceStable(x.result.Symbols, func(i, j int) bool { return x.result.Symbols[i].StartByte < x.result.Symbols[j].StartByte })
	x.collectRefs(root)
	return x.result
}

func (x *extraction) collectImports(root parser.Node) {
	root.Walk(func(n parser.Node) bool {
		switch n.Type() {
		case "import_statement":
			for _, c := range n.NamedChildren() {
				switch c.Type() {
				case "dotted_name":
					// `import a.b` liga o nome `a`.
					first := c.Child("identifier").Text()
					x.modules[first] = true
					x.result.Imports = append(x.result.Imports, extract.Import{Kind: "python", Module: c.Text(), LocalName: first, Line: c.StartLine()})
				case "aliased_import":
					alias := c.Child("identifier").Text()
					x.modules[alias] = true
					x.result.Imports = append(x.result.Imports, extract.Import{Kind: "python", Module: c.Child("dotted_name").Text(), LocalName: alias, Line: c.StartLine()})
				}
			}
			return false
		case "import_from_statement":
			module := ""
			if m := n.Child("dotted_name", "relative_import"); !m.IsNil() {
				module = m.Text()
			}
			kids := n.NamedChildren()
			if len(kids) > 0 && kids[0].Is("dotted_name", "relative_import") {
				kids = kids[1:]
			}
			for _, c := range kids {
				switch c.Type() {
				case "dotted_name":
					name := c.Text()
					x.imported[name] = true
					x.result.Imports = append(x.result.Imports, extract.Import{Kind: "python", Module: module, ImportedName: name, LocalName: name, Line: c.StartLine()})
				case "aliased_import":
					name, alias := c.Child("dotted_name").Text(), c.Child("identifier").Text()
					x.imported[alias] = true
					x.result.Imports = append(x.result.Imports, extract.Import{Kind: "python", Module: module, ImportedName: name, LocalName: alias, Line: c.StartLine()})
				case "wildcard_import":
					x.result.Imports = append(x.result.Imports, extract.Import{Kind: "python", Module: module, IsWildcard: true, Line: c.StartLine()})
				}
			}
			return false
		}
		return !n.Is("function_definition", "class_definition")
	})
}

// collectSymbols percorre um bloco (módulo, classe ou função) registrando
// definições com o container dado.
func (x *extraction) collectSymbols(block parser.Node, container, qualifiedPrefix string) {
	for _, stmt := range block.NamedChildren() {
		decl, annotations, span := stmt, []string(nil), stmt
		if stmt.Is("decorated_definition") {
			for _, d := range stmt.ChildrenOf("decorator") {
				annotations = append(annotations, extract.Collapse(d.Text()))
			}
			decl = stmt.Child("function_definition", "class_definition")
		}
		switch decl.Type() {
		case "function_definition":
			x.function(decl, span, container, qualifiedPrefix, annotations)
		case "class_definition":
			x.class(decl, span, container, qualifiedPrefix, annotations)
		case "expression_statement":
			if a := decl.Child("assignment"); !a.IsNil() {
				x.assignment(a, container, qualifiedPrefix)
			}
		}
	}
}

func (x *extraction) function(decl, span parser.Node, container, qualifiedPrefix string, annotations []string) {
	name := decl.Child("identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("block")
	kind := extract.KindFunction
	if container != "" && x.isClass(container) {
		kind = extract.KindMethod
	}
	qualified := join(qualifiedPrefix, name.Text())
	x.add(extract.Symbol{
		Name: name.Text(), QualifiedName: qualified, Kind: kind, Container: container,
		Signature: strings.TrimSuffix(extract.Trim(extract.SignatureUntil(decl, body)), ":"),
		Exported:  !strings.HasPrefix(name.Text(), "_"), ExportName: name.Text(),
		JSX:       false,
		StartLine: span.StartLine(), EndLine: span.EndLine(), StartByte: span.StartByte(), EndByte: span.EndByte(),
		NameLine: name.StartLine(), Annotations: annotations,
	})
	if !body.IsNil() {
		x.collectSymbols(body, name.Text(), qualified) // funções aninhadas: Externa.interna
	}
}

func (x *extraction) class(decl, span parser.Node, container, qualifiedPrefix string, annotations []string) {
	name := decl.Child("identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("block")
	x.add(extract.Symbol{
		Name: name.Text(), QualifiedName: join(qualifiedPrefix, name.Text()), Kind: extract.KindClass, Container: container,
		Signature: strings.TrimSuffix(extract.Trim(extract.SignatureUntil(decl, body)), ":"),
		Exported:  !strings.HasPrefix(name.Text(), "_"), ExportName: name.Text(),
		StartLine: span.StartLine(), EndLine: span.EndLine(), StartByte: span.StartByte(), EndByte: span.EndByte(),
		NameLine: name.StartLine(), Annotations: annotations,
	})
	x.fieldTypes[name.Text()] = map[string]string{}
	if !body.IsNil() {
		x.collectSymbols(body, name.Text(), join(qualifiedPrefix, name.Text()))
	}
}

// assignment registra `NAME = …` no módulo como variável e `attr: T = …`
// na classe como campo.
func (x *extraction) assignment(a parser.Node, container, qualifiedPrefix string) {
	target := a.NamedChildren()[0]
	if !target.Is("identifier") {
		return
	}
	kind := extract.KindVariable
	if container != "" && x.isClass(container) {
		kind = extract.KindField
		if t := typeText(a.Child("type")); t != "" {
			x.fieldTypes[container][target.Text()] = t
		}
	} else if container != "" {
		return // atribuição dentro de função: local, não símbolo
	}
	sig := extract.Collapse(strings.TrimSpace(strings.SplitN(a.Text(), "=", 2)[0]))
	hint := typeText(a.Child("type"))
	if hint == "" {
		hint = x.valueType(lastNamed(a)) // `ZERO = Money()`: o tipo da variável
	}
	x.add(extract.Symbol{
		Name: target.Text(), QualifiedName: join(qualifiedPrefix, target.Text()), Kind: kind, Container: container,
		Signature: sig, Exported: !strings.HasPrefix(target.Text(), "_"), ExportName: target.Text(),
		StartLine: a.StartLine(), EndLine: a.EndLine(), StartByte: a.StartByte(), EndByte: a.EndByte(), NameLine: target.StartLine(),
		ReturnHint: hint,
	})
}

func (x *extraction) isClass(name string) bool {
	_, ok := x.fieldTypes[name]
	return ok
}

func (x *extraction) add(s extract.Symbol) {
	if s.QualifiedName == "" {
		s.QualifiedName = s.Name
	}
	x.result.Symbols = append(x.result.Symbols, s)
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// typeText reduz uma anotação de tipo ao nome resolvível: `Pet` -> Pet,
// `list[Pet]` -> Pet[], `Optional[Pet]` -> Pet, `mod.Pet` -> mod.Pet.
func typeText(t parser.Node) string {
	if t.IsNil() {
		return ""
	}
	if t.Is("type") {
		return typeText(lastNamed(t))
	}
	switch t.Type() {
	case "identifier":
		return t.Text()
	case "attribute":
		return extract.Collapse(t.Text())
	case "generic_type", "subscript":
		base := typeText(t.NamedChildren()[0])
		args := t.NamedChildren()[1:]
		if arg := t.Child("type_parameter"); !arg.IsNil() {
			args = arg.NamedChildren()
		}
		if len(args) == 0 {
			return base
		}
		inner := typeText(args[len(args)-1])
		switch base {
		case "list", "List", "set", "Set", "Sequence", "Iterable", "Iterator", "tuple", "Tuple":
			return inner + "[]"
		case "Optional", "Awaitable", "Coroutine", "Type", "type":
			return inner
		}
		return base
	case "string":
		return strings.Trim(t.Text(), "\"'") // anotação adiada: "Pet"
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
