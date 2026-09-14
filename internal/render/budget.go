package render

import (
	"fmt"
	"strings"
)

// bytesPerToken é a aproximação usada para o orçamento; código em inglês
// fica entre 3,5 e 4,5 bytes por token nos tokenizers atuais.
const bytesPerToken = 4

// Tokens estima os tokens de um texto.
func Tokens(text string) int {
	return (len(text) + bytesPerToken - 1) / bytesPerToken
}

// Budget corta o texto no fim de uma linha para caber em maxTokens e avisa
// quantas linhas ficaram de fora e como estreitar. maxTokens <= 0 = sem
// limite.
func Budget(text string, maxTokens int) string {
	if maxTokens <= 0 || Tokens(text) <= maxTokens {
		return text
	}
	limit := maxTokens * bytesPerToken
	cut := strings.LastIndex(text[:limit], "\n")
	if cut <= 0 {
		cut = limit
	}
	omitted := 0
	if rest := strings.Trim(text[cut:], "\n"); rest != "" {
		omitted = strings.Count(rest, "\n") + 1
	}
	return text[:cut] + fmt.Sprintf("\n… truncated at %d tokens (%d lines omitted); narrow with path/kind/exclude_tests or raise max_tokens\n", maxTokens, omitted)
}
