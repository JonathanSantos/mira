package extract

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/lang"
)

// javaModifiers ficam antes do tipo numa assinatura Java.
var javaModifiers = map[string]bool{
	"public": true, "private": true, "protected": true, "static": true, "final": true, "abstract": true,
	"synchronized": true, "native": true, "default": true, "strictfp": true, "transient": true, "volatile": true,
}

// DeclaredType extrai de uma assinatura o tipo que um membro "entrega": o
// retorno de um método ou função (callable) ou o tipo de um campo ou
// propriedade. É o que permite seguir `a.b().c()`: o tipo de `b()` diz
// onde procurar `c`. Devolve "" quando a assinatura não declara (TS sem
// anotação, `var` Java, construtor).
func DeclaredType(signature, name string, callable bool, l lang.Lang) string {
	switch l {
	case lang.TypeScript, lang.JavaScript:
		return tsDeclaredType(signature, callable)
	case lang.Python:
		return pythonDeclaredType(signature, callable)
	case lang.Go:
		return goDeclaredType(signature, name, callable)
	}
	return javaDeclaredType(signature, name, callable)
}

// pythonDeclaredType lê `-> T` de um `def` ou `nome: T` de um atributo;
// `list[T]` vira T[], `Optional[T]` vira T.
func pythonDeclaredType(signature string, callable bool) string {
	if callable {
		i := strings.LastIndex(signature, "->")
		if i < 0 {
			return ""
		}
		return pythonType(strings.TrimSpace(signature[i+2:]))
	}
	colon := topLevelIndex(signature, ':')
	if colon < 0 {
		return ""
	}
	return pythonType(cutTypeEnd(strings.TrimSpace(signature[colon+1:])))
}

func pythonType(t string) string {
	t = strings.Trim(strings.TrimSpace(t), "\"'")
	open := strings.Index(t, "[")
	if open < 0 || !strings.HasSuffix(t, "]") {
		return t
	}
	base, inner := t[:open], pythonType(t[open+1:len(t)-1])
	if i := strings.LastIndex(inner, ","); i >= 0 {
		inner = strings.TrimSpace(inner[i+1:])
	}
	switch base {
	case "list", "List", "set", "Set", "Sequence", "Iterable", "Iterator", "tuple", "Tuple":
		return inner + "[]"
	case "Optional", "Awaitable", "Coroutine", "Type", "type":
		return inner
	}
	return base
}

// goDeclaredType lê o primeiro resultado de `func … Nome(…) T` (ou de
// `(T, error)`, ou `(o *T, err error)`) ou o tipo de um campo `nome T`;
// `*T` vira T, `[]*T` vira T[].
func goDeclaredType(signature, name string, callable bool) string {
	if !callable {
		i := strings.Index(signature, name+" ")
		if i < 0 {
			return ""
		}
		return goType(strings.TrimSpace(signature[i+len(name)+1:]))
	}
	i := strings.Index(signature, name+"(")
	if i < 0 {
		return ""
	}
	open := i + len(name)
	if _, ok := balanced(signature, open); !ok {
		return ""
	}
	depth, end := 0, -1
	for j := open; j < len(signature); j++ {
		if signature[j] == '(' {
			depth++
		} else if signature[j] == ')' {
			depth--
			if depth == 0 {
				end = j
				break
			}
		}
	}
	rest := strings.TrimSpace(signature[end+1:])
	if strings.HasPrefix(rest, "(") {
		inner, ok := balanced(rest, 0)
		if !ok {
			return ""
		}
		rest = strings.TrimSpace(splitTopLevel(inner)[0])
		if k := strings.LastIndex(rest, " "); k >= 0 {
			rest = rest[k+1:] // resultado nomeado: `(o *Owner, err error)`
		}
	}
	if k := topLevelIndex(rest, '{'); k >= 0 {
		rest = rest[:k]
	}
	return goType(strings.TrimSpace(rest))
}

func goType(t string) string {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "[]") {
		if inner := goType(t[2:]); inner != "" {
			return inner + "[]"
		}
		return ""
	}
	t = strings.TrimLeft(t, "*")
	if strings.HasPrefix(t, "map[") || strings.HasPrefix(t, "chan ") || strings.HasPrefix(t, "func(") || strings.HasPrefix(t, "<-") {
		return ""
	}
	if i := strings.Index(t, "["); i >= 0 {
		t = t[:i] // genérico: o nome basta
	}
	return t
}

func tsDeclaredType(signature string, callable bool) string {
	if callable {
		open := openParen(signature)
		if open < 0 {
			return ""
		}
		_, closed := balanced(signature, open)
		if !closed {
			return ""
		}
		rest := signature[open:]
		depth := 0
		end := -1
		for i := 0; i < len(rest); i++ {
			if rest[i] == '(' {
				depth++
			} else if rest[i] == ')' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		after := strings.TrimSpace(rest[end+1:])
		if !strings.HasPrefix(after, ":") {
			return ""
		}
		return cutTypeEnd(strings.TrimSpace(after[1:]))
	}
	colon := topLevelIndex(signature, ':')
	if colon < 0 {
		return ""
	}
	return cutTypeEnd(strings.TrimSpace(signature[colon+1:]))
}

// cutTypeEnd corta o tipo onde a assinatura continua (`=>`, `{`, `=`).
func cutTypeEnd(t string) string {
	if i := strings.Index(t, "=>"); i >= 0 {
		t = t[:i]
	}
	if i := topLevelIndex(t, '='); i >= 0 {
		t = t[:i]
	}
	if i := topLevelIndex(t, '{'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), ";"))
}

func javaDeclaredType(signature, name string, callable bool) string {
	head := signature
	if callable {
		i := strings.Index(signature, name+"(")
		if i < 0 {
			return ""
		}
		head = signature[:i]
	} else if i := strings.LastIndex(signature, " "+name); i >= 0 {
		head = signature[:i]
	}
	var tokens []string
	for _, tok := range strings.Fields(stripAnnotations(head)) {
		if javaModifiers[tok] || strings.HasPrefix(tok, "<") {
			continue // modificador ou parâmetro de tipo `<T>`
		}
		tokens = append(tokens, tok)
	}
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " ")
}
