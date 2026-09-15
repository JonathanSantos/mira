package resolve_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/store"
)

// gap é uma causa de perda de recall medida no benchmark
// (benchmark/results/recall-*.md), reduzida a um exemplo mínimo. Enquanto
// aberta, o teste só exige que o uso não aponte para a definição errada: a
// precisão não se negocia. Quando o resolvedor passar a achar o alvo, o teste
// falha pedindo closed: true, e daí em diante protege o ganho. Sobrecargas
// ficam de fora: ali o Mira e o gabarito escolhem declarações diferentes do
// mesmo conjunto, e não há alvo errado a proteger.
type gap struct {
	cause      string
	files      map[string]string
	file       string
	line       int
	name       string
	target     string // qualified_name da definição certa
	targetFile string
	closed     bool
}

var goMod = "module example.com/app\n\ngo 1.22\n"

var recallGaps = []gap{
	{
		cause: "ts receiver without type: member chain through an indexed access type",
		files: map[string]string{
			"src/scene.ts": "export class Scene {\n  getNonDeletedElements() {\n    return [];\n  }\n}\n",
			"src/app.ts":   "import { Scene } from './scene';\nexport class App {\n  scene: Scene = new Scene();\n}\n",
			"src/types.ts": "import type { App } from './app';\nexport type AppClassProperties = {\n  scene: App[\"scene\"];\n};\n",
			"src/lasso.ts": "import type { AppClassProperties } from './types';\nexport class Lasso {\n  constructor(private app: AppClassProperties) {}\n  run() {\n    return this.app.scene.getNonDeletedElements();\n  }\n}\n",
		},
		file: "src/lasso.ts", line: 5, name: "getNonDeletedElements", target: "Scene.getNonDeletedElements", targetFile: "src/scene.ts",
	},
	{
		cause: "ts receiver without type: destructuring",
		files: map[string]string{
			"src/types.ts":         "export type Field = {\n  onChange(value: unknown): void;\n};\nexport type ControllerReturn = {\n  field: Field;\n};\n",
			"src/useController.ts": "import type { ControllerReturn } from './types';\nexport function useController(): ControllerReturn {\n  return null as unknown as ControllerReturn;\n}\n",
			"src/use.ts":           "import { useController } from './useController';\nexport function Input() {\n  const { field } = useController();\n  field.onChange(1);\n}\n",
		},
		file: "src/use.ts", line: 4, name: "onChange", target: "Field.onChange", targetFile: "src/types.ts",
	},
	{
		cause: "ts receiver without type: untyped callback parameter",
		files: map[string]string{
			"src/item.ts": "export class Item {\n  run() {}\n}\n",
			"src/use.ts":  "import { Item } from './item';\nexport function all(items: Item[]) {\n  items.forEach((item) => item.run());\n}\n",
		},
		file: "src/use.ts", line: 3, name: "run", target: "Item.run", targetFile: "src/item.ts",
	},
	{
		cause: "ts bare name: function declared inside a test callback",
		files: map[string]string{
			"src/form.test.tsx": "it('renders', () => {\n  function Input() {\n    return null;\n  }\n  return <Input />;\n});\n",
		},
		file: "src/form.test.tsx", line: 5, name: "Input", target: "Input", targetFile: "src/form.test.tsx",
	},
	{
		cause: "ts not extracted: member after optional chaining",
		files: map[string]string{
			"src/values.ts": "export type Values = {\n  content: { children: number[] };\n};\nexport function first(v?: Values) {\n  return v?.content.children[0];\n}\n",
		},
		file: "src/values.ts", line: 5, name: "content", target: "Values.content", targetFile: "src/values.ts",
	},
	{
		cause: "go ambiguous: same function in files with opposite build tags",
		files: map[string]string{
			"go.mod":                       goMod,
			"binding/binding.go":           "//go:build !nomsgpack\n\npackage binding\n\nfunc validate(obj any) error { return nil }\n",
			"binding/binding_nomsgpack.go": "//go:build nomsgpack\n\npackage binding\n\nfunc validate(obj any) error { return nil }\n",
			"binding/form.go":              "package binding\n\nfunc Bind(obj any) error {\n\treturn validate(obj)\n}\n",
		},
		file: "binding/form.go", line: 4, name: "validate", target: "validate", targetFile: "binding/binding.go",
	},
	{
		cause: "go member not found: variable declared in files with opposite build tags",
		files: map[string]string{
			"go.mod":                       goMod,
			"binding/binding.go":           "//go:build !nomsgpack\n\npackage binding\n\nvar Uri = uriBinding{}\n",
			"binding/binding_nomsgpack.go": "//go:build nomsgpack\n\npackage binding\n\nvar Uri = uriBinding{}\n",
			"binding/uri.go":               "package binding\n\ntype uriBinding struct{}\n\nfunc (uriBinding) BindUri(m map[string][]string, obj any) error { return nil }\n",
			"binding/use.go":               "package binding\n\nfunc F() error {\n\tb := Uri\n\treturn b.BindUri(nil, nil)\n}\n",
		},
		file: "binding/use.go", line: 5, name: "BindUri", target: "uriBinding.BindUri", targetFile: "binding/uri.go",
	},
	{
		cause: "go marked external: call of a variable built by a generic function",
		files: map[string]string{
			"go.mod":       goMod,
			"engine.go":    "package app\n\ntype Engine struct{}\n\nfunc (e *Engine) Use() {}\n",
			"ginS/gins.go": "package ginS\n\nimport (\n\t\"sync\"\n\n\t\"example.com/app\"\n)\n\nvar engine = sync.OnceValue(func() *app.Engine { return nil })\n\nfunc Use() {\n\tengine().Use()\n}\n",
		},
		file: "ginS/gins.go", line: 12, name: "Use", target: "Engine.Use", targetFile: "engine.go",
	},
	{
		cause: "python bare name: import inside a function",
		files: map[string]string{
			"pkg/__init__.py":   "",
			"pkg/helpers.py":    "def explain(app):\n    return app\n",
			"pkg/templating.py": "def render(app):\n    from .helpers import explain\n    explain(app)\n",
		},
		file: "pkg/templating.py", line: 3, name: "explain", target: "explain", targetFile: "pkg/helpers.py",
	},
	{
		cause: "python not extracted: function object used as a receiver",
		files: map[string]string{
			"pkg/__init__.py": "",
			"pkg/cli.py":      "def run_command():\n    pass\n\n\nrun_command.params = []\n",
		},
		file: "pkg/cli.py", line: 5, name: "run_command", target: "run_command", targetFile: "pkg/cli.py",
	},
	{
		cause: "java not extracted: field used as a receiver",
		files: map[string]string{
			"src/app/OwnerRepository.java": "package app;\n\npublic interface OwnerRepository {\n    Object findById(int id);\n}\n",
			"src/app/OwnerController.java": "package app;\n\nclass OwnerController {\n    private final OwnerRepository owners;\n\n    OwnerController(OwnerRepository owners) {\n        this.owners = owners;\n    }\n\n    Object show(int id) {\n        return owners.findById(id);\n    }\n}\n",
		},
		file: "src/app/OwnerController.java", line: 11, name: "owners", target: "app.OwnerController.owners", targetFile: "src/app/OwnerController.java",
	},
	{
		cause: "java receiver without type: field of the enclosing class used in an inner class",
		files: map[string]string{
			"src/app/NamedEntity.java":       "package app;\n\npublic class NamedEntity {\n    public void setName(String name) {}\n}\n",
			"src/app/Pet.java":               "package app;\n\npublic class Pet extends NamedEntity {}\n",
			"src/app/PetValidatorTests.java": "package app;\n\nclass PetValidatorTests {\n    private Pet pet;\n\n    class ValidateHasErrors {\n        void validate() {\n            pet.setName(\"x\");\n        }\n    }\n}\n",
		},
		file: "src/app/PetValidatorTests.java", line: 8, name: "setName", target: "app.NamedEntity.setName", targetFile: "src/app/NamedEntity.java",
	},
}

func TestRecallGaps(t *testing.T) {
	for _, g := range recallGaps {
		t.Run(g.cause, func(t *testing.T) {
			st := index(t, g.files)
			refs := refsAt(t, st, g.file, g.line, g.name)
			found := false
			for _, r := range refs {
				if r.ResolvedSymbolID == nil {
					continue
				}
				sym, ok, err := st.SymbolByID(*r.ResolvedSymbolID)
				require.NoError(t, err)
				require.True(t, ok)
				f, _, err := st.FileByID(sym.FileID)
				require.NoError(t, err)
				require.Equal(t, g.target+" in "+g.targetFile, sym.QualifiedName+" in "+f.Path, "a resolved use must point to the right definition")
				found = true
			}
			t.Logf("%s:%d %s -> %s", g.file, g.line, g.name, describe(refs))
			if g.closed {
				assert.True(t, found, "closed gap reopened: %s at %s:%d no longer resolves", g.name, g.file, g.line)
			} else {
				assert.False(t, found, "gap closed: %s at %s:%d now resolves; set closed: true", g.name, g.file, g.line)
			}
		})
	}
}

// refsAt são as refs com esse nome numa linha do arquivo.
func refsAt(t *testing.T, st *store.Store, file string, line int, name string) []store.Ref {
	t.Helper()
	f, ok, err := st.FileByPath(file)
	require.NoError(t, err)
	require.True(t, ok, "file %s not indexed", file)
	all, err := st.RefsOfFile(f.ID)
	require.NoError(t, err)
	var out []store.Ref
	for _, r := range all {
		if r.Line == line && r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func describe(refs []store.Ref) string {
	if len(refs) == 0 {
		return "not extracted"
	}
	var out []string
	for _, r := range refs {
		out = append(out, fmt.Sprintf("%s %s (receiver %q)", r.Kind, r.Resolution, r.Receiver))
	}
	return fmt.Sprint(out)
}
