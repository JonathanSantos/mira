// Package render produz o formato texto compacto que a CLI (modo texto) e o
// servidor MCP devolvem. JSON continua disponível na CLI com --json, mas o
// que o agente lê é isto: uma linha por item, caminho uma vez por grupo,
// sem chaves repetidas.
package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/editor"
	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/graph"
	"github.com/JonathanSantos/mira/internal/indexer"
)

// ResolveMany separa os resultados de cada nome com uma linha em branco.
func ResolveMany(results []graph.ResolveResult) string {
	parts := make([]string, 0, len(results))
	for _, res := range results {
		parts = append(parts, Resolve(res))
	}
	return strings.Join(parts, "\n")
}

// Resolve: cabeçalho da definição, anotações, assinatura, snippet indentado,
// callers/callees como referências de uma linha.
func Resolve(res graph.ResolveResult) string {
	var b strings.Builder
	if len(res.Definitions) == 0 {
		fmt.Fprintf(&b, "no definition found for %q\n", res.Query)
		if res.Hint != "" {
			b.WriteString(res.Hint + "\n")
		}
		return b.String()
	}
	for i, d := range res.Definitions {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s %s %s:%d-%d%s\n", d.Label(), d.Kind, d.File, d.StartLine, d.EndLine, flags(d.Exported, d.JSX))
		for _, ann := range d.Annotations {
			b.WriteString("  " + ann + "\n")
		}
		b.WriteString("  " + d.Signature + "\n")
		if d.ParseError {
			b.WriteString("  warning: file had parse errors; callers/callees may be incomplete\n")
		}
		if d.Window != "" {
			if d.WindowStart > d.StartLine {
				fmt.Fprintf(&b, "  … %d-%d\n", d.StartLine, d.WindowStart-1)
			}
			b.WriteString(numbered(d.Window, d.WindowStart, "  ") + "\n")
			if end := d.WindowStart + strings.Count(d.Window, "\n"); end < d.EndLine {
				fmt.Fprintf(&b, "  … %d-%d\n", end+1, d.EndLine)
			}
		} else if d.Body != "" {
			b.WriteString(numbered(d.Body, d.StartLine, "  ") + "\n")
		} else {
			b.WriteString(numbered(d.Snippet, d.StartLine, "  ") + "\n")
			shown := strings.Count(d.Snippet, "\n") + 1
			if omitted := d.EndLine - d.StartLine + 1 - shown; omitted > 0 {
				fmt.Fprintf(&b, "  … %d more lines: include-body, skeleton, or snippet %d-%d\n", omitted, d.StartLine+shown, d.EndLine)
			}
		}
		if len(d.Members) > 0 {
			fmt.Fprintf(&b, "  members (%d):\n", countOutline(d.Members))
			outline(&b, d.Members, 2)
		}
		related(&b, "callers", d.Callers, 1)
		if d.Skeleton != nil {
			skeleton(&b, d.Skeleton) // os usos do esqueleto cobrem os callees
		} else {
			related(&b, "callees", d.Callees, 1)
		}
		refList(&b, "referenced by", d.ReferencedBy)
		refList(&b, "extends", d.Extends)
		refList(&b, "implements", d.Implements)
		refList(&b, "extended by", d.ExtendedBy)
		refList(&b, "implemented by", d.ImplementedBy)
		refList(&b, "annotated by", d.AnnotatedBy)
		if d.Refs != nil {
			b.WriteString(refFiles(d.Refs, "  "))
		}
	}
	return b.String()
}

// skeleton é o bloco estrutural de uma função: uma linha de contagens,
// retornos e throws com linha, locais, funções aninhadas e os usos
// agrupados pela origem do alvo.
func skeleton(b *strings.Builder, sk *graph.SkeletonInfo) {
	if sk.Note != "" {
		b.WriteString("  skeleton: " + sk.Note + "\n")
	}
	if sk.Lines == 0 {
		return
	}
	fmt.Fprintf(b, "  skeleton: %d lines, %d branches, %d loops", sk.Lines, sk.Branches, sk.Loops)
	if sk.NestedCount > 0 {
		fmt.Fprintf(b, ", %d nested function(s)", sk.NestedCount)
	}
	if sk.Async {
		b.WriteString(", async")
	}
	b.WriteString("\n")
	points(b, "returns", sk.Returns, "")
	declared := ""
	if len(sk.DeclaredThrows) > 0 {
		declared = " declared " + strings.Join(sk.DeclaredThrows, ", ")
	}
	points(b, "throws", sk.Throws, declared)
	if len(sk.Locals) > 0 {
		items := make([]string, 0, len(sk.Locals))
		for _, l := range sk.Locals {
			if l.EndLine > l.Line {
				items = append(items, fmt.Sprintf("%s (%d-%d)", l.Text, l.Line, l.EndLine))
				continue
			}
			items = append(items, fmt.Sprintf("%s (%d)", l.Text, l.Line))
		}
		b.WriteString("  locals: " + strings.Join(items, ", ") + "\n")
	}
	if len(sk.Nested) > 0 {
		items := make([]string, 0, len(sk.Nested))
		for _, n := range sk.Nested {
			items = append(items, fmt.Sprintf("%s %d-%d", base(strings.ReplaceAll(n.Name, ".", "/")), n.StartLine, n.EndLine))
		}
		fmt.Fprintf(b, "  nested (%d): %s", sk.NestedTotal, strings.Join(items, ", "))
		if sk.NestedTotal > len(sk.Nested) {
			fmt.Fprintf(b, ", +%d more (see symbols)", sk.NestedTotal-len(sk.Nested))
		}
		b.WriteString("\n")
	}
	useBlock(b, "uses (same file)", sk.Uses.SameFile)
	useBlock(b, "uses (other files)", sk.Uses.OtherFiles)
	useBlock(b, "uses (external)", sk.Uses.External)
	useBlock(b, "outer", sk.Uses.Outer)
	useBlock(b, "unresolved", sk.Uses.Unresolved)
}

// points imprime "label (n)[extra]:" e uma instrução por linha. "returns"
// aparece mesmo vazio: "sem return" é informação.
func points(b *strings.Builder, label string, pts []extract.Point, extra string) {
	if len(pts) == 0 && extra == "" && label != "returns" {
		return
	}
	fmt.Fprintf(b, "  %s (%d)%s", label, len(pts), extra)
	if len(pts) == 0 {
		b.WriteString("\n")
		return
	}
	b.WriteString(":\n")
	for _, p := range pts {
		fmt.Fprintf(b, "    %d %s\n", p.Line, p.Text)
	}
}

// useBlock imprime um grupo de usos: `nome alvo (linhas)`, onde o alvo é
// `:linha` no mesmo arquivo ou `arquivo:linha` em outro.
func useBlock(b *strings.Builder, label string, uses []graph.Use) {
	if len(uses) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, u := range uses {
		b.WriteString("    " + useLine(u) + "\n")
	}
}

func useLine(u graph.Use) string {
	var sb strings.Builder
	sb.WriteString(u.Short())
	if u.Kind == "property" || u.Kind == "identifier" || u.Kind == "field" {
		sb.WriteString(" " + u.Kind)
	}
	switch {
	case u.File != "":
		fmt.Fprintf(&sb, " %s:%d", u.File, u.Line)
	case u.Line > 0:
		fmt.Fprintf(&sb, " :%d", u.Line)
	case u.Resolution != "":
		sb.WriteString(" [" + u.Resolution + "]")
	}
	lines := make([]string, 0, len(u.Lines))
	for _, l := range u.Lines {
		lines = append(lines, fmt.Sprint(l))
	}
	sb.WriteString(" (" + strings.Join(lines, ", "))
	if u.Count > len(u.Lines) {
		fmt.Fprintf(&sb, ", %d total", u.Count)
	}
	sb.WriteString(")")
	return sb.String()
}

// refFiles é o bloco "arquivo (n)" + "linha [kind] em container: texto".
func refFiles(res *graph.RefsResult, pad string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%srefs (%d", pad, res.Total)
	if res.Truncated {
		b.WriteString(", truncated")
	}
	b.WriteString("):\n")
	for _, f := range res.Files {
		fmt.Fprintf(&b, "%s  %s (%d)\n", pad, f.File, len(f.Refs))
		for _, r := range f.Refs {
			fmt.Fprintf(&b, "%s    %s\n", pad, refLine(r))
		}
	}
	return b.String()
}

// refLine é `linha [kind resolução -> alvo] in container: texto`. Alvo e
// container Java aparecem como `Classe.membro`: o nome completo já está no
// cabeçalho de targets e repeti-lo por linha dobrava o tamanho.
func refLine(r graph.Reference) string {
	mark := r.Kind
	if r.Resolution != "" {
		mark += " " + r.Resolution
	}
	if r.Target != "" {
		mark += " -> " + short(r.Target)
	}
	where := ""
	if r.Container != "" {
		where = " in " + short(r.Container)
		if r.ContainerStart > 0 {
			where += fmt.Sprintf(":%d-%d", r.ContainerStart, r.ContainerEnd)
		}
	}
	return fmt.Sprintf("%d [%s]%s: %s", r.Line, mark, where, r.Text)
}

// short reduz `a.b.c.Classe.membro` a `Classe.membro`; nomes com até dois
// segmentos ficam como estão.
func short(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) <= 2 {
		return name
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func related(b *strings.Builder, label string, rel []graph.Related, level int) {
	pad := strings.Repeat("  ", level)
	fmt.Fprintf(b, "%s%s (%d):\n", pad, label, len(rel))
	for _, r := range rel {
		fmt.Fprintf(b, "%s  %s\n", pad, Ref(r.SymbolRef))
		if len(r.Callers) > 0 {
			related(b, "callers", r.Callers, level+2)
		}
		if len(r.Callees) > 0 {
			related(b, "callees", r.Callees, level+2)
		}
	}
}

func refList(b *strings.Builder, label string, refs []graph.SymbolRef) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, r := range refs {
		b.WriteString("    " + Ref(r) + "\n")
	}
}

// Ref é a forma de uma linha de um símbolo: `nome kind arquivo:início-fim`.
func Ref(r graph.SymbolRef) string {
	return fmt.Sprintf("%s %s %s:%d-%d", r.Name, r.Kind, r.File, r.StartLine, r.EndLine)
}

// RefsMany separa os blocos de cada nome com uma linha em branco.
func RefsMany(results []graph.RefsResult) string {
	parts := make([]string, 0, len(results))
	for _, res := range results {
		parts = append(parts, Refs(res))
	}
	return strings.Join(parts, "\n")
}

// Refs: totais, alvos uma vez, depois cada arquivo com `linha kind texto`;
// resolução e alvo só quando não são os únicos possíveis.
func Refs(res graph.RefsResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "refs %s: %d total (%s) by kind (%s)", res.Name, res.Total, counts(res.Summary), counts(res.ByKind))
	if res.Truncated {
		b.WriteString(" [truncated; narrow with --path, --kind or --exclude-tests]")
	}
	b.WriteString("\n")
	if len(res.Targets) > 0 {
		b.WriteString("targets:\n")
		for _, t := range res.Targets {
			b.WriteString("  " + Ref(t) + "\n")
		}
	}
	if len(res.Unmatched) > 0 {
		member := res.Name[strings.LastIndex(res.Name, ".")+1:]
		if i := strings.Index(member, ":"); i >= 0 {
			member = member[:i] // `Owner.getPet:126`
		}
		fmt.Fprintf(&b, "other refs named %s that may be it (%s): refs %s lists them\n", member, counts(res.Unmatched), member)
	}
	if len(res.Candidates) > 0 {
		b.WriteString("candidates (ambiguous refs could be any of these):\n")
		for _, c := range res.Candidates {
			b.WriteString("  " + Ref(c) + "\n")
		}
	}
	for _, f := range res.Files {
		fmt.Fprintf(&b, "%s (%d)\n", f.File, len(f.Refs))
		for _, r := range f.Refs {
			b.WriteString("  " + refLine(r) + "\n")
		}
	}
	return b.String()
}

func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// searchMarks: D definição, s literal de string, c comentário, t arquivo
// de texto, ~ sub-token de identificador composto, espaço = identificador.
var searchMarks = map[string]string{"string": "s", "comment": "c", "text": "t", "part": "~"}

// Search: arquivo (contagem), depois marca, linha, símbolo que envolve e
// texto; com contexto, a janela numerada ao redor com `>` na linha do hit.
func Search(res graph.SearchResult) string {
	var b strings.Builder
	for _, f := range res.Files {
		fmt.Fprintf(&b, "%s (%d)\n", f.File, f.Count)
		for _, h := range f.Hits {
			mark := " "
			if h.Definition {
				mark = "D"
			} else if m, ok := searchMarks[h.Kind]; ok {
				mark = m
			}
			where := ""
			if h.Container != "" {
				where = fmt.Sprintf(" in %s:%d-%d", short(h.Container), h.ContainerStart, h.ContainerEnd)
			}
			if h.Context == "" {
				fmt.Fprintf(&b, "  %s %d%s: %s\n", mark, h.Line, where, h.Text)
				continue
			}
			fmt.Fprintf(&b, "  %s %d%s\n", mark, h.Line, where)
			for i, line := range strings.Split(h.Context, "\n") {
				n := h.ContextStart + i
				pointer := " "
				if n == h.Line {
					pointer = ">"
				}
				fmt.Fprintf(&b, "    %s %d| %s\n", pointer, n, line)
			}
		}
		if f.Truncated {
			b.WriteString("  ...\n")
		}
	}
	fmt.Fprintf(&b, "%d hit(s) in %d file(s)", res.Total, len(res.Files))
	if res.Truncated {
		b.WriteString(" [truncated; narrow with --path or --exclude-tests]")
	}
	b.WriteString("\n")
	if res.Hint != "" {
		b.WriteString(res.Hint + "\n")
	}
	return b.String()
}

// Symbols: arquivo uma vez, uma linha por símbolo com filhos indentados.
func Symbols(res graph.SymbolsResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d symbols)\n", res.File, res.Count)
	if res.ParseError {
		b.WriteString("warning: " + res.Hint + "\n")
	}
	outline(&b, res.Symbols, 0)
	return b.String()
}

func countOutline(items []graph.Outline) int {
	n := 0
	for _, o := range items {
		n += 1 + countOutline(o.Children)
	}
	return n
}

func outline(b *strings.Builder, items []graph.Outline, level int) {
	pad := strings.Repeat("  ", level)
	for _, o := range items {
		fmt.Fprintf(b, "%s%d-%d %s %s%s", pad, o.StartLine, o.EndLine, o.Kind, o.Name, flags(o.Exported, o.JSX))
		if len(o.Annotations) > 0 {
			b.WriteString("  " + strings.Join(o.Annotations, " "))
		}
		if o.Signature != "" {
			b.WriteString("  " + o.Signature)
		}
		b.WriteString("\n")
		outline(b, o.Children, level+1)
	}
}

// Files: árvore com totais.
func Files(res graph.FilesResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s files=%d symbols=%d\n", res.Tree.Path, res.Files, res.Symbols)
	tree(&b, res.Tree, 1)
	return b.String()
}

func tree(b *strings.Builder, dir graph.DirEntry, level int) {
	pad := strings.Repeat("  ", level)
	for _, d := range dir.Dirs {
		fmt.Fprintf(b, "%s%s/ files=%d symbols=%d\n", pad, d.Label, d.Files, d.Symbols)
		tree(b, d, level+1)
	}
	for _, f := range dir.Entries {
		flag := ""
		if f.ParseError {
			flag = " [parse error]"
		}
		fmt.Fprintf(b, "%s%s symbols=%d%s\n", pad, base(f.Path), f.Symbols, flag)
	}
}

// Snippet: cabeçalho `arquivo:início-fim` e as linhas numeradas.
func Snippet(res graph.SnippetResult) string {
	var b strings.Builder
	if res.Stale {
		b.WriteString("warning: file changed since last index\n")
	}
	fmt.Fprintf(&b, "%s:%d-%d", res.File, res.StartLine, res.EndLine)
	if res.Symbol != "" {
		fmt.Fprintf(&b, " (in %s:%d-%d)", short(res.Symbol), res.SymbolStart, res.SymbolEnd)
	}
	b.WriteString("\n")
	if res.Plain {
		b.WriteString(res.Text + "\n")
		return b.String()
	}
	b.WriteString(numbered(res.Text, res.StartLine, "") + "\n")
	return b.String()
}

// numbered prefixa cada linha com o número real no arquivo, alinhado à
// direita, para que o agente cite `arquivo:linha` sem contar.
func numbered(text string, start int, pad string) string {
	lines := strings.Split(text, "\n")
	width := len(fmt.Sprint(start + len(lines) - 1))
	for i := range lines {
		lines[i] = fmt.Sprintf("%s%*d| %s", pad, width, start+i, lines[i])
	}
	return strings.Join(lines, "\n")
}

// Edit: o que a edição fez (ou faria, no preview), o que ficou de fora e o
// que a verificação achou depois de reindexar.
func Edit(res editor.Result) string {
	var b strings.Builder
	b.WriteString(editHeader(res))
	switch {
	case res.Preview:
		b.WriteString(res.Diff)
	case res.Action == editor.Rename:
		writeLinesByFile(&b, res.Updated)
	}
	if res.Checked > 0 {
		fmt.Fprintf(&b, "  verified: %d of %d references still resolve\n", res.Checked-len(res.Dangling), res.Checked)
	}
	writeEditSites(&b, "dangling", res.Dangling)
	writeEditSites(&b, "skipped", res.Skipped)
	for _, n := range res.Notes {
		fmt.Fprintf(&b, "  note: %s\n", n)
	}
	return b.String()
}

func editHeader(res editor.Result) string {
	tail := "; index refreshed"
	if res.Preview {
		tail = " (preview: nothing written)"
	}
	name := short(res.Symbol)
	switch res.Action {
	case editor.Rename:
		return fmt.Sprintf("%s %s -> %s: %d lines in %d files%s\n", res.Action, name, res.NewName, len(res.Updated), len(res.Files), tail)
	case editor.Delete:
		return fmt.Sprintf("%s %s: %s:%d-%d (%d lines removed)%s\n", res.Action, name, res.File, res.StartLine, res.EndLine, res.EndLine-res.StartLine+1, tail)
	}
	return fmt.Sprintf("%s %s: %s:%d-%d (%d lines)%s\n", res.Action, name, res.File, res.StartLine, res.EndLine, res.Lines, tail)
}

// maxLinesPerFile corta a lista de linhas de um arquivo muito tocado.
const maxLinesPerFile = 12

// writeLinesByFile lista as linhas tocadas agrupadas por arquivo: o agente
// sabe onde olhar sem receber o texto de volta.
func writeLinesByFile(b *strings.Builder, sites []editor.Site) {
	file := ""
	var lines []string
	flush := func() {
		if file == "" {
			return
		}
		more := ""
		if len(lines) > maxLinesPerFile {
			more = fmt.Sprintf(", … (+%d more)", len(lines)-maxLinesPerFile)
			lines = lines[:maxLinesPerFile]
		}
		fmt.Fprintf(b, "  %s: %s%s\n", file, strings.Join(lines, ", "), more)
	}
	for _, s := range sites {
		if s.File != file {
			flush()
			file, lines = s.File, nil
		}
		lines = append(lines, fmt.Sprint(s.Line))
	}
	flush()
}

func writeEditSites(b *strings.Builder, label string, sites []editor.Site) {
	if len(sites) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s (%d):\n", label, len(sites))
	for _, s := range sites {
		line := fmt.Sprintf("    %s:%d %s", s.File, s.Line, s.Reason)
		if s.Text != "" {
			line += ": " + s.Text
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
}

// Status: contagens.
func Status(st indexer.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "root: %s\nfiles=%d symbols=%d refs=%d imports=%d words=%d edges=%d\n",
		st.Root, st.Files, st.Symbols, st.Refs, st.Imports, st.Words, st.Edges)
	for _, l := range []string{"typescript", "javascript", "java", "python", "go", "text"} {
		if c, ok := st.ByLang[l]; ok {
			fmt.Fprintf(&b, "  %s files=%d symbols=%d refs=%d imports=%d\n", l, c.Files, c.Symbols, c.Refs, c.Imports)
		}
	}
	fmt.Fprintf(&b, "outdated files: %d\n", st.Outdated)
	return b.String()
}

// Report: resultado de uma indexação com timings.
func Report(r indexer.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "files: %d walked, %d new, %d changed, %d rehashed, %d unchanged, %d removed\n",
		r.Walked, r.New, r.Changed, r.Rehashed, r.Unchanged, r.Removed)
	if r.ParseErrors > 0 {
		fmt.Fprintf(&b, "parse errors: %d (files indexed with partial trees)\n", r.ParseErrors)
	}
	fmt.Fprintf(&b, "resolve: %d files, %d resolved, %d ambiguous, %d external, %d unresolved, %d edges\n",
		r.Resolve.Files, r.Resolve.Resolved, r.Resolve.Ambiguous, r.Resolve.External, r.Resolve.Unresolved, r.Resolve.Edges)
	b.WriteString("timings:")
	for _, p := range r.Phases {
		fmt.Fprintf(&b, " %s=%s", p.Name, p.Duration.Round(100_000))
	}
	fmt.Fprintf(&b, " total=%s\n", r.Total.Round(100_000))
	for _, w := range r.Warnings {
		b.WriteString("warning: " + w + "\n")
	}
	return b.String()
}

func flags(exported, jsx bool) string {
	var out []string
	if exported {
		out = append(out, "exported")
	}
	if jsx {
		out = append(out, "jsx")
	}
	if len(out) == 0 {
		return ""
	}
	return " [" + strings.Join(out, " ") + "]"
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
