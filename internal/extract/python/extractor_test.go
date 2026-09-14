package python

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

const sample = `import os.path
import numpy as np
from .models import Owner as O, Pet
from ..util import *

LIMIT: int = 3
_private = 1

@dataclass
class Owner(Person, base.Base):
    name: str = ""

    def __init__(self, repo: Repo):
        self.repo = repo
        self.pets: list[Pet] = []

    @property
    def first(self) -> Pet:
        return self.pets[0]

    def add_pet(self, pet: Pet, count=1) -> Pet:
        self.pets.append(pet)
        self.repo.save(self)
        np.sum([1])
        helper(pet, count)
        return pet

def helper(x, *args, **kw):
    def inner(y):
        return y
    if x:
        raise ValueError("x")
    for i in range(3):
        pass
    owner = Owner(Repo())
    return owner.first.name
`

func run(t *testing.T, src string) extract.Result {
	t.Helper()
	tree, err := parser.New().Parse(lang.Python, []byte(src))
	require.NoError(t, err)
	require.False(t, tree.HasError, "parse error in fixture")
	return New().Extract(tree)
}

func symbol(t *testing.T, res extract.Result, qualified string) extract.Symbol {
	t.Helper()
	for _, s := range res.Symbols {
		if s.QualifiedName == qualified {
			return s
		}
	}
	require.Failf(t, "symbol not found", "%s in %+v", qualified, res.Symbols)
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

func TestSymbolsAndImports(t *testing.T) {
	res := run(t, sample)
	var names []string
	for _, s := range res.Symbols {
		names = append(names, s.Kind+":"+s.QualifiedName)
	}
	assert.Equal(t, []string{
		"variable:LIMIT", "variable:_private", "class:Owner", "field:Owner.name", "method:Owner.__init__",
		"field:Owner.repo", "field:Owner.pets", "method:Owner.first", "method:Owner.add_pet", "function:helper", "function:helper.inner",
	}, names, "self.repo / self.pets in __init__ are fields of Owner")
	assert.Equal(t, "repo: Repo", symbol(t, res, "Owner.repo").Signature)
	assert.Equal(t, "pets: Pet[]", symbol(t, res, "Owner.pets").Signature)
	owner := symbol(t, res, "Owner")
	assert.Equal(t, "class Owner(Person, base.Base)", owner.Signature)
	assert.Equal(t, []string{"@dataclass"}, owner.Annotations)
	assert.Equal(t, 9, owner.StartLine, "the span starts at the decorator")
	assert.False(t, symbol(t, res, "_private").Exported)
	add := symbol(t, res, "Owner.add_pet")
	assert.Equal(t, "def add_pet(self, pet: Pet, count=1) -> Pet", add.Signature)
	assert.Equal(t, "Owner", add.Container)
	assert.Equal(t, []string{"@property"}, symbol(t, res, "Owner.first").Annotations)
	assert.Equal(t, "LIMIT: int", symbol(t, res, "LIMIT").Signature)
	assert.Equal(t, "helper", symbol(t, res, "helper.inner").Container)

	var imports []string
	for _, im := range res.Imports {
		imports = append(imports, im.Module+"|"+im.ImportedName+"|"+im.LocalName)
	}
	assert.Equal(t, []string{"os.path||os", "numpy||np", ".models|Owner|O", ".models|Pet|Pet", "..util||"}, imports)
	assert.True(t, res.Imports[4].IsWildcard)
}

func TestRefs(t *testing.T) {
	res := run(t, sample)
	bases := map[string]extract.Ref{}
	for _, r := range res.Refs {
		if r.Kind == extract.RefExtends {
			bases[r.Name] = r
		}
	}
	require.Len(t, bases, 2)
	assert.Equal(t, "", bases["Person"].Receiver)
	assert.Equal(t, "base", bases["Base"].Receiver)
	assert.Equal(t, ModuleReceiver, bases["Base"].ReceiverType)

	save := refsOf(res, "save")
	require.Len(t, save, 1)
	assert.Equal(t, "repo", save[0].Receiver)
	assert.Equal(t, "Repo", save[0].ReceiverType, "self.repo = repo, and repo: Repo is annotated")
	assert.Equal(t, 1, save[0].Arity)

	appendRef := refsOf(res, "append")
	require.Len(t, appendRef, 1)
	assert.Equal(t, "pets", appendRef[0].Receiver)
	assert.Equal(t, "Pet[]", appendRef[0].ReceiverType, "self.pets: list[Pet]")

	sum := refsOf(res, "sum")
	require.Len(t, sum, 1)
	assert.Equal(t, "np", sum[0].Receiver)
	assert.Equal(t, ModuleReceiver, sum[0].ReceiverType)

	helper := refsOf(res, "helper")
	require.Len(t, helper, 1)
	assert.Equal(t, extract.RefCall, helper[0].Kind)
	assert.Equal(t, []string{"Pet", "int"}, helper[0].ArgTypes)

	var chained extract.Ref
	for _, r := range refsOf(res, "name") {
		if len(r.ReceiverPath) > 0 {
			chained = r
		}
	}
	assert.Equal(t, "owner", chained.Receiver)
	assert.Equal(t, "Owner", chained.ReceiverType, "owner = Owner(...) gives the type")
	assert.Equal(t, []string{"first"}, chained.ReceiverPath)

	var kinds []string
	for _, r := range refsOf(res, "Pet") {
		kinds = append(kinds, r.Kind)
	}
	assert.ElementsMatch(t, []string{"import", "type", "type", "type", "type"}, kinds, "import, list[Pet] attribute, -> Pet, pet: Pet, -> Pet")
	assert.Empty(t, refsOf(res, "property"), "builtin decorators are not refs; the annotation text stays on the symbol")
	assert.Empty(t, refsOf(res, "ValueError"), "builtins are not refs")
	assert.Empty(t, refsOf(res, "range"))
	limit := refsOf(res, "x")
	assert.Empty(t, limit, "parameters are locals")
}

func TestSkeleton(t *testing.T) {
	res := run(t, sample)
	tree, err := parser.New().Parse(lang.Python, []byte(sample))
	require.NoError(t, err)
	helper := symbol(t, res, "helper")
	sk, ok := Skeleton(tree.Root().NamedDescendantForByteRange(helper.StartByte, helper.EndByte))
	require.True(t, ok)
	assert.Equal(t, 1, sk.Nested, "inner")
	assert.Equal(t, 1, sk.Branches)
	assert.Equal(t, 1, sk.Loops)
	assert.Equal(t, []extract.Point{{Line: 36, Text: "return owner.first.name"}}, sk.Returns, "inner's return is not helper's")
	assert.Equal(t, []extract.Point{{Line: 32, Text: `raise ValueError("x")`}}, sk.Throws)
	assert.Equal(t, []extract.Point{{Line: 35, Text: "owner"}}, sk.Locals)
	for _, name := range []string{"x", "args", "kw", "i", "owner", "y"} {
		assert.True(t, sk.Declared[name], name)
	}
	first := symbol(t, res, "Owner.first")
	fsk, ok := Skeleton(tree.Root().NamedDescendantForByteRange(first.StartByte, first.EndByte))
	require.True(t, ok, "decorated methods")
	assert.Equal(t, 3, fsk.Lines)
}

func TestTypedSplatParameters(t *testing.T) {
	src := "import typing as t\n\ndef render(*args: t.Any, **kwargs: t.Any) -> None:\n    dict(*args, **kwargs)\n"
	res := run(t, src)
	assert.Empty(t, refsOf(res, "args"), "*args: t.Any is a local")
	assert.Empty(t, refsOf(res, "kwargs"), "**kwargs: t.Any is a local")
}
