// Package java extrai símbolos, referências e imports de arquivos Java.
package java

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// Extractor implementa extract.Extractor para Java.
type Extractor struct{}

func New() *Extractor { return &Extractor{} }

type extraction struct {
	result extract.Result
	pkg    string
	// fieldTypes mapeia campo -> tipo simples, por classe.
	fieldTypes map[string]map[string]string
	// localTypes cacheia, por corpo de método (byte inicial), variável -> tipo.
	localTypes map[int]map[string]string
}

func (e *Extractor) Extract(tree *parser.Tree) extract.Result {
	x := &extraction{
		fieldTypes: map[string]map[string]string{},
		localTypes: map[int]map[string]string{},
	}
	root := tree.Root()
	x.pkg = packageOf(root)
	x.result.Package = x.pkg
	x.collectImports(root)
	for _, decl := range root.NamedChildren() {
		x.typeDeclaration(decl, "", x.pkg)
	}
	x.collectRefs(root)
	return x.result
}

func packageOf(root parser.Node) string {
	pkg := root.Child("package_declaration")
	if pkg.IsNil() {
		return ""
	}
	name := pkg.Child("scoped_identifier", "identifier")
	if name.IsNil() {
		return ""
	}
	return name.Text()
}

var typeDeclarations = []string{
	"class_declaration", "interface_declaration", "enum_declaration",
	"record_declaration", "annotation_type_declaration",
}

// typeDeclaration registra um tipo e seus membros. qualifiedPrefix é o
// prefixo do nome qualificado (package ou tipo externo).
func (x *extraction) typeDeclaration(decl parser.Node, container, qualifiedPrefix string) {
	if !decl.Is(typeDeclarations...) {
		return
	}
	name := decl.Child("identifier")
	if name.IsNil() {
		return
	}
	typeName := name.Text()
	qualified := join(qualifiedPrefix, typeName)
	body := decl.Child("class_body", "interface_body", "enum_body", "annotation_type_body")
	x.add(extract.Symbol{
		Name:          typeName,
		QualifiedName: qualified,
		Kind:          kindOf(decl),
		Container:     container,
		Signature:     extract.Trim(signature(decl, body)),
		Exported:      isPublic(decl) || isInterfaceMember(decl),
		StartLine:     decl.StartLine(),
		EndLine:       decl.EndLine(),
		StartByte:     decl.StartByte(),
		EndByte:       decl.EndByte(),
		NameLine:      name.StartLine(),
		Annotations:   annotationTexts(decl),
	})
	if body.IsNil() {
		return
	}
	x.fieldTypes[typeName] = map[string]string{}
	x.recordComponents(decl, typeName, qualified)
	x.members(body, typeName, qualified)
}

// members percorre um corpo de tipo; enums guardam os membros num
// enum_body_declarations aninhado.
func (x *extraction) members(body parser.Node, typeName, qualified string) {
	for _, m := range body.NamedChildren() {
		switch m.Type() {
		case "method_declaration":
			x.method(m, typeName, qualified, extract.KindMethod)
		case "constructor_declaration":
			x.method(m, typeName, qualified, extract.KindConstructor)
		case "field_declaration", "constant_declaration":
			x.fields(m, typeName, qualified)
		case "enum_body_declarations":
			x.members(m, typeName, qualified)
		default:
			x.typeDeclaration(m, typeName, qualified)
		}
	}
}

func (x *extraction) method(m parser.Node, typeName, qualified, kind string) {
	name := m.Child("identifier")
	if name.IsNil() {
		return
	}
	body := m.Child("block", "constructor_body")
	x.add(extract.Symbol{
		Name:          name.Text(),
		QualifiedName: qualified + "." + name.Text(),
		Kind:          kind,
		Container:     typeName,
		Signature:     extract.Trim(signature(m, body)),
		Exported:      isPublic(m) || isInterfaceMember(m),
		StartLine:     m.StartLine(),
		EndLine:       m.EndLine(),
		StartByte:     m.StartByte(),
		EndByte:       m.EndByte(),
		NameLine:      name.StartLine(),
		Annotations:   annotationTexts(m),
	})
}

func (x *extraction) fields(decl parser.Node, typeName, qualified string) {
	fieldType := simpleTypeName(decl.Child(typeNodeTypes...))
	for _, d := range decl.ChildrenOf("variable_declarator") {
		name := d.Child("identifier")
		if name.IsNil() {
			continue
		}
		x.fieldTypes[typeName][name.Text()] = fieldType
		x.add(extract.Symbol{
			Name:          name.Text(),
			QualifiedName: qualified + "." + name.Text(),
			Kind:          extract.KindField,
			Container:     typeName,
			Signature:     fieldSignature(decl, name),
			Exported:      isPublic(decl) || isInterfaceMember(decl),
			StartLine:     decl.StartLine(),
			EndLine:       decl.EndLine(),
			StartByte:     decl.StartByte(),
			EndByte:       decl.EndByte(),
			NameLine:      name.StartLine(),
			Annotations:   annotationTexts(decl),
		})
	}
}

// recordComponents trata `record Line(String sku, int qty)`: cada componente
// é um campo implícito e também o accessor `sku()`; fica registrado como
// field e o resolvedor aceita fields de record como alvo de chamada.
func (x *extraction) recordComponents(decl parser.Node, typeName, qualified string) {
	params := decl.Child("formal_parameters")
	if params.IsNil() || !decl.Is("record_declaration") {
		return
	}
	for _, p := range params.ChildrenOf("formal_parameter") {
		name := p.Child("identifier")
		if name.IsNil() {
			continue
		}
		typ := p.Child(typeNodeTypes...)
		x.fieldTypes[typeName][name.Text()] = simpleTypeName(typ)
		x.add(extract.Symbol{
			Name:          name.Text(),
			QualifiedName: qualified + "." + name.Text(),
			Kind:          extract.KindField,
			Container:     typeName,
			Signature:     extract.Collapse(typ.Text() + " " + name.Text()),
			Exported:      true,
			StartLine:     p.StartLine(),
			EndLine:       p.EndLine(),
			StartByte:     p.StartByte(),
			EndByte:       p.EndByte(),
			NameLine:      name.StartLine(),
		})
	}
}

func (x *extraction) add(s extract.Symbol) {
	x.result.Symbols = append(x.result.Symbols, s)
}

func kindOf(decl parser.Node) string {
	switch decl.Type() {
	case "interface_declaration":
		return extract.KindInterface
	case "enum_declaration":
		return extract.KindEnum
	case "record_declaration":
		return extract.KindRecord
	case "annotation_type_declaration":
		return extract.KindAnnotation
	}
	return extract.KindClass
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// signature monta "modificadores + tipo + nome + parâmetros" do começo do
// nó até o corpo, descartando as anotações dos modificadores.
func signature(decl, stop parser.Node) string {
	var parts []string
	for _, c := range decl.Children() {
		if !stop.IsNil() && c.StartByte() >= stop.StartByte() {
			break
		}
		if !c.IsNamed() && c.Text() == ";" {
			break // método abstrato/de interface: termina em ";"
		}
		if c.Is("modifiers") {
			parts = append(parts, modifiersWithoutAnnotations(c)...)
			continue
		}
		parts = append(parts, c.Text())
	}
	// A lista de parâmetros é um filho próprio; sem isso sairia "total (…)".
	return strings.ReplaceAll(extract.Collapse(strings.Join(parts, " ")), " (", "(")
}

// fieldSignature monta "modificadores tipo nome" para um declarador de campo,
// sem o inicializador.
func fieldSignature(decl, name parser.Node) string {
	var parts []string
	if mods := decl.Child("modifiers"); !mods.IsNil() {
		parts = append(parts, modifiersWithoutAnnotations(mods)...)
	}
	if typ := decl.Child(typeNodeTypes...); !typ.IsNil() {
		parts = append(parts, typ.Text())
	}
	parts = append(parts, name.Text())
	return extract.Collapse(strings.Join(parts, " "))
}

// annotationTexts devolve as anotações dos modificadores como no fonte.
func annotationTexts(decl parser.Node) []string {
	mods := decl.Child("modifiers")
	if mods.IsNil() {
		return nil
	}
	var out []string
	for _, c := range mods.ChildrenOf("annotation", "marker_annotation") {
		out = append(out, extract.Collapse(c.Text()))
	}
	return out
}

func modifiersWithoutAnnotations(mods parser.Node) []string {
	var out []string
	for _, c := range mods.Children() {
		if c.Is("annotation", "marker_annotation") {
			continue
		}
		out = append(out, c.Text())
	}
	return out
}

func isPublic(decl parser.Node) bool {
	mods := decl.Child("modifiers")
	if mods.IsNil() {
		return false
	}
	for _, c := range mods.Children() {
		if !c.IsNamed() && c.Text() == "public" {
			return true
		}
	}
	return false
}

// isInterfaceMember: membros de interface são públicos por padrão.
func isInterfaceMember(decl parser.Node) bool {
	return decl.Parent().Is("interface_body")
}
