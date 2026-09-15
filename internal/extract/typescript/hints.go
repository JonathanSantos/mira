package typescript

import (
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
)

// returnHints preenche ReturnHint das funções sem tipo de retorno anotado,
// olhando o primeiro `return` do escopo próprio. É a inferência mínima que
// faz `repo.get(id).total()` resolver quando `get` devolve `new Svc()`.
func (x *extraction) returnHints(root parser.Node) {
	for i := range x.result.Symbols {
		s := &x.result.Symbols[i]
		if s.Kind != extract.KindFunction && s.Kind != extract.KindMethod {
			continue
		}
		if extract.DeclaredType(s.Signature, s.Name, true, lang.TypeScript) != "" {
			continue // anotado: a assinatura já diz
		}
		fn := functionNode(root.NamedDescendantForByteRange(s.StartByte, s.EndByte))
		if fn.IsNil() {
			continue
		}
		s.ReturnHint = x.returnHint(fn)
	}
}

func (x *extraction) returnHint(fn parser.Node) string {
	body := functionBody(fn)
	for body.Is("arrow_function", "function_expression") {
		body = functionBody(body) // curried: o hint é o da função mais interna
	}
	if body.IsNil() {
		return ""
	}
	if !body.Is("statement_block") {
		return x.hintOf(body) // arrow concisa
	}
	hint := ""
	body.Walk(func(n parser.Node) bool {
		if hint != "" {
			return false
		}
		if n.Is(functionTypes...) && n.StartByte() != body.StartByte() {
			return false // o return de um callback não é o desta função
		}
		if n.Is("return_statement") {
			if value := lastNamedChild(n); !value.IsNil() {
				hint = x.hintOf(value)
			}
		}
		return true
	})
	return hint
}

// hintOf reduz a expressão devolvida a um nome de tipo ou a `call:f`.
func (x *extraction) hintOf(expr parser.Node) string {
	switch expr.Type() {
	case "new_expression":
		return expr.Child("identifier").Text()
	case "await_expression", "parenthesized_expression", "non_null_expression":
		return x.hintOf(lastNamedChild(expr))
	case "as_expression", "satisfies_expression":
		return typeName(lastNamedChild(expr))
	case "identifier":
		t, _ := x.typeOf(expr)
		return t
	case "member_expression":
		kids := expr.NamedChildren()
		if len(kids) == 2 && kids[0].Is("this") {
			return x.varTypes[kids[1].Text()]
		}
	case "call_expression":
		if fn := expr.NamedChildren()[0]; fn.Is("identifier") && !builtins[fn.Text()] && !x.isLocal(fn) {
			return "call:" + fn.Text()
		}
	}
	return ""
}
