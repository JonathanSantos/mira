package java

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// nestedScopes abrem um escopo próprio dentro do corpo de um método:
// lambdas e classes anônimas.
var nestedScopes = []string{"lambda_expression", "class_body", "method_declaration", "constructor_declaration"}

// Skeleton monta a visão estrutural de um método ou construtor. Devolve
// false quando o nó não é um deles (campo, classe, enum).
func Skeleton(span parser.Node) (extract.Skeleton, bool) {
	if !span.Is("method_declaration", "constructor_declaration") {
		return extract.Skeleton{}, false
	}
	sk := extract.Skeleton{Lines: span.EndLine() - span.StartLine() + 1, Declared: map[string]bool{}}
	declareParams(span, sk.Declared)
	for _, t := range span.Child("throws").NamedChildren() {
		sk.DeclaredThrows = append(sk.DeclaredThrows, t.Text())
	}
	body := span.Child("block", "constructor_body")
	if body.IsNil() {
		return sk, true // abstrato ou de interface
	}
	walkSkeleton(body, 0, &sk)
	return sk, true
}

// declareParams marca os parâmetros de um método, construtor ou lambda.
func declareParams(fn parser.Node, declared map[string]bool) {
	if id := fn.Child("identifier"); fn.Is("lambda_expression") && !id.IsNil() {
		declared[id.Text()] = true // `x -> …`
	}
	for _, p := range fn.Child("formal_parameters", "inferred_parameters").NamedChildren() {
		if p.Is("identifier") {
			declared[p.Text()] = true
			continue
		}
		if id := p.Child("identifier"); !id.IsNil() {
			declared[id.Text()] = true
		}
	}
}

func walkSkeleton(n parser.Node, nested int, sk *extract.Skeleton) {
	for _, c := range n.NamedChildren() {
		if c.Is(nestedScopes...) {
			if !c.Is("class_body") {
				sk.Nested++ // os métodos da classe anônima são contados um a um
			}
			declareParams(c, sk.Declared)
			walkSkeleton(c, nested+1, sk)
			continue
		}
		switch c.Type() {
		case "catch_clause":
			sk.Branches++
			if id := c.Child("catch_formal_parameter").Child("identifier"); !id.IsNil() {
				sk.Declared[id.Text()] = true
			}
		case "enhanced_for_statement":
			sk.Loops++
			if id := c.Child("identifier"); !id.IsNil() {
				sk.Declared[id.Text()] = true
			}
		case "resource":
			if id := c.Child("identifier"); !id.IsNil() {
				sk.Declared[id.Text()] = true
			}
		case "if_statement", "switch_expression", "ternary_expression":
			sk.Branches++
		case "for_statement", "while_statement", "do_statement":
			sk.Loops++
		case "return_statement":
			if nested == 0 {
				sk.Returns = append(sk.Returns, extract.PointOf(c))
			}
		case "throw_statement":
			if nested == 0 {
				sk.Throws = append(sk.Throws, extract.PointOf(c))
			}
		case "local_variable_declaration":
			for _, d := range c.ChildrenOf("variable_declarator") {
				id := d.Child("identifier")
				if id.IsNil() {
					continue
				}
				sk.Declared[id.Text()] = true
				if nested == 0 {
					local := extract.Point{Line: id.StartLine(), Text: id.Text()}
					if value := lastNamed(d); value.StartByte() != id.StartByte() && value.EndLine() > id.StartLine() {
						local.EndLine = value.EndLine() // classe anônima, lambda ou initializer de várias linhas
					}
					sk.Locals = append(sk.Locals, local)
				}
			}
		}
		walkSkeleton(c, nested, sk)
	}
}
