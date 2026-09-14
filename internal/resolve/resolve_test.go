// Pacote externo de teste: o indexer importa resolve, e só um pacote
// _test pode fechar esse ciclo para rodar o pipeline completo.
package resolve_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

// index escreve os arquivos num repo temporário e roda o pipeline inteiro.
func index(t *testing.T, files map[string]string) *store.Store {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = indexer.New(root, repo.DefaultConfig(), st).Run(context.Background(), indexer.Options{Workers: 2})
	require.NoError(t, err)
	return st
}

// expectation descreve uma ref e o que se espera dela.
type expectation struct {
	file       string
	name       string
	kind       string
	resolution string
	target     string // qualified_name do símbolo resolvido ("" quando não há)
	targetFile string
}

func check(t *testing.T, st *store.Store, e expectation) {
	t.Helper()
	f, ok, err := st.FileByPath(e.file)
	require.NoError(t, err)
	require.True(t, ok, "file %s not indexed", e.file)
	refs, err := st.RefsOfFile(f.ID)
	require.NoError(t, err)
	for _, r := range refs {
		if r.Name != e.name || r.Kind != e.kind {
			continue
		}
		assert.Equal(t, e.resolution, r.Resolution, "%s %s in %s", e.kind, e.name, e.file)
		if e.target == "" {
			assert.Nil(t, r.ResolvedSymbolID, "%s %s in %s should not point to a symbol", e.kind, e.name, e.file)
			return
		}
		require.NotNil(t, r.ResolvedSymbolID, "%s %s in %s has no target", e.kind, e.name, e.file)
		sym, ok, err := st.SymbolByID(*r.ResolvedSymbolID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, e.target, sym.QualifiedName)
		if e.targetFile != "" {
			tf, _, err := st.FileByID(sym.FileID)
			require.NoError(t, err)
			assert.Equal(t, e.targetFile, tf.Path)
		}
		return
	}
	require.Failf(t, "ref not found", "%s %s in %s", e.kind, e.name, e.file)
}

func TestTypeScript(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []expectation
	}{
		{
			name: "local definition",
			files: map[string]string{
				"a.ts": "export function f() {}\nexport function g() { f(); }\n",
			},
			want: []expectation{{"a.ts", "f", "call", store.Resolved, "f", "a.ts"}},
		},
		{
			name: "relative import with and without extension",
			files: map[string]string{
				"lib/a.ts": "export function f() {}\n",
				"b.ts":     "import { f } from './lib/a';\nf();\n",
				"c.ts":     "import { f } from './lib/a.js';\nf();\n",
			},
			want: []expectation{
				{"b.ts", "f", "call", store.Resolved, "f", "lib/a.ts"},
				{"c.ts", "f", "call", store.Resolved, "f", "lib/a.ts"},
				{"c.ts", "f", "import", store.Resolved, "f", "lib/a.ts"},
			},
		},
		{
			name: "directory index barrel, default re-export and chained export star",
			files: map[string]string{
				"components/Button.tsx": "export default function Button() { return null; }\n",
				"components/Card.tsx":   "export function Card() { return null; }\n",
				"components/index.ts":   "export { default as Button } from './Button';\nexport * from './Card';\n",
				"ui.ts":                 "export * from './components';\n",
				"page.tsx":              "import { Button, Card } from './ui';\nexport function Page() { return <Card><Button /></Card>; }\n",
			},
			want: []expectation{
				{"page.tsx", "Button", "jsx", store.Resolved, "Button", "components/Button.tsx"},
				{"page.tsx", "Card", "jsx", store.Resolved, "Card", "components/Card.tsx"},
			},
		},
		{
			name: "default import and aliased import",
			files: map[string]string{
				"svc.ts": "export default class Svc { run() {} }\nexport function helper() {}\n",
				"use.ts": "import Svc, { helper as h } from './svc';\nnew Svc();\nh();\n",
			},
			want: []expectation{
				{"use.ts", "Svc", "new", store.Resolved, "Svc", "svc.ts"},
				{"use.ts", "h", "call", store.Resolved, "helper", "svc.ts"},
			},
		},
		{
			name: "re-export cycle terminates as unresolved",
			files: map[string]string{
				"a.ts": "export * from './b';\n",
				"b.ts": "export * from './a';\n",
				"c.ts": "import { missing } from './a';\nmissing();\n",
			},
			want: []expectation{{"c.ts", "missing", "call", store.Unresolved, "", ""}},
		},
		{
			name: "external package and asset imports",
			files: map[string]string{
				"a.tsx":    "import { useState } from 'react';\nimport './a.css';\nimport logo from './logo.svg';\nexport function A() { useState(1); return <img src={logo} />; }\n",
				"a.css":    "",
				"logo.svg": "",
			},
			want: []expectation{
				{"a.tsx", "useState", "call", store.External, "", ""},
				{"a.tsx", "logo", "identifier", store.External, "", ""},
			},
		},
		{
			name: "no import: two exported definitions are ambiguous, one is unresolved",
			files: map[string]string{
				"x/f.ts":  "export function dup() {}\nexport function single() {}\n",
				"y/f.ts":  "export function dup() {}\n",
				"main.js": "dup();\nsingle();\n",
			},
			want: []expectation{
				{"main.js", "dup", "call", store.Ambiguous, "", ""},
				{"main.js", "single", "call", store.Unresolved, "", ""},
			},
		},
		{
			name: "missing relative module",
			files: map[string]string{
				"a.ts": "import { f } from './nope';\nf();\n",
			},
			want: []expectation{{"a.ts", "f", "call", store.Unresolved, "", ""}},
		},
		{
			name: "commonjs require resolves to module.exports",
			files: map[string]string{
				"tax.js":    "function calculateTax(a) { return a; }\nmodule.exports = { calculateTax };\n",
				"report.js": "const { calculateTax } = require('./tax');\nfunction build() { return calculateTax(1); }\nmodule.exports = { build };\n",
			},
			want: []expectation{
				{"report.js", "calculateTax", "call", store.Resolved, "calculateTax", "tax.js"},
				{"report.js", "calculateTax", "import", store.Resolved, "calculateTax", "tax.js"},
			},
		},
		{
			name: "method call through constructor injection and static call",
			files: map[string]string{
				"svc.ts":  "export class OrderService { total() {} static create() {} }\n",
				"ctrl.ts": "import { OrderService } from './svc';\nexport class Ctrl {\n  constructor(private readonly orders: OrderService) {}\n  run() { this.orders.total(); OrderService.create(); }\n}\n",
			},
			want: []expectation{
				{"ctrl.ts", "total", "method", store.Resolved, "OrderService.total", "svc.ts"},
				{"ctrl.ts", "create", "method", store.Resolved, "OrderService.create", "svc.ts"},
				{"ctrl.ts", "OrderService", "type", store.Resolved, "OrderService", "svc.ts"},
			},
		},
		{
			name: "nested function (closure) resolves inside its file",
			files: map[string]string{
				"factory.ts": "export function create() {\n  const register = () => 1;\n  function submit() { return register(); }\n  return { register, submit };\n}\n",
			},
			want: []expectation{{"factory.ts", "register", "call", store.Resolved, "create.register", "factory.ts"}},
		},
		{
			name: "namespace import member call",
			files: map[string]string{
				"n.ts":   "export function thing() {}\n",
				"use.ts": "import * as ns from './n';\nns.thing();\n",
			},
			want: []expectation{{"use.ts", "thing", "method", store.Resolved, "thing", "n.ts"}},
		},
		{
			name: "decorators from external package are external, custom ones resolve",
			files: map[string]string{
				"deco.ts": "export function Log() { return () => {}; }\n",
				"svc.ts":  "import { Injectable } from '@nestjs/common';\nimport { Log } from './deco';\n@Injectable()\n@Log()\nexport class Svc {}\n",
			},
			want: []expectation{
				{"svc.ts", "Injectable", "annotation", store.External, "", ""},
				{"svc.ts", "Log", "annotation", store.Resolved, "Log", "deco.ts"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := index(t, tt.files)
			for _, e := range tt.want {
				check(t, st, e)
			}
		})
	}
}

func TestJava(t *testing.T) {
	const pricing = "src/com/acme/pricing/"
	const order = "src/com/acme/order/"
	tests := []struct {
		name  string
		files map[string]string
		want  []expectation
	}{
		{
			name: "explicit import, same package without import, static import, jdk external",
			files: map[string]string{
				pricing + "Discount.java": "package com.acme.pricing;\npublic class Discount { public static int calculate(int x) { return x; } }\n",
				pricing + "Money.java":    "package com.acme.pricing;\npublic record Money(long cents) {}\n",
				order + "Order.java":      "package com.acme.order;\npublic class Order { public long total() { return 1; } }\n",
				order + "Svc.java": "package com.acme.order;\nimport java.util.List;\nimport com.acme.pricing.Discount;\nimport static com.acme.pricing.Discount.calculate;\n" +
					"public class Svc {\n  List<Order> items;\n  int run(Order o) { o.total(); Discount.calculate(1); return calculate(2); }\n}\n",
			},
			want: []expectation{
				{order + "Svc.java", "Discount", "import", store.Resolved, "com.acme.pricing.Discount", ""},
				{order + "Svc.java", "Order", "type", store.Resolved, "com.acme.order.Order", ""},
				{order + "Svc.java", "total", "method", store.Resolved, "com.acme.order.Order.total", ""},
				{order + "Svc.java", "calculate", "method", store.Resolved, "com.acme.pricing.Discount.calculate", ""},
				{order + "Svc.java", "List", "import", store.External, "", ""},
				{order + "Svc.java", "List", "type", store.External, "", ""},
			},
		},
		{
			name: "wildcard import resolves, two wildcards with the same name are ambiguous",
			files: map[string]string{
				pricing + "Money.java":              "package com.acme.pricing;\npublic record Money(long cents) {}\n",
				"src/com/acme/legacy/Money.java":    "package com.acme.legacy;\npublic class Money {}\n",
				"src/com/acme/report/Report.java":   "package com.acme.report;\nimport com.acme.pricing.*;\npublic class Report { Money m; }\n",
				"src/com/acme/report/Bridge.java":   "package com.acme.report;\nimport com.acme.pricing.*;\nimport com.acme.legacy.*;\npublic class Bridge { Money m = new Money(1); }\n",
				"src/com/acme/report/Explicit.java": "package com.acme.report;\nimport com.acme.legacy.Money;\nimport com.acme.pricing.*;\npublic class Explicit { Money m; }\n",
			},
			want: []expectation{
				{"src/com/acme/report/Report.java", "Money", "type", store.Resolved, "com.acme.pricing.Money", ""},
				{"src/com/acme/report/Bridge.java", "Money", "type", store.Ambiguous, "", ""},
				{"src/com/acme/report/Bridge.java", "Money", "new", store.Ambiguous, "", ""},
				{"src/com/acme/report/Explicit.java", "Money", "type", store.Resolved, "com.acme.legacy.Money", ""},
			},
		},
		{
			name: "record accessor, own method, fully qualified jdk type and unknown type",
			files: map[string]string{
				pricing + "Money.java": "package com.acme.pricing;\npublic record Money(long cents) {\n  public Money plus(Money o) { return new Money(cents + o.cents()); }\n  public Money twice() { return plus(this); }\n  java.util.Map<String, Money> cache;\n  Widget w;\n}\n",
			},
			want: []expectation{
				{pricing + "Money.java", "cents", "method", store.Resolved, "com.acme.pricing.Money.cents", ""},
				{pricing + "Money.java", "plus", "method", store.Resolved, "com.acme.pricing.Money.plus", ""},
				{pricing + "Money.java", "Map", "type", store.External, "", ""},
				{pricing + "Money.java", "String", "type", store.External, "", ""},
				{pricing + "Money.java", "Widget", "type", store.Unresolved, "", ""},
			},
		},
		{
			name: "implements, extends and annotations produce resolved refs",
			files: map[string]string{
				order + "Base.java":   "package com.acme.order;\npublic class Base {}\n",
				order + "Svc.java":    "package com.acme.order;\npublic interface Svc { void run(); }\n",
				order + "Marker.java": "package com.acme.order;\npublic @interface Marker {}\n",
				order + "Impl.java":   "package com.acme.order;\n@Marker\npublic class Impl extends Base implements Svc {\n  @Override public void run() {}\n}\n",
			},
			want: []expectation{
				{order + "Impl.java", "Base", "extends", store.Resolved, "com.acme.order.Base", ""},
				{order + "Impl.java", "Svc", "implements", store.Resolved, "com.acme.order.Svc", ""},
				{order + "Impl.java", "Marker", "annotation", store.Resolved, "com.acme.order.Marker", ""},
				{order + "Impl.java", "Override", "annotation", store.External, "", ""},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := index(t, tt.files)
			for _, e := range tt.want {
				check(t, st, e)
			}
		})
	}
}

func TestPropertyRefsAndInheritedMembers(t *testing.T) {
	const order = "src/com/acme/order/"
	st := index(t, map[string]string{
		order + "Owner.java":           "package com.acme.order;\npublic class Owner {\n  private String telephone;\n  public String getTelephone() { return this.telephone; }\n  public void setTelephone(String t) { telephone = t; }\n}\n",
		order + "OwnerRepository.java": "package com.acme.order;\nimport org.springframework.data.jpa.repository.JpaRepository;\npublic interface OwnerRepository extends JpaRepository<Owner, Integer> {\n  Owner findByName(String name);\n}\n",
		order + "Svc.java":             "package com.acme.order;\npublic class Svc {\n  private final OwnerRepository owners;\n  Svc(OwnerRepository owners) { this.owners = owners; }\n  void run(Owner o) { owners.save(o); owners.findByName(\"x\"); String t = o.telephone; }\n}\n",
		"src/ctrl.ts":                  "import { OrderService } from './svc';\nexport class Ctrl {\n  constructor(private readonly svc: OrderService) {}\n  run() { return this.svc.cache; }\n}\n",
		"src/svc.ts":                   "export class OrderService { cache = new Map(); }\n",
	})
	for _, e := range []expectation{
		{order + "Owner.java", "telephone", "property", store.Resolved, "com.acme.order.Owner.telephone", ""},
		{order + "Svc.java", "telephone", "property", store.Resolved, "com.acme.order.Owner.telephone", ""},
		{order + "Svc.java", "findByName", "method", store.Resolved, "com.acme.order.OwnerRepository.findByName", ""},
		// save() não está em OwnerRepository, mas ela estende um tipo fora do índice: herdado da lib.
		{order + "Svc.java", "save", "method", store.External, "", ""},
		{"src/ctrl.ts", "cache", "property", store.Resolved, "OrderService.cache", "src/svc.ts"},
	} {
		check(t, st, e)
	}
}

func TestEdgesFollowResolution(t *testing.T) {
	st := index(t, map[string]string{
		"src/com/acme/A.java": "package com.acme;\npublic class A { void a() { new B().b(); } }\n",
		"src/com/acme/B.java": "package com.acme;\npublic class B implements Runnable { public void run() {} void b() {} }\n",
	})
	a, err := st.SymbolsByQualifiedName("com.acme.A.a")
	require.NoError(t, err)
	require.Len(t, a, 1)
	callees, err := st.Callees(a[0].ID, store.EdgeCalls)
	require.NoError(t, err)
	var names []string
	for _, c := range callees {
		names = append(names, c.QualifiedName)
	}
	assert.ElementsMatch(t, []string{"com.acme.B", "com.acme.B.b"}, names)

	b, err := st.SymbolsByQualifiedName("com.acme.B")
	require.NoError(t, err)
	runnable, err := st.Callees(b[0].ID, store.EdgeImplements)
	require.NoError(t, err)
	assert.Empty(t, runnable, "external interfaces produce no edge")
}

// TestOverloadsByArity: sobrecargas se separam pelo número de argumentos da
// chamada; quando nenhuma assinatura bate, a ref fica ambígua (nunca chuta).
func TestOverloadsByArity(t *testing.T) {
	const owner = "src/com/acme/owner/"
	st := index(t, map[string]string{
		owner + "Owner.java": "package com.acme.owner;\nimport java.util.List;\npublic class Owner {\n" +
			"  public Pet getPet(Integer id) { return null; }\n" +
			"  public Pet getPet(String name) { return null; }\n" +
			"  public Pet getPet(String name, boolean ignoreNew) { return null; }\n" +
			"  public void addVisit(Integer petId, Visit visit) { Pet pet = getPet(petId); pet.addVisit(visit); }\n" +
			"  public Pet byName(String n) { return getPet(n, true); }\n" +
			"  public List<Pet> pets() { return List.of(); }\n" +
			"}\n",
		owner + "Pet.java":   "package com.acme.owner;\npublic class Pet {\n  public void addVisit(Visit v) {}\n  public void addVisit(Visit v, boolean check) {}\n}\n",
		owner + "Visit.java": "package com.acme.owner;\npublic class Visit {}\n",
		owner + "Ctl.java": "package com.acme.owner;\npublic class Ctl {\n  Pet load(Owner owner, int petId, String name) { owner.getPet(name, true); return owner.getPet(petId); }\n" +
			"  void twoNames(Owner owner) { owner.getPet(1, 2, 3); }\n}\n",
		"src/form.ts": "export function register(name: string, options = {}) { return name; }\nexport function register2(a: number) { return a; }\n" +
			"export class Form {\n  set(name: string): void {}\n  set(name: string, value: unknown): void {}\n  run() { this.set('a'); this.set('a', 1); }\n}\n",
	})
	for _, e := range []expectation{
		// getPet(petId) dentro de Owner: uma de três sobrecargas, pela aridade.
		{owner + "Owner.java", "getPet", "method", store.Resolved, "com.acme.owner.Owner.getPet", ""},
		{owner + "Owner.java", "addVisit", "method", store.Resolved, "com.acme.owner.Pet.addVisit", ""},
		{owner + "Ctl.java", "getPet", "method", store.Resolved, "com.acme.owner.Owner.getPet", ""},
	} {
		check(t, st, e)
	}
	// As três chamadas em Owner/Ctl apontam para sobrecargas diferentes: a de
	// dois argumentos em byName e em Ctl.load, a de um em addVisit e Ctl.load.
	ctl, _, err := st.FileByPath(owner + "Ctl.java")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(ctl.ID)
	require.NoError(t, err)
	byLine := map[int]store.Ref{}
	for _, r := range refs {
		if r.Name == "getPet" {
			byLine[r.Arity] = r
		}
	}
	two, _, err := st.SymbolByID(*byLine[2].ResolvedSymbolID)
	require.NoError(t, err)
	assert.Contains(t, two.Signature, "String name, boolean ignoreNew")
	one, _, err := st.SymbolByID(*byLine[1].ResolvedSymbolID)
	require.NoError(t, err)
	assert.Contains(t, one.Signature, "getPet(Integer id)")
	assert.Equal(t, store.Ambiguous, byLine[3].Resolution, "no overload takes three arguments: stays ambiguous")

	form, _, err := st.FileByPath("src/form.ts")
	require.NoError(t, err)
	tsRefs, err := st.RefsOfFile(form.ID)
	require.NoError(t, err)
	var sets []store.Ref
	for _, r := range tsRefs {
		if r.Name == "set" && r.Kind == "method" {
			sets = append(sets, r)
		}
	}
	require.Len(t, sets, 2)
	for _, r := range sets {
		require.NotNil(t, r.ResolvedSymbolID, "set with %d args", r.Arity)
		target, _, err := st.SymbolByID(*r.ResolvedSymbolID)
		require.NoError(t, err)
		if r.Arity == 1 {
			assert.Equal(t, "set(name: string): void", target.Signature)
		} else {
			assert.Equal(t, "set(name: string, value: unknown): void", target.Signature)
		}
	}
}

// TestInheritedMembersInRepo: o membro é procurado nos tipos pai dentro do
// índice (Owner extends Person extends BaseEntity), em Java e em TS, mesmo
// quando o arquivo do pai ainda não foi resolvido na rodada; um pai externo
// ainda faz o membro ser external.
func TestInheritedMembersInRepo(t *testing.T) {
	const model = "src/com/acme/model/"
	const owner = "src/com/acme/owner/"
	st := index(t, map[string]string{
		model + "BaseEntity.java": "package com.acme.model;\npublic class BaseEntity {\n  private Integer id;\n  public Integer getId() { return id; }\n}\n",
		model + "Person.java":     "package com.acme.model;\npublic class Person extends BaseEntity {\n  public String getLastName() { return \"\"; }\n}\n",
		owner + "Owner.java":      "package com.acme.owner;\nimport com.acme.model.Person;\npublic class Owner extends Person {\n  public void touch() { getId(); }\n}\n",
		owner + "Ctl.java":        "package com.acme.owner;\npublic class Ctl {\n  void show(Owner owner) { owner.getLastName(); owner.getId(); owner.missing(); }\n}\n",
		owner + "Repo.java":       "package com.acme.owner;\nimport org.springframework.data.jpa.repository.JpaRepository;\npublic interface Repo extends JpaRepository<Owner, Integer> {}\n",
		owner + "Svc.java":        "package com.acme.owner;\npublic class Svc {\n  Repo repo;\n  void run(Owner o) { repo.save(o); }\n}\n",
		"src/base.ts":             "export class Base {\n  id = 0;\n  getId() { return this.id; }\n}\n",
		"src/person.ts":           "import { Base } from './base';\nexport class Person extends Base {\n  lastName() { return ''; }\n}\n",
		"src/ctl.ts":              "import { Person } from './person';\nexport class Ctl {\n  show(p: Person) { return p.getId() + p.lastName() + p.id; }\n}\n",
	})
	for _, e := range []expectation{
		{owner + "Ctl.java", "getLastName", "method", store.Resolved, "com.acme.model.Person.getLastName", ""},
		{owner + "Ctl.java", "getId", "method", store.Resolved, "com.acme.model.BaseEntity.getId", ""},
		{owner + "Ctl.java", "missing", "method", store.Unresolved, "", ""},
		// Chamada sem receiver dentro da subclasse também sobe a cadeia.
		{owner + "Owner.java", "getId", "method", store.Resolved, "com.acme.model.BaseEntity.getId", ""},
		{owner + "Svc.java", "save", "method", store.External, "", ""},
		{"src/ctl.ts", "getId", "method", store.Resolved, "Base.getId", "src/base.ts"},
		{"src/ctl.ts", "lastName", "method", store.Resolved, "Person.lastName", "src/person.ts"},
		{"src/ctl.ts", "id", "property", store.Resolved, "Base.id", "src/base.ts"},
	} {
		check(t, st, e)
	}
}

// TestChainedReceivers: `a.b().c()` é seguido pelo tipo declarado de cada
// passo, dentro do índice; um passo fora do índice vira external e um
// passo sem tipo declarado (TS sem anotação) fica unresolved.
func TestChainedReceivers(t *testing.T) {
	const owner = "src/com/acme/owner/"
	st := index(t, map[string]string{
		owner + "Owner.java": "package com.acme.owner;\nimport java.util.Optional;\npublic class Owner {\n" +
			"  private Pet first;\n  public Pet getPet(Integer id) { return null; }\n  public Optional<Pet> maybe() { return Optional.empty(); }\n" +
			"  public Pet firstPet() { return first; }\n  void self() { firstPet().addVisit(null); getPet(1).addVisit(null); }\n}\n",
		owner + "Pet.java":   "package com.acme.owner;\npublic class Pet {\n  private Owner owner;\n  public void addVisit(Visit v) {}\n  public Owner getOwner() { return owner; }\n}\n",
		owner + "Visit.java": "package com.acme.owner;\npublic class Visit {}\n",
		owner + "Ctl.java": "package com.acme.owner;\npublic class Ctl {\n  void run(Owner owner, int id) {\n" +
			"    owner.getPet(id).addVisit(null);\n    owner.getPet(id).getOwner().getPet(2);\n    owner.first.addVisit(null);\n    owner.maybe().get();\n  }\n}\n",
		"src/svc.ts": "export class Svc {\n  total(): number { return 1; }\n}\nexport class Repo {\n  get(id: string): Svc { return new Svc(); }\n  loose(id: string) { return new Svc(); }\n}\nexport function createRepo(): Repo { return new Repo(); }\n",
		"src/ctl.ts": "import { Repo, createRepo } from './svc';\nexport class Ctl {\n  constructor(private readonly repo: Repo) {}\n  run(id: string) { this.repo.get(id).total(); this.repo.loose(id).total(); createRepo().get(id).total(); }\n}\n",
	})
	javaRefs := map[string]expectation{}
	for _, e := range []expectation{
		{owner + "Owner.java", "addVisit", "method", store.Resolved, "com.acme.owner.Pet.addVisit", ""},
		{owner + "Ctl.java", "addVisit", "method", store.Resolved, "com.acme.owner.Pet.addVisit", ""},
		{owner + "Ctl.java", "getOwner", "method", store.Resolved, "com.acme.owner.Pet.getOwner", ""},
		// Optional é java.util: fora do índice.
		{owner + "Ctl.java", "get", "method", store.External, "", ""},
		{"src/ctl.ts", "total", "method", store.Resolved, "Svc.total", "src/svc.ts"},
	} {
		javaRefs[e.file+e.name] = e
		check(t, st, e)
	}
	// Em Ctl.java há dois `getPet`: um direto (owner.getPet) e um no fim da
	// cadeia (...getOwner().getPet(2)); os dois resolvem.
	ctl, _, err := st.FileByPath(owner + "Ctl.java")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(ctl.ID)
	require.NoError(t, err)
	for _, r := range refs {
		if r.Name == "getPet" {
			assert.Equal(t, store.Resolved, r.Resolution, "getPet at line %d path %v", r.Line, r.ReceiverPath)
		}
	}
	// this.repo.loose(id).total(): loose não anota o retorno, mas `return new Svc()` é inferido.
	tsFile, _, err := st.FileByPath("src/ctl.ts")
	require.NoError(t, err)
	tsRefs, err := st.RefsOfFile(tsFile.ID)
	require.NoError(t, err)
	var totals []string
	for _, r := range tsRefs {
		if r.Name == "total" {
			totals = append(totals, r.Resolution+":"+strings.Join(r.ReceiverPath, "."))
		}
	}
	assert.ElementsMatch(t, []string{"resolved:get", "resolved:loose", "resolved:createRepo.get"}, totals)
}

func TestSubclassArgumentPicksOverload(t *testing.T) {
	const pkg = "src/com/acme/zoo/"
	st := index(t, map[string]string{
		pkg + "Animal.java":  "package com.acme.zoo;\npublic class Animal {}\n",
		pkg + "Dog.java":     "package com.acme.zoo;\npublic class Dog extends Animal {}\n",
		pkg + "Shelter.java": "package com.acme.zoo;\npublic class Shelter {\n  public void admit(Animal a) {}\n  public void admit(String name) {}\n  void run(Dog dog) { admit(dog); admit(\"rex\"); }\n}\n",
	})
	shelter, _, err := st.FileByPath(pkg + "Shelter.java")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(shelter.ID)
	require.NoError(t, err)
	got := map[string]string{}
	for _, r := range refs {
		if r.Name != "admit" {
			continue
		}
		require.NotNil(t, r.ResolvedSymbolID, "admit(%v)", r.ArgTypes)
		target, _, err := st.SymbolByID(*r.ResolvedSymbolID)
		require.NoError(t, err)
		got[r.ArgTypes[0]] = target.Signature
	}
	assert.Equal(t, map[string]string{"Dog": "public void admit(Animal a)", "String": "public void admit(String name)"}, got)
}

func TestTSConfigPaths(t *testing.T) {
	st := index(t, map[string]string{
		"tsconfig.json":      "{\n  // comentário permitido no tsconfig\n  \"compilerOptions\": {\n    \"baseUrl\": \".\",\n    \"paths\": { \"@/*\": [\"src/*\"], \"@utils\": [\"src/utils/index.ts\"], },\n  },\n}\n",
		"src/utils/index.ts": "export function fmt(x: number) { return x; }\n",
		"src/logic/a.ts":     "import { fmt } from '@/utils';\nimport { fmt as f2 } from '@utils';\nimport { helper } from 'src/logic/b';\nimport lodash from 'lodash';\nexport const a = fmt(1) + f2(2) + helper() + lodash.x;\n",
		"src/logic/b.ts":     "export function helper() { return 1; }\n",
	})
	for _, e := range []expectation{
		{"src/logic/a.ts", "fmt", "call", store.Resolved, "fmt", "src/utils/index.ts"},
		{"src/logic/a.ts", "f2", "call", store.Resolved, "fmt", "src/utils/index.ts"},
		{"src/logic/a.ts", "helper", "call", store.Resolved, "helper", "src/logic/b.ts"},
		{"src/logic/a.ts", "lodash", "import", store.External, "", ""},
	} {
		check(t, st, e)
	}
}

// TestInferredReturnsAndContainers: uma função TS sem anotação leva a
// cadeia pelo `return new X()` / `return f()`; containers Java e TS
// entregam o elemento em get()/orElseThrow()/find().
func TestInferredReturnsAndContainers(t *testing.T) {
	const owner = "src/com/acme/owner/"
	st := index(t, map[string]string{
		"src/svc.ts": "export class Svc {\n  total(): number { return 1; }\n}\nexport function makeSvc() { return new Svc(); }\nexport function viaCall() { return makeSvc(); }\n" +
			"export class Repo {\n  private cache: Svc[] = [];\n  first() { return this.cache; }\n  all(): Svc[] { return this.cache; }\n  async load(): Promise<Svc> { return new Svc(); }\n}\n",
		"src/ctl.ts":         "import { makeSvc, viaCall, Repo } from './svc';\nexport function run(repo: Repo) {\n  makeSvc().total();\n  viaCall().total();\n  repo.all().find((s) => s).total();\n  repo.first().pop().total();\n  repo.load().then(() => 1).total();\n  repo.all().length;\n}\n",
		owner + "Owner.java": "package com.acme.owner;\nimport java.util.List;\nimport java.util.Optional;\npublic class Owner {\n  public String getLastName() { return \"\"; }\n}\n",
		owner + "Repo.java":  "package com.acme.owner;\nimport java.util.List;\nimport java.util.Optional;\npublic class Repo {\n  public List<Owner> all() { return null; }\n  public Optional<Owner> byId(int id) { return Optional.empty(); }\n}\n",
		owner + "Ctl.java":   "package com.acme.owner;\npublic class Ctl {\n  void run(Repo repo) {\n    repo.all().get(0).getLastName();\n    repo.byId(1).orElseThrow().getLastName();\n    repo.all().stream().findFirst().get().getLastName();\n    repo.all().size();\n    repo.byId(1).map(o -> o).get().getLastName();\n  }\n}\n",
	})
	ctl, _, err := st.FileByPath("src/ctl.ts")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(ctl.ID)
	require.NoError(t, err)
	var totals []string
	for _, r := range refs {
		if r.Name == "total" {
			totals = append(totals, r.Resolution+":"+strings.Join(r.ReceiverPath, "."))
		}
		if r.Name == "length" {
			assert.Equal(t, store.External, r.Resolution, "array length is runtime")
		}
	}
	assert.ElementsMatch(t, []string{
		"resolved:makeSvc", "resolved:viaCall", "resolved:all.find", "resolved:first.pop", "resolved:load.then",
	}, totals)

	javaCtl, _, err := st.FileByPath(owner + "Ctl.java")
	require.NoError(t, err)
	javaRefs, err := st.RefsOfFile(javaCtl.ID)
	require.NoError(t, err)
	got := map[string]string{}
	for _, r := range javaRefs {
		if r.Name == "getLastName" || r.Name == "size" {
			got[r.Name+":"+strings.Join(r.ReceiverPath, ".")] = r.Resolution
		}
	}
	assert.Equal(t, map[string]string{
		"getLastName:all.get":                  store.Resolved,
		"getLastName:byId.orElseThrow":         store.Resolved,
		"getLastName:all.stream.findFirst.get": store.Resolved,
		"getLastName:byId.map.get":             store.External, // map() troca o tipo: não dá para seguir
		"size:all":                             store.External,
	}, got)
}

// TestGoResolution: imports do próprio módulo resolvem para o diretório;
// `pkg.Func()` acha o símbolo exportado do pacote; métodos espalhados por
// arquivos do mesmo pacote resolvem; struct embutida promove métodos;
// cadeias seguem tipos de retorno e elementos de slice.
func TestGoResolution(t *testing.T) {
	st := index(t, map[string]string{
		"go.mod":                   "module github.com/acme/app\n\ngo 1.22\n",
		"internal/model/person.go": "package model\n\ntype Person struct {\n\tName string\n}\n\nfunc (p *Person) LastName() string { return p.Name }\n",
		"internal/owner/owner.go":  "package owner\n\nimport \"github.com/acme/app/internal/model\"\n\ntype Owner struct {\n\tmodel.Person\n\tpets []*Pet\n}\n\ntype Pet struct{ Name string }\n\nfunc (p *Pet) AddVisit(v string) {}\n\nfunc New(name string) *Owner { return &Owner{pets: []*Pet{{Name: name}}} }\n",
		"internal/owner/repo.go":   "package owner\n\nfunc (o *Owner) FirstPet() *Pet { return o.pets[0] }\n\nfunc (o *Owner) Pets() []*Pet { return o.pets }\n",
		"cmd/app/main.go":          "package main\n\nimport (\n\t\"fmt\"\n\towners \"github.com/acme/app/internal/owner\"\n)\n\nvar defaultOwner = owners.New(\"d\")\n\nfunc main() {\n\tdefaultOwner.FirstPet().AddVisit(\"c\")\n\to := owners.New(\"x\")\n\to.FirstPet().AddVisit(\"v\")\n\to.Pets()[0].AddVisit(\"w\")\n\tfmt.Println(o.LastName(), o.pets)\n\tp := owners.Pet{}\n\tp.AddVisit(\"z\")\n\tfirst := o.FirstPet()\n\tfirst.AddVisit(\"a\")\n\tfor _, pet := range o.Pets() {\n\t\tpet.AddVisit(\"b\")\n\t}\n}\n",
	})
	for _, e := range []expectation{
		{"cmd/app/main.go", "New", "method", store.Resolved, "New", "internal/owner/owner.go"},
		{"cmd/app/main.go", "Pet", "new", store.Resolved, "Pet", "internal/owner/owner.go"},
		{"cmd/app/main.go", "Println", "method", store.External, "", ""},
		// o.LastName(): método promovido de model.Person (struct embutida).
		{"cmd/app/main.go", "LastName", "method", store.Resolved, "Person.LastName", "internal/model/person.go"},
		{"internal/owner/owner.go", "Person", "extends", store.Resolved, "Person", "internal/model/person.go"},
		{"internal/owner/repo.go", "pets", "property", store.Resolved, "Owner.pets", "internal/owner/owner.go"},
		{"internal/owner/owner.go", "Name", "property", store.Resolved, "Pet.Name", "internal/owner/owner.go"},
		{"internal/owner/owner.go", "pets", "property", store.Resolved, "Owner.pets", "internal/owner/owner.go"},
	} {
		check(t, st, e)
	}
	main, _, err := st.FileByPath("cmd/app/main.go")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(main.ID)
	require.NoError(t, err)
	var visits []string
	for _, r := range refs {
		if r.Name == "AddVisit" {
			visits = append(visits, r.Resolution+":"+strings.Join(r.ReceiverPath, "."))
		}
	}
	assert.ElementsMatch(t, []string{"resolved:FirstPet", "resolved:Pets.get", "resolved:", "resolved:FirstPet", "resolved:Pets.get", "resolved:FirstPet"}, visits,
		"o := owners.New(...) infers Owner; FirstPet() returns *Pet; Pets()[0] walks the slice")
}

// TestTypeAliasMembersResolve: membros de `type X = {…}` resolvem pelo alias,
// pelas bases de uma interseção e por objetos aninhados; um membro declarado
// nos dois lados de uma union fica ambíguo, e um receptor que o extrator não
// entende (`"v ".trim()`) não cai na função homônima.
func TestTypeAliasMembersResolve(t *testing.T) {
	st := index(t, map[string]string{
		"src/types.ts": "export type Base = Readonly<{ id: string; x: number }>;\n" +
			"export type Rect = Base & Readonly<{ type: \"rect\"; width: number }>;\n" +
			"export type Arrow = Base & Readonly<{ type: \"arrow\"; elbowed: boolean; customData?: { generationData?: string } }>;\n" +
			"export type Shape = Rect | Arrow;\n",
		"src/use.ts": "import { Arrow, Shape } from './types';\n" +
			"export function trim(v: string) { return v; }\n" +
			"export function f(a: Arrow, s: Shape) {\n" +
			"  return [a.elbowed, a.id, s.x, s.type, a.customData?.generationData, \"v \".trim()];\n}\n",
		"src/wrap.ts": "import { Arrow } from './types';\n" +
			"export type Holder = { arrow: Readonly<Arrow> | null };\n" +
			"export function g(h: Holder) { return h.arrow?.elbowed; }\n",
	})
	for _, e := range []expectation{
		{"src/use.ts", "elbowed", "property", store.Resolved, "Arrow.elbowed", "src/types.ts"},
		{"src/use.ts", "id", "property", store.Resolved, "Base.id", "src/types.ts"},
		{"src/use.ts", "x", "property", store.Resolved, "Base.x", "src/types.ts"},
		{"src/use.ts", "type", "property", store.Ambiguous, "", ""},
		{"src/use.ts", "generationData", "property", store.Resolved, "Arrow.customData.generationData", "src/types.ts"},
		{"src/use.ts", "trim", "method", store.Unresolved, "", ""},
		{"src/wrap.ts", "elbowed", "property", store.Resolved, "Arrow.elbowed", "src/types.ts"},
	} {
		check(t, st, e)
	}
}

// TestDestructuredAndReexportedImports: `export const { useAtom } = x` dá um
// símbolo a quem importa useAtom, e `export { atom }` de um nome importado de
// um pacote leva o import até o pacote (external) em vez de ficar pendente.
func TestDestructuredAndReexportedImports(t *testing.T) {
	st := index(t, map[string]string{
		"src/editor-jotai.ts": "import { atom } from 'jotai';\nimport * as jotai from 'jotai-scope';\n" +
			"export const { useAtom, useSetAtom } = jotai;\nexport { atom };\n",
		"src/use.ts":     "import { atom, useAtom } from './editor-jotai';\nexport const a = atom(0);\nexport function f() {\n  return useAtom(a);\n}\n",
		"src/Canvas.tsx": "function Canvas() {\n  return null;\n}\nexport default React.memo(Canvas);\n",
		"src/app.tsx":    "import Canvas from './Canvas';\nexport function App() {\n  return <Canvas />;\n}\n",
	})
	for _, e := range []expectation{
		{"src/use.ts", "atom", "import", store.External, "", ""},
		{"src/use.ts", "useAtom", "import", store.Resolved, "useAtom", "src/editor-jotai.ts"},
		{"src/use.ts", "useAtom", "call", store.Resolved, "useAtom", "src/editor-jotai.ts"},
		{"src/app.tsx", "Canvas", "import", store.Resolved, "Canvas", "src/Canvas.tsx"},
	} {
		check(t, st, e)
	}
}

// TestGoScopedReceivers: a mesma variável com tipos diferentes em duas
// funções resolve pelo tipo de cada uma; `(T{}).M()` usa o tipo do literal;
// um receptor que o extrator não entende fica unresolved, sem cair no tipo
// que tem o nome do método.
func TestGoScopedReceivers(t *testing.T) {
	st := index(t, map[string]string{
		"go.mod":           "module github.com/acme/app\n\ngo 1.22\n",
		"render/render.go": "package render\n\ntype Render interface{ Render() error }\n\ntype JSON struct{}\n\nfunc (JSON) Render() error { return nil }\n\ntype XML struct{}\n\nfunc (XML) Render() error { return nil }\n",
		"render/use.go":    "package render\n\nfunc a() {\n\tr := JSON{}\n\t_ = r.Render()\n}\n\nfunc b() {\n\tr := XML{}\n\t_ = r.Render()\n\t_ = (JSON{}).Render()\n\t_ = func() Render { return nil }().Render()\n}\n",
	})
	f, _, err := st.FileByPath("render/use.go")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(f.ID)
	require.NoError(t, err)
	var got []string
	for _, r := range refs {
		if r.Name != "Render" || r.Kind != "method" {
			continue
		}
		target := ""
		if r.ResolvedSymbolID != nil {
			sym, _, err := st.SymbolByID(*r.ResolvedSymbolID)
			require.NoError(t, err)
			target = sym.QualifiedName
		}
		got = append(got, fmt.Sprintf("%d %s %s", r.Line, r.Resolution, target))
	}
	assert.ElementsMatch(t, []string{"5 resolved JSON.Render", "10 resolved XML.Render", "11 resolved JSON.Render", "12 unresolved "}, got)
}

// TestPythonResolution: imports relativos e absolutos acham módulos e
// nomes; `self.attr` tipado resolve métodos; classes base resolvem
// membros herdados; alias de módulo resolve funções; cadeias seguem `->`.
func TestPythonResolution(t *testing.T) {
	st := index(t, map[string]string{
		"app/__init__.py":        "",
		"app/models/__init__.py": "from .person import Person\n",
		"app/models/person.py":   "class Person:\n    def last_name(self) -> str:\n        return \"\"\n",
		"app/models/pet.py":      "class Pet:\n    def add_visit(self, v: str) -> None:\n        pass\n",
		"app/owner.py":           "from .models import Person\nfrom .models.pet import Pet\nfrom app.repo import Repo\nimport app.util as util\n\nclass Owner(Person):\n    def __init__(self, repo: Repo):\n        self.repo = repo\n        self.pets: list[Pet] = []\n\n    def first(self) -> Pet:\n        return self.pets[0]\n\n    def run(self):\n        self.repo.save(self)\n        self.first().add_visit(\"v\")\n        self.pets[0].add_visit(\"w\")\n        self.last_name()\n        util.slug(self)\n        Owner(self.repo).first()\n",
		"app/repo.py":            "class Repo:\n    def save(self, o) -> None:\n        pass\n",
		"app/util.py":            "def slug(o) -> str:\n    return \"\"\n",
		"tests/test_owner.py":    "from app.owner import Owner\nimport requests\n\ndef test_it():\n    Owner(None).run()\n    requests.get(\"x\")\n",
		"app/nodes.py":           "class Expr:\n    pass\n",
		"app/meta.py":            "from . import nodes\n\ndef find(n: nodes.Expr) -> nodes.Expr:\n    return n\n",
	})
	for _, e := range []expectation{
		{"app/owner.py", "Person", "extends", store.Resolved, "Person", "app/models/person.py"},
		{"app/owner.py", "save", "method", store.Resolved, "Repo.save", "app/repo.py"},
		{"app/owner.py", "last_name", "method", store.Resolved, "Person.last_name", "app/models/person.py"},
		{"app/owner.py", "slug", "method", store.Resolved, "slug", "app/util.py"},
		{"tests/test_owner.py", "Owner", "call", store.Resolved, "Owner", "app/owner.py"},
		{"tests/test_owner.py", "get", "method", store.External, "", ""},
		{"app/meta.py", "Expr", "type", store.Resolved, "Expr", "app/nodes.py"},
	} {
		check(t, st, e)
	}
	owner, _, err := st.FileByPath("app/owner.py")
	require.NoError(t, err)
	refs, err := st.RefsOfFile(owner.ID)
	require.NoError(t, err)
	var visits []string
	for _, r := range refs {
		if r.Name == "add_visit" {
			visits = append(visits, r.Resolution+":"+strings.Join(r.ReceiverPath, "."))
		}
	}
	assert.ElementsMatch(t, []string{"resolved:first", "resolved:get"}, visits,
		"self.first() -> Pet and self.pets[0] with pets: list[Pet]")
	var firsts []string
	for _, r := range refs {
		if r.Name == "first" {
			firsts = append(firsts, r.Resolution+":"+strings.Join(r.ReceiverPath, "."))
		}
	}
	assert.Contains(t, firsts, "resolved:", "Owner(self.repo).first(): a call chain starting at a class")
}

// TestPythonImportCyclesTerminate: pacotes que importam de si mesmos
// (`from . import sub` no __init__.py) e wildcards cíclicos resolvem sem
// recursão sem fim, e os nomes chegam ao arquivo certo.
func TestPythonImportCyclesTerminate(t *testing.T) {
	st := index(t, map[string]string{
		"pkg/__init__.py": "from . import sub\nfrom .app import App\nfrom .helpers import *\n",
		"pkg/sub.py":      "from . import app\n\ndef helper():\n    return app.App()\n",
		"pkg/app.py":      "from . import sub\nfrom pkg import helpers\n\nclass App:\n    def run(self):\n        return sub.helper()\n",
		"pkg/helpers.py":  "from pkg import *\n\ndef shout():\n    return App()\n",
		"main.py":         "from pkg import App, sub, shout\n\nApp().run()\nsub.helper()\nshout()\n",
	})
	for _, e := range []expectation{
		{"main.py", "App", "call", store.Resolved, "App", "pkg/app.py"},
		{"main.py", "helper", "method", store.Resolved, "helper", "pkg/sub.py"},
		{"main.py", "shout", "call", store.Resolved, "shout", "pkg/helpers.py"},
		{"pkg/app.py", "helper", "method", store.Resolved, "helper", "pkg/sub.py"},
	} {
		check(t, st, e)
	}
}
