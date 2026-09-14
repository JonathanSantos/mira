package java

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// collectImports trata `import a.b.C`, `import static a.b.C.m`,
// `import a.b.*` e `import static a.b.C.*`.
func (x *extraction) collectImports(root parser.Node) {
	for _, decl := range root.ChildrenOf("import_declaration") {
		path := decl.Child("scoped_identifier", "identifier")
		if path.IsNil() {
			continue
		}
		kind := extract.ImportJava
		if !decl.AnonChild("static").IsNil() {
			kind = extract.ImportJavaStatic
		}
		full := path.Text()
		if !decl.Child("asterisk").IsNil() {
			x.result.Imports = append(x.result.Imports, extract.Import{
				Kind: kind, Module: full, ImportedName: "*", LocalName: "*", IsWildcard: true, Line: decl.StartLine(),
			})
			continue
		}
		dot := strings.LastIndex(full, ".")
		name := full[dot+1:]
		x.result.Imports = append(x.result.Imports, extract.Import{
			Kind: kind, Module: full, ImportedName: name, LocalName: name, Line: decl.StartLine(),
		})
		x.result.Refs = append(x.result.Refs, extract.Ref{
			Name: name, Kind: extract.RefImport, Line: decl.StartLine(), Col: 1, Container: -1,
		})
	}
}
