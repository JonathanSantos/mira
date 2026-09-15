package typescript

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// collectImports registra imports ESM, re-exports e requires de topo.
func (x *extraction) collectImports(root parser.Node) {
	for _, stmt := range root.NamedChildren() {
		switch stmt.Type() {
		case "import_statement":
			x.importStatement(stmt)
		case "export_statement":
			x.reexport(stmt)
		case "lexical_declaration", "variable_declaration":
			for _, d := range stmt.ChildrenOf("variable_declarator") {
				x.require(d)
			}
		}
	}
}

func (x *extraction) importStatement(stmt parser.Node) {
	module := moduleOf(stmt.Child("string"))
	line := stmt.StartLine()
	if req := stmt.Child("import_require_clause"); !req.IsNil() {
		// `import fs = require('fs')` liga o module.exports inteiro.
		x.addImport(extract.Import{Kind: extract.ImportCJS, Module: moduleOf(req.Child("string")),
			ImportedName: "*", LocalName: req.Child("identifier").Text(), Line: line})
		return
	}
	clause := stmt.Child("import_clause")
	if clause.IsNil() {
		x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, Line: line})
		return
	}
	for _, c := range clause.NamedChildren() {
		switch c.Type() {
		case "identifier":
			x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, ImportedName: "default", LocalName: c.Text(), Line: line})
		case "namespace_import":
			x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, ImportedName: "*", LocalName: c.Child("identifier").Text(), Line: line})
		case "named_imports":
			for _, spec := range c.ChildrenOf("import_specifier") {
				name, local := specifierNames(spec)
				x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, ImportedName: name, LocalName: local, Line: spec.StartLine()})
			}
		}
	}
}

// reexport trata `export { a as b } from`, `export * from` e
// `export * as ns from`.
func (x *extraction) reexport(stmt parser.Node) {
	source := stmt.Child("string")
	if source.IsNil() {
		return
	}
	module := moduleOf(source)
	line := stmt.StartLine()
	if clause := stmt.Child("export_clause"); !clause.IsNil() {
		for _, spec := range clause.ChildrenOf("export_specifier") {
			name, exported := specifierNames(spec)
			x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, ImportedName: name, LocalName: exported, IsReexport: true, Line: spec.StartLine()})
			if name != "default" {
				// `export { a } from './m'` usa a de lá, como um import na mesma linha.
				x.result.Refs = append(x.result.Refs, extract.Ref{Name: name, Kind: extract.RefImport, Line: spec.StartLine(), Col: spec.StartCol(), Container: -1})
			}
		}
		return
	}
	if ns := stmt.Child("namespace_export"); !ns.IsNil() {
		x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, ImportedName: "*", LocalName: ns.Child("identifier").Text(), IsReexport: true, Line: line})
		return
	}
	x.addImport(extract.Import{Kind: extract.ImportESM, Module: module, ImportedName: "*", LocalName: "*", IsReexport: true, IsWildcard: true, Line: line})
}

// require trata `const x = require('./m')` e `const { a, b: c } = require('./m')`.
func (x *extraction) require(declarator parser.Node) {
	value := lastNamedChild(declarator)
	if !isRequireCall(value) {
		return
	}
	module := moduleOf(value.Child("arguments").Child("string"))
	line := declarator.StartLine()
	target := declarator.NamedChildren()[0]
	switch target.Type() {
	case "identifier":
		x.addImport(extract.Import{Kind: extract.ImportCJS, Module: module, ImportedName: "*", LocalName: target.Text(), Line: line})
	case "object_pattern":
		for _, prop := range target.NamedChildren() {
			switch prop.Type() {
			case "shorthand_property_identifier_pattern":
				x.addImport(extract.Import{Kind: extract.ImportCJS, Module: module, ImportedName: prop.Text(), LocalName: prop.Text(), Line: line})
			case "pair_pattern":
				key := prop.Child("property_identifier")
				local := lastNamedChild(prop)
				if !key.IsNil() && local.Is("identifier") {
					x.addImport(extract.Import{Kind: extract.ImportCJS, Module: module, ImportedName: key.Text(), LocalName: local.Text(), Line: line})
				}
			}
		}
	}
}

// specifierNames devolve (nome de origem, nome local/alias) de um
// import_specifier ou export_specifier.
func specifierNames(spec parser.Node) (string, string) {
	ids := spec.ChildrenOf("identifier", "string")
	if len(ids) == 0 {
		return "", ""
	}
	name := moduleOf(ids[0])
	if len(ids) > 1 {
		return name, moduleOf(ids[1])
	}
	return name, name
}

func (x *extraction) addImport(im extract.Import) {
	x.result.Imports = append(x.result.Imports, im)
	if im.LocalName != "" && !im.IsReexport {
		x.imported[im.LocalName] = true
	}
}
