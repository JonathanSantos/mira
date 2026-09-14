package editor

import (
	"fmt"
	"strings"

	"github.com/JonathanSantos/mira/internal/store"
)

// replaceIn troca um trecho exato dentro do range do símbolo. O agente
// manda só o que muda, não a função inteira; o trecho precisa aparecer uma
// vez (ou All) para a edição nunca cair no lugar errado, e só as linhas que
// de fato mudam são reescritas.
func (w *workspace) replaceIn(res *Result, f *openFile, sym store.Symbol, req Request) error {
	if req.Old == "" {
		return fmt.Errorf("replace-in needs the exact text to find inside %s", sym.QualifiedName)
	}
	if req.Old == req.Text {
		return fmt.Errorf("the old and new text are the same")
	}
	body := strings.Join(f.lines[sym.StartLine-1:sym.EndLine], "\n")
	switch count := strings.Count(body, req.Old); {
	case count == 0:
		return fmt.Errorf("text not found in %s (lines %d-%d); copy it exactly, indentation included", sym.QualifiedName, sym.StartLine, sym.EndLine)
	case count > 1 && !req.All:
		return fmt.Errorf("text found %d times in %s (lines %d-%d); include surrounding text to make it unique, or pass all",
			count, sym.QualifiedName, sym.StartLine, sym.EndLine)
	}
	oldLines := strings.Split(body, "\n")
	newLines := strings.Split(strings.ReplaceAll(body, req.Old, req.Text), "\n")
	head := commonPrefix(oldLines, newLines)
	tail := commonSuffix(oldLines[head:], newLines[head:])
	start, end := sym.StartLine+head, sym.StartLine+len(oldLines)-1-tail
	repl := newLines[head : len(newLines)-tail]
	f.change(start, end, repl)
	res.StartLine, res.EndLine, res.Lines = start, start+len(repl)-1, len(repl)
	if len(repl) == 0 {
		res.EndLine = end
	}
	incoming, err := w.refsTo(sym, false)
	if err != nil {
		return err
	}
	res.incoming = shifted(incoming, f.file.Path, start, end, len(repl))
	return nil
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func commonSuffix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}
