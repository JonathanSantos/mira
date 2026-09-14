package extract

import "strings"

// Param é um parâmetro lido de uma assinatura.
type Param struct {
	Type     string // como escrito, sem o nome: `Integer`, `Map<String, Integer>`, `string[]`; "" quando não há
	Optional bool   // `x?: T` ou `x = valor` (TS)
	Rest     bool   // `...xs` (TS) ou `T... xs` (Java)
}

// Params lê a lista de parâmetros de uma assinatura. ok é false quando a
// assinatura não tem parênteses (campo, arrow com um parâmetro solto).
func Params(signature string) ([]Param, bool) {
	open := openParen(signature)
	if open < 0 {
		return nil, false
	}
	list, closed := balanced(signature, open)
	if !closed {
		return nil, false
	}
	var out []Param
	for _, raw := range splitTopLevel(list) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p := Param{Rest: isRest(raw), Optional: isOptional(raw)}
		if colon := topLevelIndex(raw, ':'); colon >= 0 {
			p.Type = strings.TrimSpace(cutDefault(raw[colon+1:])) // TS: `name: T = v`
		} else if !strings.Contains(raw, "=") {
			p.Type = javaParamType(raw)
		}
		out = append(out, p)
	}
	return out, true
}

// ParamBounds conta os parâmetros: o mínimo exigido (sem opcionais e rest)
// e o máximo aceito (-1 quando há rest ou varargs). É o que permite
// escolher entre sobrecargas pelo número de argumentos da chamada.
func ParamBounds(signature string) (minArgs, maxArgs int, ok bool) {
	params, ok := Params(signature)
	if !ok {
		return 0, 0, false
	}
	for _, p := range params {
		if p.Rest {
			return minArgs, -1, true
		}
		maxArgs++
		if !p.Optional {
			minArgs++
		}
	}
	return minArgs, maxArgs, true
}

// javaParamType tira anotações, `final` e o nome: `@Valid Visit visit` ->
// `Visit`, `String... args` -> `String`.
func javaParamType(raw string) string {
	raw = strings.ReplaceAll(raw, "...", " ")
	var tokens []string
	for _, tok := range strings.Fields(stripAnnotations(raw)) {
		if tok != "final" {
			tokens = append(tokens, tok)
		}
	}
	if len(tokens) < 2 {
		return ""
	}
	return strings.Join(tokens[:len(tokens)-1], " ")
}

// stripAnnotations remove `@Nome` e `@Nome(...)` de um parâmetro Java.
func stripAnnotations(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '@' {
			b.WriteByte(s[i])
			i++
			continue
		}
		for i < len(s) && s[i] != ' ' && s[i] != '(' {
			i++
		}
		if i < len(s) && s[i] == '(' {
			_, closed := balanced(s, i)
			if !closed {
				return b.String()
			}
			depth := 0
			for ; i < len(s); i++ {
				if s[i] == '(' {
					depth++
				} else if s[i] == ')' {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
			}
		}
	}
	return b.String()
}

// cutDefault corta `= valor` de um tipo TS (`T = {}` -> `T`).
func cutDefault(s string) string {
	if i := topLevelIndex(s, '='); i >= 0 && (i+1 >= len(s) || s[i+1] != '>') {
		return s[:i]
	}
	return s
}

// topLevelIndex acha a primeira ocorrência do byte fora de brackets.
func topLevelIndex(s string, want byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '(' || c == '[' || c == '{' || c == '<':
			depth++
		case c == '>' && i > 0 && s[i-1] == '=':
		case c == ')' || c == ']' || c == '}' || c == '>':
			depth--
		case c == want && depth == 0:
			return i
		}
	}
	return -1
}

// openParen acha o "(" da lista de parâmetros: o primeiro fora de generics
// (`f<T>(…)`, `const f: Fn<A, B> = (…) =>`).
func openParen(s string) int {
	angle := 0
	for i, c := range s {
		switch c {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '(':
			if angle == 0 {
				return i
			}
		}
	}
	return -1
}

// balanced devolve o texto entre o "(" em open e o ")" que o fecha,
// ignorando parênteses dentro de strings.
func balanced(s string, open int) (string, bool) {
	depth := 0
	var quote rune
	for i, c := range s[open:] {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[open+1 : open+i], true
			}
		}
	}
	return "", false
}

// splitTopLevel separa por vírgulas fora de (), [], {} e <>; o `=>` de um
// tipo função não fecha nada.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote rune
	for i, c := range s {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(' || c == '[' || c == '{' || c == '<':
			depth++
		case c == '>' && i > 0 && s[i-1] == '=':
		case c == ')' || c == ']' || c == '}' || c == '>':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// isRest cobre `...args` (TS) e `String... args` (Java).
func isRest(param string) bool {
	return strings.Contains(param, "...")
}

// isOptional cobre `x?: T` e `x = valor` (TS). O "=" tem de estar fora de
// parênteses para não confundir com `@RequestParam(defaultValue = "1")`.
func isOptional(param string) bool {
	head := param
	if i := strings.IndexAny(param, ":="); i >= 0 {
		head = param[:i]
	}
	if strings.HasSuffix(strings.TrimSpace(head), "?") {
		return true
	}
	return topLevelIndex(param, '=') >= 0 && !strings.Contains(param[topLevelIndex(param, '='):], "=>") ||
		hasDefaultBeforeArrow(param)
}

// hasDefaultBeforeArrow cobre `cb = () => 1`: há um `=` de default e depois
// um `=>` que pertence ao valor.
func hasDefaultBeforeArrow(param string) bool {
	i := topLevelIndex(param, '=')
	return i >= 0 && i+1 < len(param) && param[i+1] != '>'
}
