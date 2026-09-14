package editor

import (
	"fmt"
	"strings"

	"github.com/JonathanSantos/mira/internal/store"
)

// maxListed limita as ocorrências citadas numa mensagem de erro.
const maxListed = 8

// remove apaga a definição, levando junto o comentário de documentação
// logo acima e a linha em branco que sobraria. Se o repositório ainda usa
// o símbolo (ou um membro dele), recusa e diz onde, a menos que Force.
func (w *workspace) remove(res *Result, f *openFile, sym store.Symbol, req Request) error {
	if strings.TrimSpace(req.Text) != "" {
		return fmt.Errorf("delete takes no text")
	}
	refs, err := w.refsTo(sym, true)
	if err != nil {
		return err
	}
	start, end := deletionRange(f, sym)
	var outside []Site
	for _, r := range refs {
		if r.File == f.file.Path && r.Line >= start && r.Line <= end {
			continue // recursão ou uso interno: some junto com o símbolo
		}
		r.Reason = "still points to the deleted symbol"
		outside = append(outside, r)
	}
	if len(outside) > 0 && !req.Force {
		return fmt.Errorf("%s is still referenced %d time(s): %s; update those first, or pass force to delete anyway",
			sym.QualifiedName, len(outside), listSites(outside))
	}
	f.change(start, end, nil)
	res.StartLine, res.EndLine = start, end
	res.Dangling = outside
	return nil
}

// deletionRange estende o range do símbolo sobre o comentário colado nele
// e sobre as linhas em branco que ficariam sobrando, preservando o
// espaçamento que o arquivo já usa (uma linha em TS, duas no topo em
// Python).
func deletionRange(f *openFile, sym store.Symbol) (int, int) {
	start, end := sym.StartLine, sym.EndLine
	for start > 1 && isComment(f.line(start-1), f.file.Lang) {
		start--
	}
	blank := func(n int) bool { return strings.TrimSpace(f.line(n)) == "" }
	last := len(f.lines)
	if last > 0 && f.lines[last-1] == "" {
		last-- // o "" depois do \n final não é uma linha
	}
	above, below := 0, 0
	for start-above > 1 && blank(start-above-1) {
		above++
	}
	for end+below < last && blank(end+below+1) {
		below++
	}
	trimmed := func(n int) string { return strings.TrimSpace(f.line(n)) }
	switch {
	case end+below == last || start-above == 1:
		start, end = start-above, end+below // no começo ou no fim do arquivo não sobra branco
	case above == 0 && (strings.HasSuffix(trimmed(start-1), "{") || strings.HasSuffix(trimmed(start-1), ":")):
		end += below // primeiro membro de um bloco: o branco colaria na abertura
	case below == 0 && (strings.HasPrefix(trimmed(end+1), "}") || strings.HasPrefix(trimmed(end+1), ")")):
		start -= above // último membro de um bloco: o branco colaria no fechamento
	default:
		end += min(above, below)
	}
	return start, end
}

func listSites(sites []Site) string {
	var parts []string
	for i, s := range sites {
		if i == maxListed {
			parts = append(parts, fmt.Sprintf("and %d more", len(sites)-maxListed))
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%d", s.File, s.Line))
	}
	return strings.Join(parts, ", ")
}
