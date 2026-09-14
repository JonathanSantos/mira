package golang

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

const sample = `package owner

import (
	"fmt"
	svc "github.com/acme/app/service"
	"github.com/acme/app/model/v2"
)

// Owner é um dono de pets.
type Owner struct {
	model.Person
	Name string
	pets []*Pet
}

type Repo interface {
	Find(id int) (*Owner, error)
}

var Limit = 3

func (o *Owner) AddPet(p *Pet) error {
	o.pets = append(o.pets, p)
	r := svc.New(o.Name)
	x := Pet{}
	fmt.Println(r.Total(), x.Name, o.pets[0].Name)
	if Limit > 2 {
		panic("too many")
	}
	return nil
}

func New(name string) *Owner { return &Owner{Name: name} }
`

func run(t *testing.T, src string) extract.Result {
	t.Helper()
	tree, err := parser.New().Parse(lang.Go, []byte(src))
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
	assert.Equal(t, "owner", res.Package)
	var names []string
	for _, s := range res.Symbols {
		names = append(names, s.Kind+":"+s.QualifiedName)
	}
	assert.Equal(t, []string{
		"class:Owner", "field:Owner.Name", "field:Owner.pets", "interface:Repo", "method:Repo.Find",
		"variable:Limit", "method:Owner.AddPet", "function:New",
	}, names)
	owner := symbol(t, res, "Owner")
	assert.Equal(t, "type Owner struct", owner.Signature)
	assert.True(t, owner.Exported)
	assert.Equal(t, 10, owner.StartLine, "the span is the whole type declaration")
	pets := symbol(t, res, "Owner.pets")
	assert.False(t, pets.Exported)
	assert.Equal(t, "pets []*Pet", pets.Signature)
	add := symbol(t, res, "Owner.AddPet")
	assert.Equal(t, "func (o *Owner) AddPet(p *Pet) error", add.Signature)
	assert.Equal(t, "Owner", add.Container)
	assert.Equal(t, "func New(name string) *Owner", symbol(t, res, "New").Signature)
	assert.Equal(t, "Find(id int) (*Owner, error)", symbol(t, res, "Repo.Find").Signature)

	var imports []string
	for _, im := range res.Imports {
		imports = append(imports, im.LocalName+"="+im.Module)
	}
	assert.Equal(t, []string{"fmt=fmt", "svc=github.com/acme/app/service", "model=github.com/acme/app/model/v2"}, imports)
}

func TestRefs(t *testing.T) {
	res := run(t, sample)
	embedded := refsOf(res, "Person")
	require.Len(t, embedded, 1)
	assert.Equal(t, extract.RefExtends, embedded[0].Kind)
	assert.Equal(t, "model", embedded[0].Receiver)
	assert.Equal(t, PackageReceiver, embedded[0].ReceiverType)

	newCall := refsOf(res, "New")
	require.Len(t, newCall, 1, "svc.New(...) is a package call; the definition of New is not a ref")
	assert.Equal(t, extract.RefMethod, newCall[0].Kind)
	assert.Equal(t, "svc", newCall[0].Receiver)
	assert.Equal(t, PackageReceiver, newCall[0].ReceiverType)
	assert.Equal(t, 1, newCall[0].Arity)
	assert.Equal(t, []string{""}, newCall[0].ArgTypes, "o.Name is a field access: type unknown at extraction, the arg count is right")

	total := refsOf(res, "Total")
	require.Len(t, total, 1)
	assert.Equal(t, "r", total[0].Receiver)
	assert.Equal(t, "call:svc.New", total[0].ReceiverType, "r := svc.New(...): the resolver follows New's return type")

	names := refsOf(res, "Name")
	kinds := map[string]int{}
	for _, r := range names {
		if len(r.ReceiverPath) == 0 {
			kinds[r.Kind+"/"+r.Receiver+"/"+r.ReceiverType]++
		}
	}
	assert.Equal(t, 1, kinds["property/x/Pet"], "x := Pet{} gives x the type Pet")
	assert.Equal(t, 1, kinds["property/o/Owner"], "o.Name inside the method")
	var chained extract.Ref
	for _, r := range names {
		if len(r.ReceiverPath) > 0 {
			chained = r
		}
	}
	assert.Equal(t, []string{"pets", "get"}, chained.ReceiverPath, "o.pets[0].Name walks the slice element")

	lits := refsOf(res, "Pet")
	var kindsPet []string
	for _, r := range lits {
		kindsPet = append(kindsPet, r.Kind)
	}
	assert.ElementsMatch(t, []string{"type", "type", "new"}, kindsPet, "field type, param type, composite literal")
	assert.Equal(t, extract.RefNew, refsOf(res, "Owner")[len(refsOf(res, "Owner"))-1].Kind, "&Owner{...} is a composite literal")
	assert.Empty(t, refsOf(res, "append"), "builtins are not refs")
	println := refsOf(res, "Println")
	require.Len(t, println, 1)
	assert.Equal(t, "fmt", println[0].Receiver)
	assert.Equal(t, PackageReceiver, println[0].ReceiverType, "fmt.Println is a package call")
	limit := refsOf(res, "Limit")
	require.Len(t, limit, 1)
	assert.Equal(t, extract.RefIdentifier, limit[0].Kind)
}

func TestSkeleton(t *testing.T) {
	res := run(t, sample)
	tree, err := parser.New().Parse(lang.Go, []byte(sample))
	require.NoError(t, err)
	add := symbol(t, res, "Owner.AddPet")
	sk, ok := Skeleton(tree.Root().NamedDescendantForByteRange(add.StartByte, add.EndByte))
	require.True(t, ok)
	assert.Equal(t, 1, sk.Branches)
	assert.Equal(t, []extract.Point{{Line: 30, Text: "return nil"}}, sk.Returns)
	assert.Equal(t, []extract.Point{{Line: 28, Text: `panic("too many")`}}, sk.Throws)
	assert.Equal(t, []extract.Point{{Line: 24, Text: "r"}, {Line: 25, Text: "x"}}, sk.Locals)
	assert.True(t, sk.Declared["o"] && sk.Declared["p"] && sk.Declared["r"])
}

func TestPredeclaredReceivers(t *testing.T) {
	src := "package p\n\nfunc f(err error, s fmt.Stringer) string {\n\tif err != nil {\n\t\treturn err.Error()\n\t}\n\treturn s.String()\n}\n"
	res := run(t, src)
	assert.Empty(t, refsOf(res, "Error"), "err.Error(): error is a predeclared type, never repo code")
	assert.Len(t, refsOf(res, "String"), 1, "s.String(): fmt.Stringer is a package type")
}

func TestTypeAliasAndCompositeKeys(t *testing.T) {
	src := "package p\n\nimport \"bytes\"\n\ntype Completion = string\n\ntype Cmd struct{ Use string }\n\ntype Key int\n\nconst KeyA Key = 1\n\nfunc f() {\n\tc := &Cmd{Use: \"x\"}\n\tcs := []Cmd{{Use: \"y\"}}\n\tm := map[Key]int{KeyA: 1}\n\tbuf := new(bytes.Buffer)\n\tbuf.WriteString(c.Use)\n\t_, _, _ = c, cs, m\n}\n"
	res := run(t, src)
	assert.Equal(t, extract.KindType, symbol(t, res, "Completion").Kind, "type alias is a type symbol")
	var uses []string
	for _, r := range refsOf(res, "Use") {
		uses = append(uses, r.Kind+":"+r.ReceiverType)
	}
	assert.ElementsMatch(t, []string{"property:Cmd", "property:Cmd", "property:Cmd"}, uses, "literal keys and c.Use are fields of Cmd")
	require.Len(t, refsOf(res, "KeyA"), 1, "map keys stay identifier refs")
	assert.Equal(t, extract.RefIdentifier, refsOf(res, "KeyA")[0].Kind)
	require.Len(t, refsOf(res, "WriteString"), 1)
	assert.Equal(t, "bytes.Buffer", refsOf(res, "WriteString")[0].ReceiverType, "buf := new(bytes.Buffer)")
}

func TestRangeElementType(t *testing.T) {
	src := "package p\n\ntype Cmd struct{}\n\nfunc (c *Cmd) Name() string { return \"\" }\n\nfunc f(cmds []*Cmd) {\n\tfor _, c := range cmds {\n\t\tc.Name()\n\t}\n\tfor i := range cmds {\n\t\t_ = i\n\t}\n}\n"
	res := run(t, src)
	require.Len(t, refsOf(res, "Name"), 1)
	assert.Equal(t, "Cmd", refsOf(res, "Name")[0].ReceiverType, "range value over []*Cmd is a *Cmd")
}

func TestReceiverFromDefiningExpression(t *testing.T) {
	src := `package p

type Command struct {
	Use      string
	commands []*Command
}

func (c *Command) Root() *Command { return c }

func (c *Command) Find(args []string) *Command { return c }

func (c *Command) sizes() int {
	rootCmd := c.Root()
	rootCmd.Find(nil)
	for _, command := range c.commands {
		_ = command.Use
	}
	return 0
}

func other(rootCmd *Command) {
	rootCmd.Find(nil)
}
`
	res := run(t, src)
	chain := func(r extract.Ref) string {
		return r.Receiver + ":" + r.ReceiverType + ":" + strings.Join(r.ReceiverPath, ".")
	}
	var finds []string
	for _, r := range refsOf(res, "Find") {
		finds = append(finds, chain(r))
	}
	assert.ElementsMatch(t, []string{"c:Command:Root", "rootCmd:Command:"}, finds,
		"rootCmd := c.Root() becomes the chain c.Root(); a parameter with the same name in another function keeps its own type")
	uses := refsOf(res, "Use")
	require.Len(t, uses, 1)
	assert.Equal(t, "c:Command:commands.get", chain(uses[0]), "a range value is an element of c.commands")
}

func TestVariadicAndPackageVarReceivers(t *testing.T) {
	src := `package p

import "github.com/spf13/cobra"

type Command struct{ Use string }

var rootCmd = &Command{}

func (c *Command) AddCommand(cmds ...*Command) {
	for _, x := range cmds {
		_ = x.Use
	}
	_ = rootCmd.Use
	_ = sharedCmd.Use
	_ = &cobra.Command{Use: "y"}
}
`
	res := run(t, src)
	var uses []string
	for _, r := range refsOf(res, "Use") {
		uses = append(uses, r.Receiver+":"+r.ReceiverType+":"+strings.Join(r.ReceiverPath, "."))
	}
	assert.ElementsMatch(t, []string{"x:Command:", "rootCmd:Command:", "sharedCmd:var:sharedCmd:", ":cobra.Command:"}, uses,
		"range over a variadic parameter yields its element; a package variable from another file is read by the resolver; "+
			"a key in a literal of another package's struct is that struct's field")
}

func TestScopedLocals(t *testing.T) {
	src := `package p

type A struct{}
type B struct{}

func (A) M() {}
func (B) M() {}

var v = 1

func f(x any, bs []B) {
	c := A{}
	c.M()
	if c, ok := x.(B); ok {
		c.M()
	}
	c.M()
	switch s := x.(type) {
	case *B:
		s.M()
	case A, B:
		s.M()
	}
	for _, c := range bs {
		c.M()
	}
	(A{}).M()
	_ = v
}

func g(c B) {
	c.M()
	v := 2
	_ = v
}
`
	res := run(t, src)
	var calls []string
	for _, r := range refsOf(res, "M") {
		calls = append(calls, r.Receiver+":"+r.ReceiverType)
	}
	assert.Equal(t, []string{"c:A", "c:B", "c:A", "s:B", "s:", "c:B", "A:A", "c:B"}, calls,
		"each use sees the declaration visible at its position: blocks, if initializers, type switch cases, range, other functions")
	vs := refsOf(res, "v")
	require.Len(t, vs, 1, "the package variable is used in f; g has its own local v")
	assert.Equal(t, 28, vs[0].Line)
	for _, name := range []string{"c", "s", "ok"} {
		assert.Empty(t, refsOf(res, name), "%s is a local, never a reference to a top-level name", name)
	}
}
