package typescript

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// functionTypes são os nós que abrem um escopo próprio de função: return,
// throw e locais dentro deles pertencem à função aninhada, não à externa.
var functionTypes = []string{
	"function_declaration", "generator_function_declaration", "function_expression", "generator_function",
	"arrow_function", "method_definition", "class_declaration", "abstract_class_declaration", "class",
}

// Skeleton monta a visão estrutural da função cujo span é `span` (o nó
// gravado no índice: export_statement, lexical_declaration, method_definition
// ou a própria função). Devolve false quando o span não é uma função.
func Skeleton(span parser.Node) (extract.Skeleton, bool) {
	fn := functionNode(span)
	if fn.IsNil() {
		return extract.Skeleton{}, false
	}
	sk := extract.Skeleton{Lines: span.EndLine() - span.StartLine() + 1, Declared: map[string]bool{}}
	// Função curried `(a) => (b) => {…}`: o que interessa é o corpo mais
	// interno; cada nível contribui parâmetros e o async.
	body := functionBody(fn)
	for {
		sk.Async = sk.Async || !fn.AnonChild("async").IsNil()
		declareParams(fn, sk.Declared)
		if !body.Is("arrow_function", "function_expression") {
			break
		}
		fn, body = body, functionBody(body)
	}
	if body.IsNil() {
		return sk, true
	}
	if !body.Is("statement_block") {
		// Arrow concisa: a expressão inteira é o retorno.
		sk.Returns = append(sk.Returns, extract.PointOf(body))
	}
	walkSkeleton(body, 0, &sk)
	return sk, true
}

// declareParams marca os nomes dos parâmetros, inclusive os de um
// destructuring; o valor default de um parâmetro não é declaração.
func declareParams(fn parser.Node, declared map[string]bool) {
	if id := fn.Child("identifier"); fn.Is("arrow_function") && !id.IsNil() {
		declared[id.Text()] = true // `x => …`
	}
	for _, p := range fn.Child("formal_parameters").NamedChildren() {
		if kids := p.NamedChildren(); len(kids) > 0 && p.Is("required_parameter", "optional_parameter", "rest_parameter") {
			declareNames(kids[0], declared)
		}
	}
}

// declareNames marca todo identificador de um padrão (nome simples ou
// destructuring).
func declareNames(pattern parser.Node, declared map[string]bool) {
	pattern.Walk(func(n parser.Node) bool {
		if n.Is("identifier", "shorthand_property_identifier_pattern") {
			declared[n.Text()] = true
		}
		return true
	})
}

// functionNode desce do span até o nó da função: declaração exportada,
// variável com arrow/function expression, campo de classe com arrow,
// atribuição CommonJS ou a própria função.
func functionNode(span parser.Node) parser.Node {
	switch span.Type() {
	case "function_declaration", "generator_function_declaration", "function_expression", "generator_function",
		"arrow_function", "method_definition":
		return span
	case "export_statement":
		for _, c := range span.NamedChildren() {
			if fn := functionNode(c); !fn.IsNil() {
				return fn
			}
		}
	case "lexical_declaration", "variable_declaration":
		for _, d := range span.ChildrenOf("variable_declarator") {
			if fn := functionNode(lastNamedChild(d)); !fn.IsNil() {
				return fn
			}
		}
	case "public_field_definition", "field_definition":
		return functionNode(lastNamedChild(span))
	case "expression_statement":
		return functionNode(span.Child("assignment_expression"))
	case "assignment_expression":
		return functionNode(lastNamedChild(span))
	}
	return parser.Node{}
}

// walkSkeleton percorre o corpo. nested > 0 dentro de funções aninhadas:
// ali só contagens de fluxo entram no esqueleto da função externa.
func walkSkeleton(n parser.Node, nested int, sk *extract.Skeleton) {
	for _, c := range n.NamedChildren() {
		if c.Is(functionTypes...) {
			sk.Nested++
			declareParams(c, sk.Declared)
			walkSkeleton(c, nested+1, sk)
			continue
		}
		switch c.Type() {
		case "catch_clause":
			sk.Branches++
			declareNames(c.Child("identifier", "object_pattern", "array_pattern"), sk.Declared)
		case "for_in_statement":
			sk.Loops++
			if kids := c.NamedChildren(); len(kids) > 0 {
				declareNames(kids[0], sk.Declared)
			}
		case "if_statement", "switch_statement", "ternary_expression":
			sk.Branches++
		case "for_statement", "while_statement", "do_statement":
			sk.Loops++
		case "await_expression":
			sk.Async = true
		case "return_statement":
			if nested == 0 {
				sk.Returns = append(sk.Returns, extract.PointOf(c))
			}
		case "throw_statement":
			if nested == 0 {
				sk.Throws = append(sk.Throws, extract.PointOf(c))
			}
		case "variable_declarator":
			if len(c.NamedChildren()) == 0 {
				break
			}
			name := c.NamedChildren()[0]
			declareNames(name, sk.Declared)
			// Closures nomeadas (`const f = () => …`) são funções aninhadas,
			// não dados locais. Um valor de várias linhas (objeto literal)
			// leva o range, para o agente pedir o snippet certo.
			value := lastNamedChild(c)
			if nested == 0 && !value.Is("arrow_function", "function_expression") {
				local := extract.Point{Line: name.StartLine(), Text: extract.Excerpt(name.Text())}
				if value.StartByte() != name.StartByte() && value.EndLine() > name.StartLine() {
					local.EndLine = value.EndLine()
				}
				sk.Locals = append(sk.Locals, local)
			}
		}
		walkSkeleton(c, nested, sk)
	}
}
