package golang

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/parser"
)

// Skeleton monta a visão estrutural de uma função ou método Go. `panic`
// entra como throw.
func Skeleton(span parser.Node) (extract.Skeleton, bool) {
	if !span.Is("function_declaration", "method_declaration", "func_literal") {
		return extract.Skeleton{}, false
	}
	sk := extract.Skeleton{Lines: span.EndLine() - span.StartLine() + 1, Declared: map[string]bool{}}
	for _, list := range span.ChildrenOf("parameter_list") {
		for _, p := range list.ChildrenOf("parameter_declaration", "variadic_parameter_declaration") {
			for _, id := range p.ChildrenOf("identifier") {
				sk.Declared[id.Text()] = true
			}
		}
	}
	body := span.Child("block")
	if body.IsNil() {
		return sk, true
	}
	walkSkeleton(body, 0, &sk)
	return sk, true
}

func walkSkeleton(n parser.Node, nested int, sk *extract.Skeleton) {
	for _, c := range n.NamedChildren() {
		if c.Is("func_literal") {
			sk.Nested++
			for _, p := range c.Child("parameter_list").ChildrenOf("parameter_declaration") {
				for _, id := range p.ChildrenOf("identifier") {
					sk.Declared[id.Text()] = true
				}
			}
			walkSkeleton(c, nested+1, sk)
			continue
		}
		switch c.Type() {
		case "if_statement", "expression_switch_statement", "type_switch_statement", "select_statement", "expression_case", "type_case", "communication_case":
			if !c.Is("expression_case", "type_case", "communication_case") {
				sk.Branches++
			}
		case "for_statement":
			sk.Loops++
		case "go_statement":
			sk.Async = true
		case "return_statement":
			if nested == 0 {
				sk.Returns = append(sk.Returns, extract.PointOf(c))
			}
		case "call_expression":
			if fn := c.NamedChildren()[0]; fn.Is("identifier") && fn.Text() == "panic" && nested == 0 {
				sk.Throws = append(sk.Throws, extract.PointOf(c))
			}
		case "short_var_declaration":
			if left := c.Child("expression_list"); !left.IsNil() {
				for _, id := range left.ChildrenOf("identifier") {
					sk.Declared[id.Text()] = true
					if nested == 0 && id.Text() != "_" {
						local := extract.Point{Line: id.StartLine(), Text: id.Text()}
						if c.EndLine() > id.StartLine() {
							local.EndLine = c.EndLine()
						}
						sk.Locals = append(sk.Locals, local)
					}
				}
			}
		case "var_spec":
			for _, id := range c.ChildrenOf("identifier") {
				sk.Declared[id.Text()] = true
				if nested == 0 {
					sk.Locals = append(sk.Locals, extract.Point{Line: id.StartLine(), Text: id.Text()})
				}
			}
		case "range_clause":
			if left := c.Child("expression_list"); !left.IsNil() {
				for _, id := range left.ChildrenOf("identifier") {
					sk.Declared[id.Text()] = true
				}
			}
		}
		walkSkeleton(c, nested, sk)
	}
}
