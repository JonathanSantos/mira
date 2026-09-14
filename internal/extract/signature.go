package extract

import (
	"strings"

	"github.com/JonathanSantos/mira/internal/parser"
)

// SignatureUntil devolve o texto do nó do começo até o início de `stop`
// (normalmente o corpo), com espaços colapsados. Se stop for nulo, usa o nó
// inteiro.
func SignatureUntil(node, stop parser.Node) string {
	src := node.Text()
	if !stop.IsNil() && stop.StartByte() > node.StartByte() {
		src = src[:stop.StartByte()-node.StartByte()]
	}
	return Collapse(src)
}

// Collapse normaliza espaços e quebras de linha para uma assinatura de
// uma linha só.
func Collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Trim remove os sufixos que sobram quando cortamos antes do corpo ("{", "=").
func Trim(sig string) string {
	sig = strings.TrimSpace(sig)
	sig = strings.TrimSuffix(sig, "{")
	sig = strings.TrimSuffix(sig, "=")
	return strings.TrimSpace(sig)
}
