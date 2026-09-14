// Package golang extrai símbolos, refs e imports de arquivos Go. Métodos
// ficam com Container = tipo do receiver; campos embutidos numa struct
// viram refs `extends`, o que faz a promoção de métodos funcionar como
// herança no resolvedor.
package golang

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

type Extractor struct{}

func New() *Extractor { return &Extractor{} }

// PackageReceiver marca no ReceiverType que o receiver é um alias de
// import (`svc.New()`), não uma variável com tipo.
const PackageReceiver = "package"

type extraction struct {
	result     extract.Result
	imports    map[string]bool              // alias -> importado
	varTypes   map[string]string            // variável de pacote -> tipo simples
	locals     map[string][]local           // nome declarado dentro de função -> declarações com escopo
	fieldTypes map[string]map[string]string // struct -> campo -> tipo
	structIdx  map[string]int               // struct -> índice do símbolo
	substDepth int                          // guarda da substituição contra definições circulares
}

// local é uma declaração dentro de função (`p *Pet`, `cmd := c.Root()`,
// `for _, cmd := range c.commands`), válida no escopo [start, end) e só
// depois da declaração (from). typ é o tipo escrito ou deduzido do valor;
// sem ele, expr é a cadeia que define a local e elem diz que a local é um
// elemento dessa cadeia.
type local struct {
	from, start, end int
	typ              string
	expr             parser.Node
	elem             bool
}

func (e *Extractor) Extract(tree *parser.Tree) extract.Result {
	x := &extraction{
		imports: map[string]bool{}, varTypes: map[string]string{}, locals: map[string][]local{},
		fieldTypes: map[string]map[string]string{}, structIdx: map[string]int{},
	}
	root := tree.Root()
	x.result.Package = root.Child("package_clause").Child("package_identifier").Text()
	x.collectImports(root)
	x.collectSymbols(root)
	x.collectDeclarations(root)
	x.collectRefs(root)
	return x.result
}

func (x *extraction) collectImports(root parser.Node) {
	for _, decl := range root.ChildrenOf("import_declaration") {
		specs := decl.ChildrenOf("import_spec")
		if list := decl.Child("import_spec_list"); !list.IsNil() {
			specs = list.ChildrenOf("import_spec")
		}
		for _, spec := range specs {
			path := strings.Trim(spec.Child("interpreted_string_literal").Text(), "\"")
			if path == "" {
				continue
			}
			local := importName(path)
			if alias := spec.Child("package_identifier"); !alias.IsNil() {
				local = alias.Text()
			}
			if local == "_" || local == "." {
				continue
			}
			x.imports[local] = true
			x.result.Imports = append(x.result.Imports, extract.Import{
				Kind: "go", Module: path, ImportedName: "", LocalName: local, Line: spec.StartLine(),
			})
		}
	}
}

// importName é o nome de pacote implícito de um caminho: o último segmento,
// sem o sufixo de versão (`.../v2`).
func importName(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if len(parts) > 1 && strings.HasPrefix(name, "v") && strings.Trim(name[1:], "0123456789") == "" && name != "v" {
		name = parts[len(parts)-2]
	}
	return strings.ReplaceAll(strings.TrimPrefix(name, "go-"), "-", "")
}

func (x *extraction) collectSymbols(root parser.Node) {
	for _, decl := range root.NamedChildren() {
		switch decl.Type() {
		case "function_declaration":
			x.function(decl)
		case "method_declaration":
			x.method(decl)
		case "type_declaration":
			specs := decl.ChildrenOf("type_spec", "type_alias") // `type A = B` conta como tipo
			for _, spec := range specs {
				x.typeSpec(decl, spec, len(specs) == 1)
			}
		case "var_declaration", "const_declaration":
			for _, spec := range decl.ChildrenOf("var_spec", "const_spec") {
				x.variable(decl, spec)
			}
		}
	}
}

func (x *extraction) function(decl parser.Node) {
	name := decl.Child("identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("block")
	x.add(extract.Symbol{
		Name: name.Text(), Kind: extract.KindFunction,
		Signature: extract.Trim(extract.SignatureUntil(decl, body)),
		Exported:  isUpper(name.Text()), ExportName: name.Text(),
		StartLine: decl.StartLine(), EndLine: decl.EndLine(), StartByte: decl.StartByte(), EndByte: decl.EndByte(),
		NameLine: name.StartLine(),
	})
}

func (x *extraction) method(decl parser.Node) {
	name := decl.Child("field_identifier")
	lists := decl.ChildrenOf("parameter_list")
	if name.IsNil() || len(lists) == 0 {
		return
	}
	recv := simpleType(lists[0].Child("parameter_declaration").Child("pointer_type", "type_identifier", "generic_type"))
	body := decl.Child("block")
	x.add(extract.Symbol{
		Name: name.Text(), QualifiedName: recv + "." + name.Text(), Kind: extract.KindMethod, Container: recv,
		Signature: extract.Trim(extract.SignatureUntil(decl, body)),
		Exported:  isUpper(name.Text()), ExportName: name.Text(),
		StartLine: decl.StartLine(), EndLine: decl.EndLine(), StartByte: decl.StartByte(), EndByte: decl.EndByte(),
		NameLine: name.StartLine(),
	})
}

func (x *extraction) typeSpec(decl, spec parser.Node, single bool) {
	name := spec.Child("type_identifier")
	if name.IsNil() {
		return
	}
	span := spec
	if single {
		span = decl // `type X struct {…}` inteiro, com a keyword
	}
	typ := lastNamed(spec)
	kind := extract.KindType
	switch typ.Type() {
	case "struct_type":
		kind = extract.KindClass
	case "interface_type":
		kind = extract.KindInterface
	}
	body := typ.Child("field_declaration_list")
	if typ.Is("interface_type") {
		body = typ
	}
	sig := "type " + extract.Trim(extract.SignatureUntil(spec, body))
	if body.IsNil() {
		sig = "type " + extract.Collapse(spec.Text())
	}
	x.add(extract.Symbol{
		Name: name.Text(), Kind: kind, Signature: sig,
		Exported: isUpper(name.Text()), ExportName: name.Text(),
		StartLine: span.StartLine(), EndLine: span.EndLine(), StartByte: span.StartByte(), EndByte: span.EndByte(),
		NameLine: name.StartLine(),
	})
	x.structIdx[name.Text()] = len(x.result.Symbols) - 1
	switch typ.Type() {
	case "struct_type":
		x.fields(typ, name.Text())
	case "interface_type":
		for _, m := range typ.ChildrenOf("method_elem", "method_spec") {
			mname := m.Child("field_identifier")
			if mname.IsNil() {
				continue
			}
			x.add(extract.Symbol{
				Name: mname.Text(), QualifiedName: name.Text() + "." + mname.Text(), Kind: extract.KindMethod, Container: name.Text(),
				Signature: extract.Collapse(m.Text()), Exported: isUpper(mname.Text()), ExportName: mname.Text(),
				StartLine: m.StartLine(), EndLine: m.EndLine(), StartByte: m.StartByte(), EndByte: m.EndByte(), NameLine: m.StartLine(),
			})
		}
		for _, embedded := range typ.ChildrenOf("type_elem") {
			x.embed(embedded, name.Text())
		}
	}
}

// fields registra os campos de uma struct; um campo sem nome é embutido:
// o tipo dele passa métodos e campos para a struct (`extends`).
func (x *extraction) fields(structType parser.Node, structName string) {
	x.fieldTypes[structName] = map[string]string{}
	for _, f := range structType.Child("field_declaration_list").ChildrenOf("field_declaration") {
		names := f.ChildrenOf("field_identifier")
		typ := f.Child("type_identifier", "pointer_type", "slice_type", "array_type", "map_type", "qualified_type", "generic_type", "channel_type", "function_type", "interface_type", "struct_type")
		if len(names) == 0 {
			x.embed(f, structName)
			continue
		}
		for _, n := range names {
			x.fieldTypes[structName][n.Text()] = simpleType(typ)
			x.add(extract.Symbol{
				Name: n.Text(), QualifiedName: structName + "." + n.Text(), Kind: extract.KindField, Container: structName,
				Signature: extract.Collapse(f.Text()), Exported: isUpper(n.Text()), ExportName: n.Text(),
				StartLine: f.StartLine(), EndLine: f.EndLine(), StartByte: f.StartByte(), EndByte: f.EndByte(), NameLine: n.StartLine(),
			})
		}
	}
}

func (x *extraction) embed(node parser.Node, structName string) {
	typ := node.Child("type_identifier", "pointer_type", "qualified_type", "generic_type")
	if node.Is("type_identifier", "qualified_type") {
		typ = node
	}
	if typ.IsNil() {
		return
	}
	if typ.Is("pointer_type") {
		typ = lastNamed(typ)
	}
	if typ.Is("qualified_type") {
		x.ref(extract.RefExtends, typ.Child("type_identifier").Text(), typ, typ.Child("package_identifier").Text(), PackageReceiver).Container = x.structIdx[structName]
		return
	}
	if name := simpleType(typ); name != "" {
		x.ref(extract.RefExtends, name, typ, "", "").Container = x.structIdx[structName]
	}
}

func (x *extraction) variable(decl, spec parser.Node) {
	typ := spec.Child("type_identifier", "pointer_type", "slice_type", "map_type", "qualified_type", "generic_type")
	hint := simpleType(typ)
	if hint == "" {
		if values := spec.Child("expression_list"); !values.IsNil() && len(values.NamedChildren()) > 0 {
			hint = x.valueType(values.NamedChildren()[0]) // `var Zero = Money{}`
		}
	}
	for _, n := range spec.ChildrenOf("identifier") {
		x.add(extract.Symbol{
			Name: n.Text(), Kind: extract.KindVariable,
			Signature: extract.Collapse(strings.TrimSpace(strings.SplitN(decl.Text(), "=", 2)[0])),
			Exported:  isUpper(n.Text()), ExportName: n.Text(),
			StartLine: spec.StartLine(), EndLine: spec.EndLine(), StartByte: spec.StartByte(), EndByte: spec.EndByte(), NameLine: n.StartLine(),
			ReturnHint: hint,
		})
		if hint != "" {
			x.varTypes[n.Text()] = hint
		}
	}
}

func (x *extraction) add(s extract.Symbol) {
	if s.QualifiedName == "" {
		s.QualifiedName = s.Name
	}
	x.result.Symbols = append(x.result.Symbols, s)
}

// simpleType reduz um nó de tipo ao nome resolvível: `*Owner` -> Owner,
// `[]*Pet` -> Pet[], `svc.Client` -> svc.Client, `map[string]int` -> "".
func simpleType(n parser.Node) string {
	switch n.Type() {
	case "type_identifier":
		return n.Text()
	case "pointer_type", "parenthesized_type":
		return simpleType(lastNamed(n))
	case "slice_type", "array_type":
		if inner := simpleType(lastNamed(n)); inner != "" {
			return inner + "[]"
		}
	case "qualified_type":
		return n.Child("package_identifier").Text() + "." + n.Child("type_identifier").Text()
	case "generic_type":
		return simpleType(n.Child("type_identifier", "qualified_type"))
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

func isUpper(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}
