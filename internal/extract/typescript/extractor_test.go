package typescript

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

func run(t *testing.T, l lang.Lang, src string) extract.Result {
	t.Helper()
	tree, err := parser.New().Parse(l, []byte(src))
	require.NoError(t, err)
	require.False(t, tree.HasError, "parse error in fixture")
	return New().Extract(tree)
}

func symbol(t *testing.T, res extract.Result, name string) extract.Symbol {
	t.Helper()
	for _, s := range res.Symbols {
		if s.Name == name {
			return s
		}
	}
	require.Failf(t, "symbol not found", "%s in %+v", name, res.Symbols)
	return extract.Symbol{}
}

func refsOf(res extract.Result, name string) []extract.Ref {
	var out []extract.Ref
	for _, r := range res.Refs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func TestSymbols(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want extract.Symbol
	}{
		{
			name: "exported function",
			src:  "export function formatMoney(v: number): string {\n  return `$${v}`;\n}\n",
			want: extract.Symbol{Name: "formatMoney", QualifiedName: "formatMoney", Kind: "function",
				Signature: "export function formatMoney(v: number): string", Exported: true, ExportName: "formatMoney",
				StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 68, NameLine: 1},
		},
		{
			name: "arrow function assigned to const",
			src:  "export const useCart = (id: string) => {\n  return id;\n};\n",
			want: extract.Symbol{Name: "useCart", QualifiedName: "useCart", Kind: "function",
				Signature: "export const useCart = (id: string) =>", Exported: true, ExportName: "useCart",
				StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 56, NameLine: 1},
		},
		{
			name: "react component gets jsx flag",
			src:  "export default function Home() {\n  return <div />;\n}\n",
			want: extract.Symbol{Name: "Home", QualifiedName: "Home", Kind: "function",
				Signature: "export default function Home()", Exported: true, ExportName: "default", JSX: true,
				StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 52, NameLine: 1},
		},
		{
			name: "class with decorator keeps clean signature and full range",
			src:  "@Injectable()\nexport class OrderService extends Base implements Svc {\n}\n",
			want: extract.Symbol{Name: "OrderService", QualifiedName: "OrderService", Kind: "class",
				Signature: "export class OrderService extends Base implements Svc", Exported: true, ExportName: "OrderService",
				StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 71, NameLine: 2, Annotations: []string{"@Injectable()"}},
		},
		{
			name: "non exported decorated class",
			src:  "@Injectable()\nclass OrderService {\n}\n",
			want: extract.Symbol{Name: "OrderService", QualifiedName: "OrderService", Kind: "class",
				Signature: "class OrderService", ExportName: "", StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 36, NameLine: 2, Annotations: []string{"@Injectable()"}},
		},
		{
			name: "interface",
			src:  "export interface Item { price: number }\n",
			want: extract.Symbol{Name: "Item", QualifiedName: "Item", Kind: "interface", Signature: "export interface Item",
				Exported: true, ExportName: "Item", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 39, NameLine: 1},
		},
		{
			name: "type alias",
			src:  "type Money = number;\n",
			want: extract.Symbol{Name: "Money", QualifiedName: "Money", Kind: "type", Signature: "type Money",
				StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 20, NameLine: 1},
		},
		{
			name: "enum",
			src:  "export enum Color { Red, Green }\n",
			want: extract.Symbol{Name: "Color", QualifiedName: "Color", Kind: "enum", Signature: "export enum Color",
				Exported: true, ExportName: "Color", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 32, NameLine: 1},
		},
		{
			name: "exported const variable",
			src:  "export const TAX_RATE: number = 0.2;\n",
			want: extract.Symbol{Name: "TAX_RATE", QualifiedName: "TAX_RATE", Kind: "variable", Signature: "export const TAX_RATE: number",
				Exported: true, ExportName: "TAX_RATE", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 36, NameLine: 1},
		},
		{
			name: "anonymous default arrow export",
			src:  "export default () => <div />;\n",
			want: extract.Symbol{Name: "default", QualifiedName: "default", Kind: "function", Signature: "export default () =>",
				Exported: true, ExportName: "default", JSX: true, StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 29, NameLine: 1},
		},
		{
			name: "namespace",
			src:  "namespace NS { export const z = 1; }\n",
			want: extract.Symbol{Name: "NS", QualifiedName: "NS", Kind: "namespace", Signature: "namespace NS",
				StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 36, NameLine: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := run(t, lang.TypeScript, tt.src)
			assert.Equal(t, tt.want, symbol(t, res, tt.want.Name))
		})
	}
}

func TestClassMembers(t *testing.T) {
	src := `@Controller('orders')
export class OrderController {
  private cache = new Map();
  handle = (e: Event) => { this.run(e); };

  constructor(private readonly svc: OrderService) {}

  @Get(':id')
  find(@Param('id') id: string): Promise<Order[]> {
    return this.svc.total(id);
  }

  private run(e: Event): void {}
}
`
	res := run(t, lang.TypeScript, src)

	ctor := symbol(t, res, "constructor")
	assert.Equal(t, "constructor", ctor.Kind)
	assert.Equal(t, "OrderController", ctor.Container)
	assert.Equal(t, "OrderController.constructor", ctor.QualifiedName)
	assert.Equal(t, "constructor(private readonly svc: OrderService)", ctor.Signature)

	find := symbol(t, res, "find")
	assert.Equal(t, "method", find.Kind)
	assert.Equal(t, []string{"@Get(':id')"}, find.Annotations)
	assert.Equal(t, []string{"@Controller('orders')"}, symbol(t, res, "OrderController").Annotations)
	assert.Nil(t, symbol(t, res, "constructor").Annotations)
	assert.Equal(t, "find(@Param('id') id: string): Promise<Order[]>", find.Signature)
	assert.Equal(t, 8, find.StartLine, "range starts at the decorator")
	assert.Equal(t, 11, find.EndLine)
	assert.True(t, find.Exported)

	run := symbol(t, res, "run")
	assert.False(t, run.Exported, "private members are not exported")

	handle := symbol(t, res, "handle")
	assert.Equal(t, "method", handle.Kind, "arrow function field counts as method")
	assert.Equal(t, "handle = (e: Event) =>", handle.Signature)

	cache := symbol(t, res, "cache")
	assert.Equal(t, "property", cache.Kind)
	assert.Equal(t, "private cache", cache.Signature)

	// Constructor injection: `this.svc.total()` knows the declared type.
	total := refsOf(res, "total")
	require.Len(t, total, 1)
	assert.Equal(t, extract.RefMethod, total[0].Kind)
	assert.Equal(t, "svc", total[0].Receiver)
	assert.Equal(t, "OrderService", total[0].ReceiverType)
	assert.Equal(t, symbolIndex(res, "find"), total[0].Container)

	// Decorators become annotation refs attached to the decorated symbol.
	controller := refsOf(res, "Controller")
	require.Len(t, controller, 1)
	assert.Equal(t, extract.RefAnnotation, controller[0].Kind)
	assert.Equal(t, symbolIndex(res, "OrderController"), controller[0].Container)
	get := refsOf(res, "Get")
	require.Len(t, get, 1)
	assert.Equal(t, symbolIndex(res, "find"), get[0].Container)

	svcType := refsOf(res, "OrderService")
	require.Len(t, svcType, 1)
	assert.Equal(t, extract.RefType, svcType[0].Kind)
}

func TestTypeAliasMembers(t *testing.T) {
	src := `export type Base = { id: string };
export type Arrow = Base & Readonly<{
  elbowed: boolean;
  customData?: { generationData?: Data };
  update(x: number): void;
}>;
type Action = { kind: "a" } | { kind: "b" };
class Eraser extends Trail {
  add() { super.addPoint(1); }
}
`
	res := run(t, lang.TypeScript, src)
	var members []string
	for _, s := range res.Symbols {
		if s.Container != "" {
			members = append(members, s.QualifiedName+" "+s.Kind+" in "+s.Container)
		}
	}
	assert.ElementsMatch(t, []string{
		"Base.id property in Base", "Arrow.elbowed property in Arrow", "Arrow.customData property in Arrow",
		"Arrow.customData.generationData property in Arrow.customData", "Arrow.update method in Arrow",
		"Action.kind property in Action", "Action.kind property in Action", "Eraser.add method in Eraser",
	}, members, "object members of an alias belong to it, also inside Readonly<...>, unions and nested literals")

	var bases []string
	for _, r := range res.Refs {
		if r.Kind == extract.RefExtends {
			bases = append(bases, r.Name)
		}
	}
	assert.ElementsMatch(t, []string{"Base", "Readonly", "Trail"}, bases, "types beside the object literal are bases of the alias")
	assert.Equal(t, symbolIndex(res, "Arrow"), refsOf(res, "Base")[0].Container)
	require.Len(t, refsOf(res, "Data"), 1)
	assert.Equal(t, extract.RefType, refsOf(res, "Data")[0].Kind, "a type inside a member is a plain type use")

	addPoint := refsOf(res, "addPoint")
	require.Len(t, addPoint, 1)
	assert.Equal(t, "super", addPoint[0].Receiver)
	assert.Equal(t, "Trail", addPoint[0].ReceiverType, "super.m() starts at the extended class")
}

func TestScopedLocalTypes(t *testing.T) {
	src := `const top: Svc = new Svc();
function a() {
  const field = get();
  field.run();
  top.run();
}
function b(top: Other) {
  const field: Field = get();
  field.run();
  top.run();
  [1].forEach(field => field.run());
}
class C {
  constructor(private readonly svc: Svc) {}
  go(repo: Repo) { this.svc.run(); }
  other() { this.repo.run(); }
}
`
	res := run(t, lang.TypeScript, src)
	var receivers []string
	for _, r := range refsOf(res, "run") {
		receivers = append(receivers, r.Receiver+":"+r.ReceiverType)
	}
	assert.Equal(t, []string{"field:call:get", "top:Svc", "field:Field", "top:Other", "field:", "svc:Svc", "repo:"}, receivers,
		"each use sees the declaration of its own function or block; a method parameter is not a field")
}

func TestUtilityTypeReceivers(t *testing.T) {
	src := "function f(a: Readonly<Svc>, b: Pick<Svc, \"run\">, c: Partial<Array<Svc>>, d: Readonly<{ x: number }>, e: Svc | null | undefined, g: Svc | Other) {\n" +
		"  a.run(); b.run(); c.run(); d.run(); e.run(); g.run();\n}\n"
	res := run(t, lang.TypeScript, src)
	var receivers []string
	for _, r := range refsOf(res, "run") {
		receivers = append(receivers, r.Receiver+":"+r.ReceiverType)
	}
	assert.Equal(t, []string{"a:Svc", "b:Svc", "c:Array<Svc>", "d:", "e:Svc", "g:"}, receivers,
		"Readonly, Pick and Partial keep the members of their first type argument; null and undefined do not change them")
}

func TestDestructuredExportsAndImportedReexports(t *testing.T) {
	src := "import { atom } from 'jotai';\nimport helper from './helper';\nimport { Local } from './local';\n" +
		"export const { useAtom, useSetAtom: setAtom, deep: { inner = 1 } } = jotai;\n" +
		"const [first, , ...rest] = pair;\n" +
		"const { a } = require('./cjs');\n" +
		"export { atom, Local as Renamed };\nexport default helper;\n"
	res := run(t, lang.TypeScript, src)
	var vars []string
	for _, s := range res.Symbols {
		vars = append(vars, fmt.Sprintf("%s exported=%v as %s", s.Name, s.Exported, s.ExportName))
	}
	assert.ElementsMatch(t, []string{
		"useAtom exported=true as useAtom", "setAtom exported=true as setAtom", "inner exported=true as inner",
		"first exported=false as ", "rest exported=false as ",
	}, vars, "each destructured name is a variable; a destructured require stays an import")
	var reexports []string
	for _, im := range res.Imports {
		if im.IsReexport {
			reexports = append(reexports, im.Module+" "+im.ImportedName+" as "+im.LocalName)
		}
	}
	assert.ElementsMatch(t, []string{"jotai atom as atom", "./local Local as Renamed", "./helper default as default"}, reexports,
		"exporting an imported name re-exports it from its module")
}

func TestDefaultExpressionAndAmbientExports(t *testing.T) {
	describe := func(res extract.Result) []string {
		var out []string
		for _, s := range res.Symbols {
			out = append(out, fmt.Sprintf("%s %s exported=%v %s", s.QualifiedName, s.Kind, s.Exported, s.Signature))
		}
		return out
	}
	memo := run(t, lang.TypeScript, "export declare class GestureEvent extends UIEvent {\n  rotation: number;\n}\n"+
		"declare const VERSION: string;\nfunction Canvas() {\n  return null;\n}\nexport default React.memo(Canvas, areEqual);\n")
	assert.ElementsMatch(t, []string{
		"GestureEvent class exported=true export class GestureEvent extends UIEvent",
		"GestureEvent.rotation property exported=true rotation: number",
		"VERSION variable exported=false const VERSION: string;",
		"Canvas function exported=false function Canvas()",
		"default variable exported=true export default React.memo(Canvas, areEqual);",
	}, describe(memo), "declare unwraps to the declaration; a default expression is the default export")
	object := run(t, lang.TypeScript, "export default {\n  Row: RowStack,\n  Col: ColStack,\n};\n")
	assert.Equal(t, []string{"default variable exported=true export default"}, describe(object))
}

func TestOverloadSignaturesExportNoValue(t *testing.T) {
	src := "export function f(a: string): string;\nexport function f(a: number): number;\nexport function f(a: any) {\n  return a;\n}\n" +
		"export default function g(a: string): void;\nexport default function g(a?: string) {}\n"
	res := run(t, lang.TypeScript, src)
	var symbols []string
	for _, s := range res.Symbols {
		symbols = append(symbols, s.Name+" "+s.Kind+" "+s.ExportName)
	}
	assert.ElementsMatch(t, []string{"f function f", "g function default"}, symbols,
		"overload signatures are not default values; the implementations are the symbols")
}

func symbolIndex(res extract.Result, name string) int {
	for i, s := range res.Symbols {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func TestImportsESM(t *testing.T) {
	src := `import { Injectable } from '@nestjs/common';
import { calculateDiscount, applyCoupon as ac } from './pricing/discount';
import Button from './components/Button';
import * as ns from './ns';
import type { Foo } from './foo';
import './styles.css';
import logo from './logo.svg';
export { default as Button } from './Button';
export { a, b as c } from './ab';
export * from './utils';
export * as utils from './utils';
`
	res := run(t, lang.TypeScript, src)
	want := []extract.Import{
		{Kind: "esm", Module: "@nestjs/common", ImportedName: "Injectable", LocalName: "Injectable", Line: 1},
		{Kind: "esm", Module: "./pricing/discount", ImportedName: "calculateDiscount", LocalName: "calculateDiscount", Line: 2},
		{Kind: "esm", Module: "./pricing/discount", ImportedName: "applyCoupon", LocalName: "ac", Line: 2},
		{Kind: "esm", Module: "./components/Button", ImportedName: "default", LocalName: "Button", Line: 3},
		{Kind: "esm", Module: "./ns", ImportedName: "*", LocalName: "ns", Line: 4},
		{Kind: "esm", Module: "./foo", ImportedName: "Foo", LocalName: "Foo", Line: 5},
		{Kind: "esm", Module: "./styles.css", Line: 6},
		{Kind: "esm", Module: "./logo.svg", ImportedName: "default", LocalName: "logo", Line: 7},
		{Kind: "esm", Module: "./Button", ImportedName: "default", LocalName: "Button", IsReexport: true, Line: 8},
		{Kind: "esm", Module: "./ab", ImportedName: "a", LocalName: "a", IsReexport: true, Line: 9},
		{Kind: "esm", Module: "./ab", ImportedName: "b", LocalName: "c", IsReexport: true, Line: 9},
		{Kind: "esm", Module: "./utils", ImportedName: "*", LocalName: "*", IsReexport: true, IsWildcard: true, Line: 10},
		{Kind: "esm", Module: "./utils", ImportedName: "*", LocalName: "utils", IsReexport: true, Line: 11},
	}
	assert.Equal(t, want, res.Imports)

	// Imported names are also refs, so `refs X` lists importers.
	assert.Equal(t, extract.RefImport, refsOf(res, "calculateDiscount")[0].Kind)
	assert.Equal(t, extract.RefImport, refsOf(res, "Button")[0].Kind)
}

func TestImportsCommonJS(t *testing.T) {
	src := `const { calculateTax, round: roundMoney } = require('./tax');
const report = require('./report');
import fs = require('fs');
`
	res := run(t, lang.JavaScript, src)
	want := []extract.Import{
		{Kind: "cjs", Module: "fs", ImportedName: "*", LocalName: "fs", Line: 3},
		{Kind: "cjs", Module: "./tax", ImportedName: "calculateTax", LocalName: "calculateTax", Line: 1},
		{Kind: "cjs", Module: "./tax", ImportedName: "round", LocalName: "roundMoney", Line: 1},
		{Kind: "cjs", Module: "./report", ImportedName: "*", LocalName: "report", Line: 2},
	}
	assert.ElementsMatch(t, want, res.Imports)
	assert.Empty(t, res.Symbols, "require() bindings are imports, not symbols")
}

func TestExportsCommonJS(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want map[string]string // symbol name -> export name
	}{
		{
			name: "object literal shorthand",
			src:  "function calculateTax(x) { return x; }\nfunction helper() {}\nmodule.exports = { calculateTax, helper };\n",
			want: map[string]string{"calculateTax": "calculateTax", "helper": "helper"},
		},
		{
			name: "object literal with alias and inline function",
			src:  "function a() {}\nmodule.exports = { b: a, c: () => 1, d() { return 2; } };\n",
			want: map[string]string{"a": "b", "c": "c", "d": "d"},
		},
		{
			name: "module.exports identifier is default",
			src:  "class Tax {}\nmodule.exports = Tax;\n",
			want: map[string]string{"Tax": "default"},
		},
		{
			name: "exports.x and module.exports.x",
			src:  "function a() {}\nfunction b() {}\nexports.a = a;\nmodule.exports.bee = b;\nexports.inline = function () {};\n",
			want: map[string]string{"a": "a", "b": "bee", "inline": "inline"},
		},
		{
			name: "esm export list with alias and export default identifier",
			src:  "function formatMoney() {}\nconst useCart = () => 1;\nclass Svc {}\nexport { formatMoney, useCart as cart };\nexport default Svc;\n",
			want: map[string]string{"formatMoney": "formatMoney", "useCart": "cart", "Svc": "default"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := run(t, lang.JavaScript, tt.src)
			got := map[string]string{}
			for _, s := range res.Symbols {
				if s.Exported {
					got[s.Name] = s.ExportName
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRefs(t *testing.T) {
	src := `import { calculateDiscount } from './discount';
import Button from './Button';
export function checkout(items: Item[]): Money {
  const cart = new Cart();
  const total = calculateDiscount(cart);
  return <Button label={total} />;
}
class Sub extends Base implements Contract {}
`
	res := run(t, lang.TypeScript, src)
	kinds := map[string]string{}
	for _, r := range res.Refs {
		if r.Kind != extract.RefImport {
			kinds[r.Name+"/"+r.Kind] = r.Kind
		}
	}
	for _, want := range []string{
		"calculateDiscount/call", "Cart/new", "Item/type", "Money/type",
		"Button/jsx", "Base/extends", "Contract/implements",
	} {
		assert.Contains(t, kinds, want)
	}
	// Locals like `items`, `cart` and `total` are not refs.
	assert.Empty(t, refsOf(res, "items"))
	assert.Empty(t, refsOf(res, "cart"))
	assert.Empty(t, refsOf(res, "total"))
	// The call inside checkout has checkout as its container.
	call := refsOf(res, "calculateDiscount")
	require.Len(t, call, 2) // import ref + call ref
	assert.Equal(t, symbolIndex(res, "checkout"), call[1].Container)
	assert.Equal(t, 5, call[1].Line)
	assert.Equal(t, 17, call[1].Col)
}

func TestStaticAndNamespaceCalls(t *testing.T) {
	src := `import * as ns from './ns';
const svc = new OrderService();
svc.total();
OrderService.create();
ns.thing();
items.map(x => x);
`
	res := run(t, lang.JavaScript, src)
	get := func(name string) extract.Ref { r := refsOf(res, name); return r[0] }
	assert.Equal(t, "OrderService", get("total").ReceiverType)
	assert.Equal(t, "OrderService", get("create").ReceiverType)
	assert.Equal(t, "ns", get("thing").Receiver)
	assert.Equal(t, "ns", get("thing").ReceiverType)
	assert.Equal(t, "", get("map").ReceiverType)
}

func TestPropertyRefs(t *testing.T) {
	src := `import { OrderService } from './svc';
export class Ctrl {
  constructor(private readonly orders: OrderService) {}
  run(owner: Owner) {
    const a = this.orders.cache;
    const b = owner.telephone;
    return this.orders.total(a) + b.length;
  }
}
`
	res := run(t, lang.TypeScript, src)
	props := map[string]extract.Ref{}
	for _, r := range res.Refs {
		if r.Kind == extract.RefProperty {
			props[r.Name] = r
		}
	}
	require.Contains(t, props, "cache")
	assert.Equal(t, "orders", props["cache"].Receiver)
	assert.Equal(t, "OrderService", props["cache"].ReceiverType)
	require.Contains(t, props, "telephone")
	assert.Equal(t, "owner", props["telephone"].Receiver)
	assert.Equal(t, "Owner", props["telephone"].ReceiverType)
	// this.orders inside the call target is the receiver of a method call, not a property ref.
	assert.Equal(t, extract.RefMethod, refsOf(res, "total")[0].Kind)
	// b.length: b is a local with unknown type, still recorded but without type.
	assert.Equal(t, "", props["length"].ReceiverType)
}

func TestNestedFunctions(t *testing.T) {
	src := `export function createFormControl(props) {
  let _fields = {};
  const register = (name, options = {}) => {
    _fields[name] = options;
    return { name };
  };
  function handleSubmit(onValid) {
    return () => validate();
  }
  const validate = () => Object.keys(_fields);
  return { register, handleSubmit };
}
`
	res := run(t, lang.JavaScript, src)
	register := symbol(t, res, "register")
	assert.Equal(t, "createFormControl", register.Container)
	assert.Equal(t, "createFormControl.register", register.QualifiedName)
	assert.Equal(t, "function", register.Kind)
	assert.Equal(t, "const register = (name, options = {}) =>", register.Signature)
	assert.Equal(t, 3, register.StartLine)
	assert.Equal(t, 6, register.EndLine)
	assert.False(t, register.Exported)

	handle := symbol(t, res, "handleSubmit")
	assert.Equal(t, "createFormControl.handleSubmit", handle.QualifiedName)
	assert.Equal(t, "function handleSubmit(onValid)", handle.Signature)

	// A variável local `_fields` não vira símbolo; a chamada a validate() vira ref.
	for _, s := range res.Symbols {
		assert.NotEqual(t, "_fields", s.Name)
	}
	require.Len(t, refsOf(res, "validate"), 1)
	assert.Equal(t, extract.RefCall, refsOf(res, "validate")[0].Kind)
	assert.Equal(t, symbolIndex(res, "handleSubmit"), refsOf(res, "validate")[0].Container)

	// `return { register, handleSubmit }` expõe as closures: refs de
	// identificador com a factory como container.
	exposed := refsOf(res, "handleSubmit")
	require.Len(t, exposed, 1)
	assert.Equal(t, extract.RefIdentifier, exposed[0].Kind)
	assert.Equal(t, symbolIndex(res, "createFormControl"), exposed[0].Container)
	assert.Equal(t, 11, exposed[0].Line)
}

func TestCallArity(t *testing.T) {
	src := `import { f } from './f';
class Svc { m(a: number, b?: number) {} }
const s = new Svc();
f(1, 2);
f(...args);
s.m(1);
new Svc();
new Svc(1);
const g = () => f();
`
	res := run(t, lang.TypeScript, src)
	arities := map[string]int{}
	for _, r := range res.Refs {
		if r.Kind == extract.RefCall || r.Kind == extract.RefMethod || r.Kind == extract.RefNew {
			arities[r.Name+"@"+fmt.Sprint(r.Line)] = r.Arity
		}
	}
	assert.Equal(t, map[string]int{"f@4": 2, "f@5": -1, "m@6": 1, "Svc@7": 0, "Svc@8": 1, "f@9": 0, "Svc@3": 0}, arities)
	for _, r := range res.Refs {
		if r.Kind == extract.RefIdentifier || r.Kind == extract.RefType {
			assert.Equal(t, -1, r.Arity, "non-calls carry no arity: %+v", r)
		}
	}
}

func TestAnonymousDefaultNestedFunctionsAndBind(t *testing.T) {
	src := `import appendErrors from './appendErrors';

export default async (field: string) => {
  const setCustomValidity = (message?: string) => {
    return message;
  };
  const curry = appendErrors.bind(null, field);
  return curry(setCustomValidity('x'));
};
`
	res := run(t, lang.TypeScript, src)
	nested := symbol(t, res, "setCustomValidity")
	assert.Equal(t, "default", nested.Container)
	assert.Equal(t, "default.setCustomValidity", nested.QualifiedName)
	assert.Equal(t, 4, nested.StartLine)

	calls := refsOf(res, "appendErrors")
	require.Len(t, calls, 2, "import ref and the .bind call: %+v", calls)
	assert.Equal(t, extract.RefCall, calls[1].Kind, ".bind counts as a call of appendErrors")
	assert.Equal(t, 7, calls[1].Line)
	assert.Empty(t, refsOf(res, "bind"), "bind itself is not a method ref")
}

func TestReturnHints(t *testing.T) {
	src := `import { Repo } from './repo';
export class Svc {}
export function make() { return new Svc(); }
export function pass(x: Svc) { return x; }
export function viaCall() { return make(); }
export function awaited() { return (async () => 1) && make(); }
export function nothing() { return { a: 1 }; }
export function typed(): Svc { return new Svc(); }
export const arrow = () => new Repo();
export class Holder {
  private repo: Repo;
  get() { return this.repo; }
  async load() { return await make(); }
  cb() { items.map((i) => { return new Repo(); }); return 1; }
}
`
	res := run(t, lang.TypeScript, src)
	hints := map[string]string{}
	for _, s := range res.Symbols {
		if s.Kind == extract.KindFunction || s.Kind == extract.KindMethod {
			hints[s.Name] = s.ReturnHint
		}
	}
	assert.Equal(t, map[string]string{
		"make": "Svc", "pass": "Svc", "viaCall": "call:make", "awaited": "", "nothing": "", "typed": "",
		"arrow": "Repo", "get": "Repo", "load": "call:make", "cb": "",
	}, hints, "typed functions keep the annotation; the callback's return is not cb's")
}

func TestLocalsShadowTopLevelNames(t *testing.T) {
	src := "let counter = 0;\n" +
		"export function polygon(...points: number[]) { return points; }\n" +
		"let i = 0;\n" +
		"counter++;\n" +
		"const Child = () => {\n" +
		"  const counter = useRef(0);\n" +
		"  counter.current++;\n" +
		"};\n" +
		"export const includes = <P>(point: P, polygon: P[]) => polygon.length;\n" +
		"items.map((field, i) => register('test.' + i));\n" +
		"export function outer() {\n" +
		"  const helper = () => 1;\n" +
		"  return helper() + polygon(1).length + i + counter;\n" +
		"}\n" +
		"function noop() {}\n" +
		"export const Form = ({ onSubmit = noop }) => onSubmit;\n"
	res := run(t, lang.TypeScript, src)
	tests := []struct {
		name  string
		lines []int
		why   string
	}{
		{"counter", []int{4, 13}, "the const inside Child hides the top-level counter on line 7"},
		{"polygon", []int{13}, "the parameter of the generic arrow hides the function on line 9"},
		{"i", []int{13}, "the callback parameter hides the top-level i on line 10"},
		{"helper", []int{13}, "a nested arrow function is a symbol, so its call stays a reference"},
		{"noop", []int{16}, "a default value in a destructured parameter is a use, not a declaration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines []int
			for _, r := range refsOf(res, tt.name) {
				lines = append(lines, r.Line)
			}
			assert.Equal(t, tt.lines, lines, tt.why)
		})
	}
}

func TestDerivedLocalTypes(t *testing.T) {
	src := "function f(items: Item[], props: Props, list: readonly Item[], app: App[\"scene\"]) {\n" +
		"  const { field } = useController();\n" +
		"  const { owner, nested: { deep } } = props;\n" +
		"  items.forEach((item) => item.run());\n" +
		"  list.map(entry => entry.run());\n" +
		"  field.run();\n" +
		"  owner.run();\n" +
		"  deep.run();\n" +
		"  app.run();\n" +
		"}\n"
	res := run(t, lang.TypeScript, src)
	var receivers []string
	for _, r := range refsOf(res, "run") {
		receivers = append(receivers, r.Receiver+":"+r.ReceiverType)
	}
	assert.Equal(t, []string{`item:Item[][number]`, `entry:Item[][number]`, `field:call:useController["field"]`,
		`owner:Props["owner"]`, `deep:Props["nested"]["deep"]`, `app:App["scene"]`}, receivers,
		"array callbacks take the element type, destructured names the member type, and indexed access is left for the resolver")
}

func TestTestCallbackFunctions(t *testing.T) {
	src := "describe('frames', () => {\n" +
		"  function selectAndDuplicate() {}\n" +
		"  const numberHeap = () => new BinaryHeap();\n" +
		"  it('renders', async () => {\n" +
		"    function Input() { return null; }\n" +
		"  });\n" +
		"});\n" +
		"helper(() => { function notACallbackOfAPlainCall() {} })();\n"
	res := run(t, lang.TypeScript, src)
	var got []string
	for _, s := range res.Symbols {
		got = append(got, s.QualifiedName)
	}
	assert.Equal(t, []string{"describe.selectAndDuplicate", "describe.numberHeap", "it.Input"}, got,
		"functions declared in describe/it callbacks are symbols named after the call")
}
