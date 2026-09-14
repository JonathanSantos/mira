// Package extract define o contrato dos extractors por linguagem e os tipos
// que eles produzem. Os tipos são "puros" (sem ids de banco): o indexer os
// converte em linhas do store.
package extract

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/parser"
)

// Kinds de símbolo.
const (
	KindFunction    = "function"
	KindClass       = "class"
	KindMethod      = "method"
	KindConstructor = "constructor"
	KindInterface   = "interface"
	KindType        = "type"
	KindEnum        = "enum"
	KindRecord      = "record"
	KindAnnotation  = "annotation"
	KindVariable    = "variable"
	KindProperty    = "property"
	KindField       = "field"
	KindNamespace   = "namespace"
)

// Kinds de referência. O resolvedor deriva o kind da edge a partir deles.
const (
	RefCall       = "call"       // f(x)
	RefNew        = "new"        // new T()
	RefMethod     = "method"     // obj.m() ou T.m()
	RefType       = "type"       // anotação de tipo, generics
	RefIdentifier = "identifier" // identificador em posição de uso
	RefJSX        = "jsx"        // <Button />
	RefAnnotation = "annotation" // @Injectable() / @Override
	RefExtends    = "extends"
	RefImplements = "implements"
	RefImport     = "import"   // o próprio nome importado
	RefProperty   = "property" // acesso a campo/propriedade: obj.x, this.x
)

// Kinds de import.
const (
	ImportESM        = "esm"
	ImportCJS        = "cjs"
	ImportJava       = "java"
	ImportJavaStatic = "java_static"
)

// Symbol é uma definição encontrada no arquivo.
type Symbol struct {
	Name          string
	QualifiedName string
	Kind          string
	Container     string
	Signature     string
	Exported      bool
	ExportName    string // nome público quando exportado: "default", alias ou o próprio nome
	JSX           bool   // função que devolve JSX (componente React)
	StartLine     int
	EndLine       int
	StartByte     int
	EndByte       int
	NameLine      int      // linha em que o nome é declarado (para "definição primeiro" na busca)
	Annotations   []string // decorators/anotações que precedem a declaração, como no fonte
	// ReturnHint é o tipo de retorno inferido de uma função sem anotação
	// (TS): o nome de um tipo (`return new X()`, `return this.repo` com
	// campo tipado, `return x` com local tipado) ou `call:f` para "o que f
	// devolve", resolvido um nível na resolução. Vazio quando não dá.
	ReturnHint string
}

// Ref é um uso de um nome. Receiver/ReceiverType servem para chamadas de
// método: em `svc.total()`, Receiver é "svc" e ReceiverType o tipo declarado
// de svc no mesmo arquivo, quando inferível.
type Ref struct {
	Name         string
	Kind         string
	Line         int
	Col          int
	Receiver     string
	ReceiverType string
	Container    int // índice em Result.Symbols do símbolo que envolve a ref, ou -1
	Arity        int // número de argumentos de uma chamada, ou -1 (desconhecido/não é chamada)
	// ArgTypes é o tipo de cada argumento quando dá para saber (literal,
	// variável com tipo declarado, `new T()`), "" quando não; separa
	// sobrecargas de mesma aridade (`getPet(Integer)` vs `getPet(String)`).
	ArgTypes []string
	// ReceiverPath são os membros entre o receiver e o nome, numa chamada
	// encadeada: em `owner.getPet(id).addVisit(v)`, Receiver é "owner" e
	// ReceiverPath é [getPet]. Receiver vazio com path = cadeia que começa
	// numa função solta (`useForm().register`).
	ReceiverPath []string
}

// Import é uma dependência declarada. Para re-exports, LocalName é o nome
// exportado por este arquivo.
type Import struct {
	Kind         string
	Module       string
	ImportedName string // nome no módulo de origem; "default", "*" ou "" (side effect)
	LocalName    string
	IsReexport   bool
	IsWildcard   bool
	Line         int
}

// Result é tudo que um extractor tira de um arquivo.
type Result struct {
	Package string // package Java declarada no arquivo
	Symbols []Symbol
	Refs    []Ref
	Imports []Import
}

// Extractor transforma uma árvore em símbolos, refs e imports.
type Extractor interface {
	Extract(tree *parser.Tree) Result
}

// ContainerOf devolve o índice do símbolo mais interno cujo range de bytes
// cobre a posição, ou -1. É como refs ganham seu container.
func ContainerOf(symbols []Symbol, byteOffset int) int {
	best := -1
	for i, s := range symbols {
		if byteOffset < s.StartByte || byteOffset >= s.EndByte {
			continue
		}
		if best == -1 || s.EndByte-s.StartByte < symbols[best].EndByte-symbols[best].StartByte {
			best = i
		}
	}
	return best
}

// Skeleton é a visão estrutural de uma função, calculada sob demanda a
// partir da árvore (não fica no índice): pontos de retorno, throws, locais
// e contagens de controle de fluxo, sem o corpo. Returns, Throws e Locals
// consideram só o escopo próprio da função; funções aninhadas entram em
// Nested. Branches e Loops contam o corpo inteiro.
type Skeleton struct {
	Lines          int
	Branches       int // if, switch, ternário, catch
	Loops          int
	Nested         int // funções/lambdas/classes anônimas dentro do corpo
	Async          bool
	Returns        []Point
	Throws         []Point
	DeclaredThrows []string // cláusula throws (Java)
	Locals         []Point  // nome da variável e linha da declaração
	// Declared tem todo nome declarado dentro da função, em qualquer escopo
	// (parâmetros, locais, variáveis de laço e de catch, parâmetros de
	// lambdas): é o filtro que separa uso de fora de uso de um local.
	Declared map[string]bool
}

// Point é um trecho de código localizado por linha: o texto é a instrução
// colapsada numa linha e cortada em ExcerptLen.
type Point struct {
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"` // última linha quando o trecho ocupa várias (objeto literal, lambda)
	Text    string `json:"text"`
}

// ExcerptLen é o tamanho máximo do texto de um Point.
const ExcerptLen = 90

// ArgType limpa o que não serve como tipo de argumento: um `call:f` é uma
// pista de receiver, não um tipo comparável com parâmetros.
func ArgType(t string) string {
	if strings.HasPrefix(t, "call:") {
		return ""
	}
	return t
}

// Excerpt colapsa o texto numa linha e corta em ExcerptLen.
func Excerpt(text string) string {
	text = Collapse(text)
	if len(text) <= ExcerptLen {
		return text
	}
	return text[:ExcerptLen-1] + "…"
}

// PointOf monta um Point a partir de um nó.
func PointOf(n parser.Node) Point {
	return Point{Line: n.StartLine(), Text: Excerpt(n.Text())}
}
