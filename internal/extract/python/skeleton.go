package python

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

var nestedScopes = []string{"function_definition", "lambda", "class_definition"}

// Skeleton monta a visão estrutural de uma função Python: `raise` entra
// como throw, `await` marca async.
func Skeleton(span parser.Node) (extract.Skeleton, bool) {
	fn := span
	if span.Is("decorated_definition") {
		fn = span.Child("function_definition")
	}
	if !fn.Is("function_definition") {
		return extract.Skeleton{}, false
	}
	sk := extract.Skeleton{Lines: span.EndLine() - span.StartLine() + 1, Declared: map[string]bool{}, Async: !fn.AnonChild("async").IsNil()}
	declareParams(fn, sk.Declared)
	body := fn.Child("block")
	if body.IsNil() {
		return sk, true
	}
	walkSkeleton(body, 0, &sk)
	return sk, true
}

func declareParams(fn parser.Node, declared map[string]bool) {
	for _, p := range fn.Child("parameters", "lambda_parameters").NamedChildren() {
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
			sk.Nested++
			declareParams(c, sk.Declared)
			walkSkeleton(c, nested+1, sk)
			continue
		}
		switch c.Type() {
		case "if_statement", "elif_clause", "conditional_expression", "except_clause", "match_statement":
			sk.Branches++
		case "for_statement", "while_statement", "for_in_clause":
			sk.Loops++
			if c.Is("for_statement", "for_in_clause") {
				declareNames(c.NamedChildren()[0], sk.Declared)
			}
		case "await":
			sk.Async = true
		case "return_statement":
			if nested == 0 {
				sk.Returns = append(sk.Returns, extract.PointOf(c))
			}
		case "raise_statement":
			if nested == 0 {
				sk.Throws = append(sk.Throws, extract.PointOf(c))
			}
		case "assignment", "augmented_assignment":
			target := c.NamedChildren()[0]
			declareNames(target, sk.Declared)
			if nested == 0 && target.Is("identifier") {
				local := extract.Point{Line: target.StartLine(), Text: target.Text()}
				if c.EndLine() > target.StartLine() {
					local.EndLine = c.EndLine()
				}
				sk.Locals = append(sk.Locals, local)
			}
		case "as_pattern_target":
			declareNames(c.NamedChildren()[0], sk.Declared)
		}
		walkSkeleton(c, nested, sk)
	}
}

func declareNames(target parser.Node, declared map[string]bool) {
	target.Walk(func(n parser.Node) bool {
		if n.Is("identifier") {
			declared[n.Text()] = true
		}
		return !n.Is("attribute", "subscript", "call")
	})
}
