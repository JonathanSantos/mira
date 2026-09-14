// Package typescript extrai símbolos, referências e imports de arquivos
// TypeScript/JavaScript (ESM e CommonJS). Toda a família é parseada com a
// gramática tsx, então há um único conjunto de tipos de nó.
package typescript

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// Extractor implementa extract.Extractor para TS/TSX/JS/JSX.
type Extractor struct{}

func New() *Extractor { return &Extractor{} }

// extraction é o estado de uma única extração; nasce e morre em Extract.
type extraction struct {
	result extract.Result
	// exports mapeia nome local -> nome exportado, vindo de `export { a as b }`,
	// `export default x` e `module.exports = { a }`.
	exports map[string]string
	// imported são os nomes locais introduzidos por imports/requires.
	imported map[string]bool
	// locals são nomes de parâmetros e variáveis de função: refs a eles não
	// são símbolos e ficam fora do índice de refs.
	locals map[string]bool
	// varTypes mapeia variável de topo e campo de classe -> tipo declarado.
	varTypes map[string]string
	// localTypes guarda o tipo (talvez vazio) de cada declaração dentro de
	// função, com o escopo dela: o mesmo nome em duas funções não se mistura.
	localTypes map[string][]localType
}

// localType é uma declaração dentro de função, visível em [start, end): o
// bloco para let/const, a função para var e parâmetros. pos desempata duas
// declarações no mesmo escopo.
type localType struct {
	start, end, pos int
	typ             string
}

func (e *Extractor) Extract(tree *parser.Tree) extract.Result {
	x := &extraction{
		exports:    map[string]string{},
		imported:   map[string]bool{},
		locals:     map[string]bool{},
		varTypes:   map[string]string{},
		localTypes: map[string][]localType{},
	}
	root := tree.Root()
	x.collectImports(root)
	x.collectSymbols(root, "")
	x.applyExports()
	x.collectDeclarations(root)
	x.collectRefs(root)
	x.returnHints(root)
	return x.result
}

// collectSymbols percorre as declarações de um bloco de módulo (program ou
// corpo de namespace) com o container dado.
func (x *extraction) collectSymbols(block parser.Node, container string) {
	for _, stmt := range block.NamedChildren() {
		switch stmt.Type() {
		case "export_statement":
			x.exportStatement(stmt, container)
		case "expression_statement":
			// Um namespace não exportado chega embrulhado em expression_statement.
			if mod := stmt.Child("internal_module"); !mod.IsNil() {
				x.namespace(mod, container, false, "", stmt)
				continue
			}
			x.commonJSExport(stmt)
		default:
			x.declaration(stmt, container, false, "", stmt)
		}
	}
}

// exportStatement trata `export <decl>`, `export default <expr>`,
// `export { a as b }` e `export = x`. Re-exports com `from` são imports.
func (x *extraction) exportStatement(stmt parser.Node, container string) {
	isDefault := !stmt.AnonChild("default").IsNil()
	if decl := stmt.Child(declarationTypes...); !decl.IsNil() {
		exportName := ""
		if isDefault {
			exportName = "default"
		}
		x.declaration(decl, container, true, exportName, stmt)
		return
	}
	if !stmt.Child("string").IsNil() {
		return // re-export: já registrado como import
	}
	if clause := stmt.Child("export_clause"); !clause.IsNil() {
		for _, spec := range clause.ChildrenOf("export_specifier") {
			ids := spec.ChildrenOf("identifier")
			if len(ids) == 0 {
				continue
			}
			local, exported := ids[0].Text(), ids[0].Text()
			if len(ids) > 1 {
				exported = ids[1].Text()
			}
			x.exports[local] = exported
			x.reexportImport(local, exported, stmt.StartLine())
		}
		return
	}
	// `export default <expr>` e `export = <expr>` (interop CommonJS). Outras
	// formas (`export function f(): T;`, assinatura de sobrecarga) não
	// exportam um valor aqui.
	value := lastNamedChild(stmt)
	if value.IsNil() || !isDefault && stmt.AnonChild("=").IsNil() {
		return
	}
	switch value.Type() {
	case "identifier":
		x.exports[value.Text()] = "default"
		x.reexportImport(value.Text(), "default", stmt.StartLine())
	case "arrow_function", "function_expression", "class", "function":
		x.anonymousDefault(value, stmt)
	case "function_signature":
		// `export default function f(): T;`: sobrecarga; a implementação é o símbolo.
	default:
		x.defaultValue(value, stmt)
	}
}

// reexportImport trata `import { atom } from 'jotai'; export { atom }`: o nome
// não é um símbolo daqui, então vira um re-export do módulo de origem, e quem
// importa atom deste arquivo chega ao pacote (external) ou ao arquivo certo.
func (x *extraction) reexportImport(local, exported string, line int) {
	for _, im := range x.result.Imports {
		if im.IsReexport || im.LocalName != local {
			continue
		}
		x.result.Imports = append(x.result.Imports, extract.Import{Kind: im.Kind, Module: im.Module,
			ImportedName: im.ImportedName, LocalName: exported, IsReexport: true, Line: line})
		return
	}
}

// defaultValue cria o símbolo "default" para `export default <expressão>`
// (`React.memo(Canvas)`, `{ Row, Col }`): sem ele, `import Canvas from
// './Canvas'` nunca resolve. O indexer dá a ele o nome do módulo.
func (x *extraction) defaultValue(value, stmt parser.Node) {
	stop := parser.Node{}
	if value.Is("object", "array") {
		stop = value // o literal inteiro não cabe numa assinatura
	}
	x.add(extract.Symbol{
		Name:       "default",
		Kind:       extract.KindVariable,
		Signature:  extract.Trim(extract.SignatureUntil(stmt, stop)),
		Exported:   true,
		ExportName: "default",
		StartLine:  stmt.StartLine(),
		EndLine:    stmt.EndLine(),
		StartByte:  stmt.StartByte(),
		EndByte:    stmt.EndByte(),
		NameLine:   stmt.StartLine(),
	})
}

// anonymousDefault cria o símbolo "default" para `export default () => …`.
func (x *extraction) anonymousDefault(value, stmt parser.Node) {
	kind := extract.KindFunction
	if value.Is("class") {
		kind = extract.KindClass
	}
	body := functionBody(value)
	x.add(extract.Symbol{
		Name:       "default",
		Kind:       kind,
		Signature:  extract.Trim(extract.SignatureUntil(stmt, body)),
		Exported:   true,
		ExportName: "default",
		JSX:        kind == extract.KindFunction && returnsJSX(value),
		StartLine:  stmt.StartLine(),
		EndLine:    stmt.EndLine(),
		StartByte:  stmt.StartByte(),
		EndByte:    stmt.EndByte(),
		NameLine:   stmt.StartLine(),
	})
	// As closures de `export default async (…) => { const helper = … }` são
	// símbolos `default.helper`; o indexer troca "default" pelo nome do módulo.
	if kind == extract.KindFunction && body.Is("statement_block") {
		x.nestedFunctions(body, "default")
	}
}

var declarationTypes = []string{
	"function_declaration", "generator_function_declaration",
	"class_declaration", "abstract_class_declaration",
	"lexical_declaration", "variable_declaration",
	"interface_declaration", "type_alias_declaration", "enum_declaration",
	"internal_module", "module", "ambient_declaration",
}

// declaration registra o símbolo de uma declaração. `span` é o nó cujo
// range vira o range do símbolo (o export_statement, quando há, para
// incluir decorators e a keyword export).
func (x *extraction) declaration(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	switch decl.Type() {
	case "function_declaration", "generator_function_declaration":
		x.function(decl, container, exported, exportName, span)
	case "class_declaration", "abstract_class_declaration":
		x.class(decl, container, exported, exportName, span)
	case "lexical_declaration", "variable_declaration":
		x.variables(decl, container, exported, exportName, span)
	case "interface_declaration":
		x.interfaceDecl(decl, container, exported, exportName, span)
	case "type_alias_declaration", "enum_declaration":
		x.typeLike(decl, container, exported, exportName, span)
	case "internal_module", "module":
		x.namespace(decl, container, exported, exportName, span)
	case "ambient_declaration":
		// `declare class G {}`, `export declare const V: string`: declaram o que
		// existe em runtime, com os mesmos símbolos da declaração comum.
		if inner := decl.Child(declarationTypes...); !inner.IsNil() {
			x.declaration(inner, container, exported, exportName, span)
		}
	}
}

func (x *extraction) function(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	name := decl.Child("identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("statement_block")
	x.add(extract.Symbol{
		Name:       name.Text(),
		Kind:       extract.KindFunction,
		Container:  container,
		Signature:  prefixed(exported, exportName, extract.Trim(extract.SignatureUntil(decl, body))),
		Exported:   exported,
		ExportName: exportNameOr(exportName, name.Text()),
		JSX:        returnsJSX(decl),
		StartLine:  span.StartLine(),
		EndLine:    span.EndLine(),
		StartByte:  span.StartByte(),
		EndByte:    span.EndByte(),
		NameLine:   name.StartLine(),
	})
	x.nestedFunctions(body, name.Text())
}

// nestedFunctions registra funções declaradas dentro do corpo de outra
// (`function inner() {}` ou `const inner = () => {}`) com container = a
// função externa. É o padrão factory-com-closures (createFormControl,
// hooks) em que a API pública nasce dentro de uma função.
func (x *extraction) nestedFunctions(body parser.Node, container string) {
	if body.IsNil() {
		return
	}
	for _, stmt := range body.NamedChildren() {
		switch stmt.Type() {
		case "function_declaration", "generator_function_declaration":
			x.function(stmt, container, false, "", stmt)
		case "lexical_declaration", "variable_declaration":
			for _, d := range stmt.ChildrenOf("variable_declarator") {
				x.nestedArrow(d, stmt, container)
			}
		}
	}
}

func (x *extraction) nestedArrow(d, stmt parser.Node, container string) {
	name := declaratorName(d)
	value := lastNamedChild(d)
	if name.IsNil() || !value.Is("arrow_function", "function_expression") {
		return
	}
	body := functionBody(value)
	x.add(extract.Symbol{
		Name:          name.Text(),
		QualifiedName: container + "." + name.Text(),
		Kind:          extract.KindFunction,
		Container:     container,
		Signature:     extract.Trim(extract.SignatureUntil(stmt, body)),
		JSX:           returnsJSX(value),
		StartLine:     stmt.StartLine(),
		EndLine:       stmt.EndLine(),
		StartByte:     stmt.StartByte(),
		EndByte:       stmt.EndByte(),
		NameLine:      name.StartLine(),
	})
	if body.Is("statement_block") {
		x.nestedFunctions(body, name.Text())
	}
}

func (x *extraction) class(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	name := decl.Child("type_identifier", "identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("class_body")
	x.add(extract.Symbol{
		Name:        name.Text(),
		Kind:        extract.KindClass,
		Container:   container,
		Signature:   prefixed(exported, exportName, extract.Trim(signatureAfterDecorators(decl, body))),
		Exported:    exported,
		ExportName:  exportNameOr(exportName, name.Text()),
		StartLine:   span.StartLine(),
		EndLine:     span.EndLine(),
		StartByte:   span.StartByte(),
		EndByte:     span.EndByte(),
		NameLine:    name.StartLine(),
		Annotations: decoratorTexts(classDecorators(decl, span)),
	})
	if body.IsNil() {
		return
	}
	x.classMembers(body, name.Text())
}

// classMembers registra métodos, construtor e campos. Decorators são
// irmãos anteriores do membro no class_body e entram no range do símbolo.
func (x *extraction) classMembers(body parser.Node, className string) {
	var pendingDecorators []parser.Node
	for _, member := range body.NamedChildren() {
		if member.Is("decorator") {
			pendingDecorators = append(pendingDecorators, member)
			continue
		}
		start := member
		if len(pendingDecorators) > 0 {
			start = pendingDecorators[0]
		}
		annotations := decoratorTexts(pendingDecorators)
		pendingDecorators = nil
		switch member.Type() {
		case "method_definition", "method_signature", "abstract_method_signature":
			x.method(member, className, start, annotations)
		case "public_field_definition", "field_definition", "property_signature":
			x.field(member, className, start, annotations)
		}
	}
}

func (x *extraction) method(member parser.Node, className string, start parser.Node, annotations []string) {
	name := member.Child("property_identifier", "private_property_identifier")
	if name.IsNil() {
		return
	}
	kind := extract.KindMethod
	if name.Text() == "constructor" {
		kind = extract.KindConstructor
	}
	body := member.Child("statement_block")
	x.add(extract.Symbol{
		Name:          name.Text(),
		QualifiedName: className + "." + name.Text(),
		Kind:          kind,
		Container:     className,
		Signature:     extract.Trim(extract.SignatureUntil(member, body)),
		Exported:      !hasAccessModifier(member, "private", "protected"),
		JSX:           returnsJSX(member),
		StartLine:     start.StartLine(),
		EndLine:       member.EndLine(),
		StartByte:     start.StartByte(),
		EndByte:       member.EndByte(),
		NameLine:      name.StartLine(),
		Annotations:   annotations,
	})
	x.nestedFunctions(body, name.Text())
}

// field registra um campo de classe; um campo com arrow function é
// funcionalmente um método (padrão comum em React/NestJS).
func (x *extraction) field(member parser.Node, className string, start parser.Node, annotations []string) {
	name := member.Child("property_identifier", "private_property_identifier")
	if name.IsNil() {
		return
	}
	value := lastNamedChild(member)
	kind := extract.KindProperty
	stop := member.AnonChild("=")
	if value.Is("arrow_function", "function_expression") {
		kind = extract.KindMethod
		stop = functionBody(value)
	}
	x.add(extract.Symbol{
		Name:          name.Text(),
		QualifiedName: className + "." + name.Text(),
		Kind:          kind,
		Container:     className,
		Signature:     extract.Trim(extract.SignatureUntil(member, stop)),
		Exported:      !hasAccessModifier(member, "private", "protected"),
		JSX:           kind == extract.KindMethod && returnsJSX(value),
		StartLine:     start.StartLine(),
		EndLine:       member.EndLine(),
		StartByte:     start.StartByte(),
		EndByte:       member.EndByte(),
		NameLine:      name.StartLine(),
		Annotations:   annotations,
	})
}

// variables trata `const a = …, b = …`. Arrow functions viram function;
// `require(...)` é import e não símbolo.
func (x *extraction) variables(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	declarators := decl.ChildrenOf("variable_declarator")
	for _, d := range declarators {
		// O nome é o primeiro filho: em `const { a } = obj`, Child("identifier")
		// acharia o valor obj.
		name := declaratorName(d)
		if name.IsNil() {
			x.destructured(decl, d, container, exported, exportName, span, len(declarators) == 1)
			continue
		}
		value := lastNamedChild(d)
		if value.Is("identifier") && value.StartByte() == name.StartByte() {
			value = parser.Node{}
		}
		if isRequireCall(value) {
			continue
		}
		kind := extract.KindVariable
		stop := d.AnonChild("=")
		if value.Is("arrow_function", "function_expression") {
			kind = extract.KindFunction
			stop = functionBody(value)
		}
		// Com um único declarador o range é a declaração toda (inclui
		// `export const`); com vários, só o declarador.
		spanNode := d
		if len(declarators) == 1 {
			spanNode = span
		}
		sig := extract.SignatureUntil(decl, stop)
		if len(declarators) > 1 {
			sig = keywordOf(decl) + " " + extract.SignatureUntil(d, stop)
		}
		x.add(extract.Symbol{
			Name:       name.Text(),
			Kind:       kind,
			Container:  container,
			Signature:  prefixed(exported, exportName, extract.Trim(sig)),
			Exported:   exported,
			ExportName: exportNameOr(exportName, name.Text()),
			JSX:        kind == extract.KindFunction && returnsJSX(value),
			StartLine:  spanNode.StartLine(),
			EndLine:    spanNode.EndLine(),
			StartByte:  spanNode.StartByte(),
			EndByte:    spanNode.EndByte(),
			NameLine:   name.StartLine(),
		})
		if kind == extract.KindFunction && stop.Is("statement_block") {
			x.nestedFunctions(stop, name.Text())
		}
	}
}

// destructured cria uma variável para cada nome de `const { a, b: c } = obj`
// e `const [x, y] = pair`: `export const { useAtom } = jotai` exporta
// useAtom, e sem o símbolo quem o importa nunca resolve. Desestruturar um
// require já é import.
func (x *extraction) destructured(decl, d parser.Node, container string, exported bool, exportName string, span parser.Node, single bool) {
	pattern := d.Child("object_pattern", "array_pattern")
	if pattern.IsNil() || isRequireCall(lastNamedChild(d)) {
		return
	}
	stop := d.AnonChild("=")
	sig, spanNode := extract.SignatureUntil(decl, stop), span
	if !single {
		sig, spanNode = keywordOf(decl)+" "+extract.SignatureUntil(d, stop), d
	}
	for _, name := range boundNames(pattern) {
		x.add(extract.Symbol{
			Name:       name.Text(),
			Kind:       extract.KindVariable,
			Container:  container,
			Signature:  prefixed(exported, exportName, extract.Trim(sig)),
			Exported:   exported,
			ExportName: exportNameOr(exportName, name.Text()),
			StartLine:  spanNode.StartLine(),
			EndLine:    spanNode.EndLine(),
			StartByte:  spanNode.StartByte(),
			EndByte:    spanNode.EndByte(),
			NameLine:   name.StartLine(),
		})
	}
}

// declaratorName é o identificador declarado por `const a = …`; zero quando o
// declarador é uma desestruturação.
func declaratorName(d parser.Node) parser.Node {
	if kids := d.NamedChildren(); len(kids) > 0 && kids[0].Is("identifier") {
		return kids[0]
	}
	return parser.Node{}
}

// boundNames são os identificadores que um padrão declara: sem as chaves de
// `b: c`, sem os valores padrão de `a = 1`, com o resto de `...rest`.
func boundNames(n parser.Node) []parser.Node {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		return []parser.Node{n}
	case "pair_pattern", "rest_pattern":
		return boundNames(lastNamedChild(n))
	case "assignment_pattern", "object_assignment_pattern":
		if kids := n.NamedChildren(); len(kids) > 0 {
			return boundNames(kids[0])
		}
	case "object_pattern", "array_pattern":
		var out []parser.Node
		for _, c := range n.NamedChildren() {
			out = append(out, boundNames(c)...)
		}
		return out
	}
	return nil
}

func (x *extraction) interfaceDecl(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	name := decl.Child("type_identifier", "identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("interface_body", "object_type")
	x.add(extract.Symbol{
		Name:       name.Text(),
		Kind:       extract.KindInterface,
		Container:  container,
		Signature:  prefixed(exported, exportName, extract.Trim(extract.SignatureUntil(decl, body))),
		Exported:   exported,
		ExportName: exportNameOr(exportName, name.Text()),
		StartLine:  span.StartLine(),
		EndLine:    span.EndLine(),
		StartByte:  span.StartByte(),
		EndByte:    span.EndByte(),
		NameLine:   name.StartLine(),
	})
	if body.IsNil() {
		return
	}
	x.typeMembers(body, name.Text())
}

// typeMembers registra os membros de um corpo de interface ou de um tipo
// objeto (`{ a: T; m(): void }`) sob o container. Propriedades são símbolos:
// é por elas que uma cadeia `order.items.reduce` descobre o tipo de `items`.
// Uma propriedade cujo tipo é outro objeto literal (`customData?: { data: X }`)
// tem os membros dele sob `Container.propriedade`, onde o resolvedor os procura.
func (x *extraction) typeMembers(body parser.Node, container string) {
	for _, member := range body.ChildrenOf("method_signature") {
		x.method(member, container, member, nil)
	}
	for _, member := range body.ChildrenOf("property_signature") {
		x.field(member, container, member, nil)
		name := member.Child("property_identifier", "private_property_identifier")
		if name.IsNil() {
			continue
		}
		for _, nested := range objectTypes(member.Child("type_annotation")) {
			x.typeMembers(nested, container+"."+name.Text())
		}
	}
}

// objectTypes acha os objetos literais que dão membros a um tipo: o próprio
// `{…}`, os lados de `A & {…}` e `{…} | null`, e os argumentos de genéricos
// como `Readonly<{…}>`. Funções, arrays e condicionais ficam de fora: os
// membros deles não são do tipo declarado.
func objectTypes(n parser.Node) []parser.Node {
	switch n.Type() {
	case "object_type":
		return []parser.Node{n}
	case "type_annotation", "parenthesized_type", "intersection_type", "union_type", "generic_type", "type_arguments":
		var out []parser.Node
		for _, child := range n.NamedChildren() {
			out = append(out, objectTypes(child)...)
		}
		return out
	}
	return nil
}

func (x *extraction) typeLike(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	name := decl.Child("type_identifier", "identifier")
	if name.IsNil() {
		return
	}
	kind := extract.KindType
	stop := decl.AnonChild("=")
	if decl.Is("enum_declaration") {
		kind = extract.KindEnum
		stop = decl.Child("enum_body")
	}
	x.add(extract.Symbol{
		Name:       name.Text(),
		Kind:       kind,
		Container:  container,
		Signature:  prefixed(exported, exportName, extract.Trim(extract.SignatureUntil(decl, stop))),
		Exported:   exported,
		ExportName: exportNameOr(exportName, name.Text()),
		StartLine:  span.StartLine(),
		EndLine:    span.EndLine(),
		StartByte:  span.StartByte(),
		EndByte:    span.EndByte(),
		NameLine:   name.StartLine(),
	})
	if decl.Is("type_alias_declaration") {
		// `type Props = Base & { onClose(): void }`: os membros do objeto são do alias.
		for _, body := range objectTypes(lastNamedChild(decl)) {
			x.typeMembers(body, name.Text())
		}
	}
}

func (x *extraction) namespace(decl parser.Node, container string, exported bool, exportName string, span parser.Node) {
	name := decl.Child("identifier", "nested_identifier")
	if name.IsNil() {
		return
	}
	body := decl.Child("statement_block")
	x.add(extract.Symbol{
		Name:       name.Text(),
		Kind:       extract.KindNamespace,
		Container:  container,
		Signature:  prefixed(exported, exportName, extract.Trim(extract.SignatureUntil(decl, body))),
		Exported:   exported,
		ExportName: exportNameOr(exportName, name.Text()),
		StartLine:  span.StartLine(),
		EndLine:    span.EndLine(),
		StartByte:  span.StartByte(),
		EndByte:    span.EndByte(),
		NameLine:   name.StartLine(),
	})
	if !body.IsNil() {
		x.collectSymbols(body, name.Text())
	}
}

// commonJSExport trata `module.exports = …`, `module.exports.x = …` e
// `exports.x = …`.
func (x *extraction) commonJSExport(stmt parser.Node) {
	assign := stmt.Child("assignment_expression")
	if assign.IsNil() {
		return
	}
	left := assign.NamedChildren()[0]
	right := lastNamedChild(assign)
	leftText := left.Text()
	switch {
	case leftText == "module.exports":
		x.commonJSExportValue("default", right, stmt)
	case strings.HasPrefix(leftText, "module.exports.") || strings.HasPrefix(leftText, "exports."):
		name := leftText[strings.LastIndex(leftText, ".")+1:]
		x.commonJSExportValue(name, right, stmt)
	}
}

// commonJSExportValue liga um nome exportado ao que está do lado direito:
// identificador (alias), objeto literal (vários exports) ou função (símbolo
// novo).
func (x *extraction) commonJSExportValue(exportName string, value, stmt parser.Node) {
	switch value.Type() {
	case "identifier":
		x.exports[value.Text()] = exportName
	case "object":
		x.commonJSObject(value)
	case "arrow_function", "function_expression", "function", "class":
		x.functionSymbol(exportName, exportName, value, stmt)
	}
}

func (x *extraction) commonJSObject(obj parser.Node) {
	for _, prop := range obj.NamedChildren() {
		switch prop.Type() {
		case "shorthand_property_identifier":
			x.exports[prop.Text()] = prop.Text()
		case "pair":
			key := prop.Child("property_identifier", "string")
			value := lastNamedChild(prop)
			if key.IsNil() || value.IsNil() {
				continue
			}
			name := strings.Trim(key.Text(), `"'`)
			if value.Is("identifier") {
				x.exports[value.Text()] = name
				continue
			}
			if value.Is("arrow_function", "function_expression") {
				x.functionSymbol(name, name, value, prop)
			}
		case "method_definition":
			name := prop.Child("property_identifier")
			if !name.IsNil() {
				x.functionSymbol(name.Text(), name.Text(), prop, prop)
			}
		}
	}
}

// functionSymbol registra uma função anônima que ganhou nome pelo export.
func (x *extraction) functionSymbol(name, exportName string, fn, span parser.Node) {
	body := fn.Child("statement_block")
	x.add(extract.Symbol{
		Name:       name,
		Kind:       extract.KindFunction,
		Signature:  extract.Trim(extract.SignatureUntil(span, body)),
		Exported:   true,
		ExportName: exportName,
		JSX:        returnsJSX(fn),
		StartLine:  span.StartLine(),
		EndLine:    span.EndLine(),
		StartByte:  span.StartByte(),
		EndByte:    span.EndByte(),
		NameLine:   span.StartLine(),
	})
}

// applyExports aplica `export { a as b }` / `module.exports = { a }` aos
// símbolos de topo já coletados.
func (x *extraction) applyExports() {
	for i := range x.result.Symbols {
		s := &x.result.Symbols[i]
		if s.Container != "" {
			continue
		}
		exportName, ok := x.exports[s.Name]
		if !ok || (s.Exported && s.ExportName == "default") {
			continue
		}
		s.Exported = true
		s.ExportName = exportName
	}
}

func (x *extraction) add(s extract.Symbol) {
	if s.QualifiedName == "" {
		s.QualifiedName = s.Name
		if s.Container != "" {
			s.QualifiedName = s.Container + "." + s.Name
		}
	}
	if !s.Exported {
		s.ExportName = ""
	}
	x.result.Symbols = append(x.result.Symbols, s)
}
