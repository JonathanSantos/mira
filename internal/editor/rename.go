package editor

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
	"github.com/JonathanSantos/mira/internal/scanner"
	"github.com/JonathanSantos/mira/internal/store"
)

// maxHomonyms limita quantas ocorrências "talvez seja este" voltam listadas.
const maxHomonyms = 20

// site é uma posição candidata a renomear. col é a coluna (1-based, em
// bytes) onde o nó começa: a do próprio nome na maioria das refs, a do
// receptor quando o nó é `pkg.Name` / `mod.Name`.
type site struct {
	file     string
	line     int
	col      int
	receiver string
	decl     bool
	check    string // nome a conferir depois da edição ("" = nada a conferir)
}

// rename troca o nome na declaração e em cada referência resolvida para o
// símbolo, em todos os arquivos. Ocorrências do mesmo nome que não
// resolvem para ele ficam intocadas e voltam listadas quando podem ser
// ele: é exatamente o que um replace textual quebraria.
func (w *workspace) rename(res *Result, f *openFile, sym store.Symbol, req Request) error {
	newName := strings.TrimSpace(req.Text)
	if !isIdentifier(newName) {
		return fmt.Errorf("invalid new name %q: pass a plain identifier", newName)
	}
	if newName == sym.Name {
		return fmt.Errorf("%s is already named %s", sym.QualifiedName, newName)
	}
	if sym.Kind == extract.KindConstructor {
		return fmt.Errorf("%s is a constructor: rename the class %s and its constructors follow", sym.QualifiedName, sym.Container)
	}
	if taken, where, ok, err := w.nameTaken(f, sym, newName); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%s is already declared in the same scope (%s:%d); pick another name", taken.QualifiedName, where, taken.StartLine)
	}
	defaultExport := f.file.Lang.IsTS() && sym.ExportName == "default"
	if defaultExport && declarationColumn(f, sym, nameLine(sym)) == 0 {
		return fmt.Errorf("%s is an anonymous default export named after its file (%s): rename the file and its imports instead", sym.Name, f.file.Path)
	}
	res.NewName = newName
	targets, err := w.renameTargets(sym)
	if err != nil {
		return err
	}
	ids := map[int64]bool{}
	binds := map[string]bool{f.file.Path: true} // arquivos onde o nome simples aponta para o símbolo
	var sites []site
	for _, t := range targets {
		ids[t.ID] = true
		line := nameLine(t)
		sites = append(sites, site{file: f.file.Path, line: line, col: declarationColumn(f, t, line), decl: true})
		refs, err := w.store.RefsTo(t.ID)
		if err != nil {
			return err
		}
		for _, r := range refs {
			if r.Name != sym.Name {
				continue // alias local (`import { plus as add }`): o uso de `add` continua certo
			}
			file, ok, err := w.pathOf(r.FileID)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			sites = append(sites, site{file: file, line: r.Line, col: r.Col, receiver: r.Receiver, check: newName})
			if r.Receiver == "" {
				binds[file] = true
			}
		}
	}
	reexports, err := w.reexportSites(res, ids, sym.Name)
	if err != nil {
		return err
	}
	sites = append(sites, reexports...)
	sameName, err := w.store.RefsByName(sym.Name)
	if err != nil {
		return err
	}
	conflicts := map[string]bool{} // arquivos onde o mesmo nome simples também aponta para outra coisa
	for _, r := range sameName {
		if r.Receiver != "" || r.ResolvedSymbolID != nil && ids[*r.ResolvedSymbolID] {
			continue
		}
		if file, ok, err := w.pathOf(r.FileID); err != nil {
			return err
		} else if ok {
			conflicts[file] = true
		}
	}
	receivers, skipped, err := w.memberReceivers(sym, ids, binds, conflicts)
	if err != nil {
		return err
	}
	sites = append(sites, receivers...)
	if defaultExport {
		sites = onlyDeclaringFile(res, sites, f.file.Path, sym.Name)
	}
	if err := w.rewrite(res, sites, sym.Name, newName); err != nil {
		return err
	}
	res.Skipped = append(res.Skipped, skipped...)
	if err := w.homonyms(res, sameName, sym.Name, ids); err != nil {
		return err
	}
	if err := w.mentions(res, sym.Name); err != nil {
		return err
	}
	if err := w.unindexed(res, sym.Name, sameName); err != nil {
		return err
	}
	if base := strings.TrimSuffix(path.Base(f.file.Path), ".java"); f.file.Lang == lang.Java && base == sym.Name {
		res.Notes = append(res.Notes, fmt.Sprintf("java: the file must be renamed to %s.java too", newName))
	}
	return nil
}

// nameTaken acha uma declaração com o nome novo no mesmo escopo: mesmo
// container no mesmo arquivo ou, em Go e Java, no mesmo pacote (onde tipos,
// funções e métodos de arquivos diferentes dividem o escopo). Renomear para
// um nome ocupado quebraria o código mesmo com todas as refs certas.
func (w *workspace) nameTaken(f *openFile, sym store.Symbol, newName string) (store.Symbol, string, bool, error) {
	candidates, err := w.store.SymbolsByName(newName)
	if err != nil {
		return store.Symbol{}, "", false, err
	}
	for _, c := range candidates {
		if c.Container != sym.Container {
			continue
		}
		if c.FileID == sym.FileID {
			return c, f.file.Path, true, nil
		}
		if f.file.Lang != lang.Go && f.file.Lang != lang.Java {
			continue
		}
		other, ok, err := w.store.FileByID(c.FileID)
		if err != nil {
			return store.Symbol{}, "", false, err
		}
		if ok && other.Lang == f.file.Lang && other.Package == f.file.Package {
			return c, other.Path, true, nil
		}
	}
	return store.Symbol{}, "", false, nil
}

// lineText devolve a linha colapsada para o agente decidir sem reler o
// arquivo; "" se o arquivo não abre (a ocorrência segue listada).
func (w *workspace) lineText(file string, line int) string {
	f, err := w.load(file)
	if err != nil {
		return ""
	}
	return extract.Collapse(f.line(line))
}

// reexportSites acha `export { plus } from './money'`: re-exports não viram
// refs no índice, só linhas de import resolvidas para o símbolo. O nome
// importado muda; quando o re-export usa o mesmo nome, o nome público do
// módulo muda junto, e a nota avisa quem consome a biblioteca de fora.
func (w *workspace) reexportSites(res *Result, targets map[int64]bool, name string) ([]site, error) {
	var sites []site
	var public []string
	for id := range targets {
		imports, err := w.store.ImportsResolvedTo(id)
		if err != nil {
			return nil, err
		}
		for _, im := range imports {
			if !im.IsReexport || im.ImportedName != name {
				continue
			}
			file, ok, err := w.pathOf(im.FileID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			sites = append(sites, site{file: file, line: im.Line})
			if im.LocalName == name && !contains(public, file) {
				public = append(public, file)
			}
		}
	}
	if len(public) > 0 {
		sort.Strings(public)
		res.Notes = append(res.Notes, fmt.Sprintf("%s is re-exported under its own name by %s: that public name changes too", name, strings.Join(public, ", ")))
	}
	return sites, nil
}

func nameLine(s store.Symbol) int {
	if s.NameLine > 0 {
		return s.NameLine
	}
	return s.StartLine
}

// onlyDeclaringFile tira os usos de outros arquivos de um export default:
// lá o símbolo chega por um import com nome local próprio (`import x from`),
// que continua válido e não é o nome da declaração (o tsserver faz igual).
func onlyDeclaringFile(res *Result, sites []site, file, name string) []site {
	var kept []site
	others := map[string]bool{}
	for _, s := range sites {
		if s.file == file {
			kept = append(kept, s)
			continue
		}
		others[s.file] = true
	}
	if dropped := len(sites) - len(kept); dropped > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d use(s) in %d file(s) reach %s through a default import under their own local name and were left as they are",
			dropped, len(others), name))
	}
	return kept
}

// renameTargets é o símbolo mais, em Java, os construtores homônimos da
// classe, que precisam mudar junto.
func (w *workspace) renameTargets(sym store.Symbol) ([]store.Symbol, error) {
	targets := []store.Symbol{sym}
	symbols, err := w.store.SymbolsOfFile(sym.FileID)
	if err != nil {
		return nil, err
	}
	for _, s := range symbols {
		if s.Kind == extract.KindConstructor && s.Name == sym.Name && s.Container == sym.Name {
			targets = append(targets, s)
		}
	}
	return targets, nil
}

// declarationColumn reparseia o arquivo para achar o nome na declaração: o
// primeiro identificador com o nome, dentro do nó, na linha do nome. Sem
// isso, `def total(label="total")` teria duas ocorrências na linha.
func declarationColumn(f *openFile, sym store.Symbol, line int) int {
	if f.tree == nil {
		tree, err := parser.New().Parse(f.file.Lang, []byte(strings.Join(f.lines, "\n")))
		if err != nil {
			return 0
		}
		f.tree = tree
	}
	col := 0
	f.tree.Root().NamedDescendantForByteRange(sym.StartByte, sym.EndByte).Walk(func(n parser.Node) bool {
		if col != 0 {
			return false
		}
		if n.StartLine() == line && strings.HasSuffix(n.Type(), "identifier") && n.Text() == sym.Name {
			col = n.StartCol()
			return false
		}
		return true
	})
	return col
}

// rewrite agrupa as posições por linha, decide as colunas de cada uma com
// place e troca o nome da direita para a esquerda.
func (w *workspace) rewrite(res *Result, sites []site, oldName, newName string) error {
	type key struct {
		file string
		line int
	}
	byLine := map[key][]site{}
	checks := map[key][]string{}
	seen := map[site]bool{}
	var order []key
	for _, s := range sites {
		if seen[s] {
			continue
		}
		seen[s] = true
		if _, err := w.load(s.file); err != nil {
			return err // arquivo desatualizado: melhor recusar tudo que renomear metade
		}
		k := key{s.file, s.line}
		if byLine[k] == nil {
			order = append(order, k)
		}
		byLine[k] = append(byLine[k], s)
		if s.check != "" && !contains(checks[k], s.check) {
			checks[k] = append(checks[k], s.check)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].file != order[j].file {
			return order[i].file < order[j].file
		}
		return order[i].line < order[j].line
	})
	for _, k := range order {
		f := w.files[k.file]
		text := f.line(k.line)
		cols, reason := place(text, oldName, byLine[k])
		if reason != "" {
			res.Skipped = append(res.Skipped, Site{File: k.file, Line: k.line, Text: extract.Collapse(text), Reason: reason})
		}
		if len(cols) == 0 {
			continue
		}
		for i := len(cols) - 1; i >= 0; i-- {
			text = text[:cols[i]-1] + newName + text[cols[i]-1+len(oldName):]
		}
		f.change(k.line, k.line, []string{text})
		res.Updated = append(res.Updated, Site{File: k.file, Line: k.line, Name: newName, Text: extract.Collapse(text)})
		for _, name := range checks[k] {
			res.incoming = append(res.incoming, Site{File: k.file, Line: k.line, Name: name})
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// memberReceivers acha `Money.ZERO` e `Money.valueOf()`: acessos a membros
// pelo nome do tipo. A ref é do membro; o receptor, que é o nome a trocar,
// não tem ref própria. Troca quando a ref resolve para um membro do
// símbolo ou, sendo um tipo, quando o arquivo liga o nome simples a ele
// sem outro candidato (cobre membros herdados de fora do índice). O resto
// que pode ser ele volta listado.
func (w *workspace) memberReceivers(sym store.Symbol, targets map[int64]bool, binds, conflicts map[string]bool) ([]site, []Site, error) {
	symbols, err := w.store.SymbolsOfFile(sym.FileID)
	if err != nil {
		return nil, nil, err
	}
	inside := map[int64]bool{}
	for _, m := range symbols {
		if !targets[m.ID] && m.StartLine >= sym.StartLine && m.EndLine <= sym.EndLine {
			inside[m.ID] = true
		}
	}
	refs, err := w.store.RefsByReceiver(sym.Name)
	if err != nil {
		return nil, nil, err
	}
	var sites []site
	var skipped []Site
	for _, r := range refs {
		if len(r.ReceiverPath) > 0 {
			continue // `Money.ZERO.plus()`: o receptor é o mesmo do passo `ZERO`, que já tem ref
		}
		file, ok, err := w.pathOf(r.FileID)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		member := r.ResolvedSymbolID != nil && inside[*r.ResolvedSymbolID]
		bound := isTypeKind(sym.Kind) && binds[file] && !conflicts[file]
		if !member && !bound {
			if r.Resolution == store.Unresolved || r.Resolution == store.Ambiguous {
				skipped = append(skipped, Site{File: file, Line: r.Line, Name: sym.Name, Text: w.lineText(file, r.Line),
					Reason: fmt.Sprintf("%s: %s.%s may use this symbol, left untouched", r.Resolution, sym.Name, r.Name)})
			}
			continue
		}
		f, err := w.load(file)
		if err != nil {
			return nil, nil, err
		}
		col, found := receiverColumn(f.line(r.Line), r.Col, sym.Name)
		if !found {
			skipped = append(skipped, Site{File: file, Line: r.Line, Name: sym.Name, Text: w.lineText(file, r.Line), Reason: "receiver not found on the line"})
			continue
		}
		s := site{file: file, line: r.Line, col: col}
		if r.Resolution == store.Resolved {
			s.check = r.Name // a linha pode não ter ref com o nome novo: confere o membro
		}
		sites = append(sites, s)
	}
	return sites, skipped, nil
}

// receiverColumn acha `Nome.` na linha: na coluna da ref (o nó começa no
// receptor) ou logo antes do membro (o nó é só o nome do membro).
func receiverColumn(line string, col int, name string) (int, bool) {
	for _, c := range []int{col, col - len(name) - 1} {
		end := c - 1 + len(name)
		if c >= 1 && end < len(line) && line[end] == '.' && wordAt(line, c, name) {
			return c, true
		}
	}
	return 0, false
}

// isTypeKind: tipos, cujo nome simples raramente é sombreado por uma
// variável local e é receptor de acesso estático.
func isTypeKind(kind string) bool {
	switch kind {
	case extract.KindClass, extract.KindInterface, extract.KindType, "enum", "record":
		return true
	}
	return false
}

// place decide as colunas a renomear numa linha, em ordem crescente. Uma
// posição é certa quando a coluna da ref cai no nome ou quando o receptor
// vem logo antes dele (`pricing.Money`). As outras só levam ocorrências que
// sobram se cada ocorrência tem uma ref começando nela ou antes, casando em
// ordem. Ocorrência sem dono (o nome numa string, por exemplo) faz a linha
// voltar com o motivo, e só as posições certas mudam: nunca chuta.
func place(line, name string, sites []site) ([]int, string) {
	exact := map[int]bool{}
	var loose []int
	for _, s := range sites {
		afterReceiver := s.col + len(s.receiver) + 1
		switch {
		case s.col > 0 && wordAt(line, s.col, name):
			exact[s.col] = true
		case s.col > 0 && s.receiver != "" && s.col <= len(line) &&
			strings.HasPrefix(line[s.col-1:], s.receiver+".") && wordAt(line, afterReceiver, name):
			exact[afterReceiver] = true
		default:
			loose = append(loose, s.col)
		}
	}
	var cols, free []int
	for c := range exact {
		cols = append(cols, c)
	}
	quoted := quotedSpans(line)
	for _, c := range findWord(line, name) {
		if !exact[c] && !insideSpan(quoted, c) {
			free = append(free, c)
		}
	}
	reason := ""
	switch {
	case len(loose) == 0:
	case len(free) == 0 && len(exact) == 0:
		reason = "name not found on the line (index out of date?)"
	case len(free) == 0:
		// refs repetidas da mesma posição
	case len(loose) >= len(free) && startsBefore(loose, free):
		cols = append(cols, free...)
	default:
		reason = fmt.Sprintf("%d occurrences of %s for %d reference(s), position unknown", len(free), name, len(loose))
	}
	if len(cols) == 0 {
		return nil, reason
	}
	sort.Ints(cols)
	return cols, reason
}

// quotedSpans devolve os trechos entre aspas da linha (colunas 1-based, aspas
// incluídas): o nome dentro de `from '../logic/createFormControl'` nunca é a
// referência. Aspas sem fechamento vão até o fim da linha, o que só pode
// fazer uma posição incerta ser pulada, nunca trocada por engano.
func quotedSpans(line string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(line); i++ {
		q := line[i]
		if q != '"' && q != '\'' && q != '`' {
			continue
		}
		start := i
		for i++; i < len(line) && line[i] != q; i++ {
			if line[i] == '\\' {
				i++
			}
		}
		spans = append(spans, [2]int{start + 1, i + 1})
	}
	return spans
}

func insideSpan(spans [][2]int, col int) bool {
	for _, s := range spans {
		if col >= s[0] && col <= s[1] {
			return true
		}
	}
	return false
}

// startsBefore casa, em ordem, cada ocorrência com uma ref que começa nela
// ou antes.
func startsBefore(starts, occurrences []int) bool {
	sort.Ints(starts)
	for i, occ := range occurrences {
		if starts[i] > occ {
			return false
		}
	}
	return true
}

// homonyms lista as refs com o mesmo nome que talvez sejam o símbolo
// (unresolved, ambiguous) e conta as que com certeza não são.
func (w *workspace) homonyms(res *Result, refs []store.Ref, name string, targets map[int64]bool) error {
	var maybe []Site
	others := 0
	for _, r := range refs {
		if r.ResolvedSymbolID != nil && targets[*r.ResolvedSymbolID] {
			continue
		}
		if r.Resolution != store.Unresolved && r.Resolution != store.Ambiguous {
			others++
			continue
		}
		file, ok, err := w.pathOf(r.FileID)
		if err != nil {
			return err
		}
		if ok {
			maybe = append(maybe, Site{File: file, Line: r.Line, Name: name, Text: w.lineText(file, r.Line), Reason: r.Resolution + ": may be this symbol, left untouched"})
		}
	}
	sortSites(maybe)
	if len(maybe) > maxHomonyms {
		res.Notes = append(res.Notes, fmt.Sprintf("%d more %s uses that may be this symbol are not listed", len(maybe)-maxHomonyms, name))
		maybe = maybe[:maxHomonyms]
	}
	res.Skipped = append(res.Skipped, maybe...)
	sortSites(res.Skipped)
	if others > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d uses of %s resolve to other symbols or libraries and were left alone", others, name))
	}
	return nil
}

// mentions aponta o nome em comentários, strings e arquivos de texto. Não
// são referências e ficam como estão; o agente decide se atualiza a doc.
func (w *workspace) mentions(res *Result, name string) error {
	hits, err := w.store.WordHits(name, "", 500)
	if err != nil {
		return err
	}
	var sites []Site
	seen := map[string]bool{}
	for _, h := range hits {
		if h.Kind != scanner.KindComment && h.Kind != scanner.KindString && h.Kind != scanner.KindText {
			continue
		}
		key := fmt.Sprintf("%s:%d", h.Path, h.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		sites = append(sites, Site{File: h.Path, Line: h.Line})
	}
	if len(sites) == 0 {
		return nil
	}
	sortSites(sites)
	res.Notes = append(res.Notes, fmt.Sprintf("%d mention(s) of %s in comments, strings or text files were not changed: %s",
		len(sites), name, listSites(sites)))
	return nil
}

// unindexed aponta linhas que usam o nome como identificador sem nenhuma
// ref cobrindo: nem trocadas, nem listadas, nem de outro símbolo. São
// lacunas do extrator (chave de objeto, sintaxe que ele não lê) que um
// rename silencioso deixaria para o compilador achar.
func (w *workspace) unindexed(res *Result, name string, sameName []store.Ref) error {
	hits, err := w.store.WordHits(name, "", 5000)
	if err != nil {
		return err
	}
	covered := map[string]bool{}
	mark := func(file string, line int) { covered[fmt.Sprintf("%s:%d", file, line)] = true }
	for _, s := range res.Updated {
		mark(s.File, s.Line)
	}
	for _, s := range res.Skipped {
		mark(s.File, s.Line)
	}
	for _, r := range sameName {
		file, ok, err := w.pathOf(r.FileID)
		if err != nil {
			return err
		}
		if ok {
			mark(file, r.Line)
		}
	}
	var sites []Site
	for _, h := range hits {
		key := fmt.Sprintf("%s:%d", h.Path, h.Line)
		if h.Kind != scanner.KindIdent || h.Definition || covered[key] {
			continue
		}
		covered[key] = true
		sites = append(sites, Site{File: h.Path, Line: h.Line})
	}
	if len(sites) == 0 {
		return nil
	}
	sortSites(sites)
	res.Notes = append(res.Notes, fmt.Sprintf("%d line(s) use %s as an identifier the index has no reference for, left untouched: %s",
		len(sites), name, listSites(sites)))
	return nil
}

func sortSites(sites []Site) {
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})
}

// wordAt diz se o nome começa na coluna (1-based, em bytes) como palavra
// inteira.
func wordAt(line string, col int, word string) bool {
	i := col - 1
	if i < 0 || i+len(word) > len(line) || line[i:i+len(word)] != word {
		return false
	}
	return (i == 0 || !isIdentByte(line[i-1])) && (i+len(word) == len(line) || !isIdentByte(line[i+len(word)]))
}

// findWord devolve as colunas (1-based) onde o nome aparece como palavra.
func findWord(line, word string) []int {
	var cols []int
	for i := 0; i+len(word) <= len(line); i++ {
		if wordAt(line, i+1, word) {
			cols = append(cols, i+1)
		}
	}
	return cols
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) || i == 0 && s[i] >= '0' && s[i] <= '9' {
			return false
		}
	}
	return true
}

// isIdentByte aceita bytes >= 0x80 para não cortar identificadores UTF-8
// (`café` não contém a palavra `caf`).
func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}
