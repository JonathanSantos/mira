package editor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/store"
)

// maxDiffLines corta cada lado de um bloco do diff: o agente precisa ver
// onde a mudança cai, não reler o texto que ele mesmo escreveu.
const maxDiffLines = 10

// Um preview com mais de maxPreviewChanges mudanças mostra só as
// previewPerFile primeiras de cada arquivo: um rename de 377 linhas em diff
// completo passa de 40 KB e estouraria o orçamento de uma resposta MCP.
const (
	maxPreviewChanges = 40
	previewPerFile    = 3
)

// diff mostra cada mudança com os números de linha do arquivo original:
// `-` sai, `+` entra. Renomes são uma linha cada; blocos longos são
// cortados nas pontas.
func (w *workspace) diff() string {
	total := 0
	for _, rel := range w.order {
		total += len(w.files[rel].changes)
	}
	var b strings.Builder
	for _, rel := range w.order {
		f := w.files[rel]
		if len(f.changes) == 0 {
			continue
		}
		b.WriteString(rel + "\n")
		changes := append([]change(nil), f.changes...)
		sort.Slice(changes, func(i, j int) bool { return changes[i].start < changes[j].start })
		for i, c := range changes {
			if total > maxPreviewChanges && i == previewPerFile {
				fmt.Fprintf(&b, "  … %d more changes in this file\n", len(changes)-previewPerFile)
				break
			}
			var old []string
			if c.end >= c.start {
				old = f.lines[c.start-1 : c.end]
			}
			if len(old) != 1 || len(c.repl) != 1 {
				switch {
				case len(old) == 0:
					fmt.Fprintf(&b, "  @@ insert %d lines at %d\n", len(c.repl), c.start)
				case len(c.repl) == 0:
					fmt.Fprintf(&b, "  @@ delete %d-%d\n", c.start, c.end)
				default:
					fmt.Fprintf(&b, "  @@ %d-%d -> %d lines\n", c.start, c.end, len(c.repl))
				}
			}
			writeSide(&b, "-", c.start, old)
			writeSide(&b, "+", c.start, c.repl)
		}
	}
	return b.String()
}

func writeSide(b *strings.Builder, mark string, first int, lines []string) {
	for i, line := range lines {
		if i == maxDiffLines && len(lines) > maxDiffLines+1 {
			fmt.Fprintf(b, "  %s     … %d more lines\n", mark, len(lines)-maxDiffLines)
			return
		}
		fmt.Fprintf(b, "  %s%4d| %s\n", mark, first+i, line)
	}
}

// Verify roda depois da reindexação: diz se os arquivos editados ainda
// parseiam e se as referências que apontavam para o símbolo continuam
// resolvendo. É a checagem que o agente faria com mais uma consulta.
func Verify(st *store.Store, res *Result) error {
	if res.Preview {
		return nil
	}
	for _, fc := range res.Files {
		f, ok, err := st.FileByPath(fc.File)
		if err != nil {
			return err
		}
		if ok && f.ParseError {
			res.Notes = append(res.Notes, fc.File+": does not parse after the edit (syntax error?)")
		}
	}
	byFile := map[string][]Site{}
	var files []string
	for _, s := range res.incoming {
		if byFile[s.File] == nil {
			files = append(files, s.File)
		}
		byFile[s.File] = append(byFile[s.File], s)
	}
	sort.Strings(files)
	for _, path := range files {
		f, ok, err := st.FileByPath(path)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		refs, err := st.RefsOfFile(f.ID)
		if err != nil {
			return err
		}
		resolved := map[string]bool{}
		for _, r := range refs {
			if r.Resolution == store.Resolved {
				resolved[fmt.Sprintf("%d:%s", r.Line, r.Name)] = true
			}
		}
		for _, s := range byFile[path] {
			if !resolved[fmt.Sprintf("%d:%s", s.Line, s.Name)] {
				s.Reason = "no longer resolves after the edit"
				res.Dangling = append(res.Dangling, s)
			}
		}
	}
	res.Checked = len(res.incoming)
	return nil
}
