// Package editor faz edições por símbolo: troca, insere, apaga ou renomeia
// uma definição usando o range e as referências que o índice conhece. É o
// lado de escrita que fecha o ciclo "localizar, editar, verificar" sem que
// o agente conte linhas nem confie em busca textual.
package editor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/JonathanSantos/mira/internal/extract"
	"github.com/JonathanSantos/mira/internal/hasher"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
	"github.com/JonathanSantos/mira/internal/store"
)

// Ações aceitas por Apply.
const (
	Replace      = "replace"
	ReplaceIn    = "replace-in"
	InsertBefore = "insert-before"
	InsertAfter  = "insert-after"
	Delete       = "delete"
	Rename       = "rename"
)

// Actions são as ações válidas, na ordem em que aparecem na ajuda.
var Actions = []string{Replace, ReplaceIn, InsertBefore, InsertAfter, Delete, Rename}

// Request é uma edição pedida.
type Request struct {
	Action  string
	File    string
	Symbol  string
	Text    string // texto novo; no rename, o nome novo; no replace-in, o que entra no lugar de Old
	Old     string // replace-in: trecho exato a trocar dentro do símbolo
	All     bool   // replace-in: troca todas as ocorrências em vez de exigir uma só
	Preview bool   // não escreve nada: devolve o diff
	Force   bool   // apaga mesmo com referências apontando para o símbolo
}

// Site é uma ocorrência que a edição tocou, ou que deveria tocar e não
// tocou (Reason diz por quê).
type Site struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Name   string `json:"name,omitempty"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// FileChange resume o que mudou num arquivo.
type FileChange struct {
	File    string `json:"file"`
	Changes int    `json:"changes"`
	Lines   int    `json:"lines"` // saldo de linhas: positivo cresceu, negativo encolheu
}

// Result descreve a edição feita (ou, com Preview, a que seria feita).
type Result struct {
	Action    string       `json:"action"`
	File      string       `json:"file"`
	Symbol    string       `json:"symbol"`
	Kind      string       `json:"kind,omitempty"`
	NewName   string       `json:"new_name,omitempty"`
	StartLine int          `json:"start_line,omitempty"`
	EndLine   int          `json:"end_line,omitempty"`
	Lines     int          `json:"lines"`
	Files     []FileChange `json:"files,omitempty"`
	Updated   []Site       `json:"updated,omitempty"`
	Skipped   []Site       `json:"skipped,omitempty"`
	Dangling  []Site       `json:"dangling,omitempty"`
	Notes     []string     `json:"notes,omitempty"`
	Checked   int          `json:"checked,omitempty"` // referências conferidas por Verify
	Diff      string       `json:"diff,omitempty"`
	Preview   bool         `json:"preview,omitempty"`

	// incoming são as referências que apontavam para o símbolo antes da
	// edição; Verify confere depois se continuam resolvendo.
	incoming []Site
}

// Apply executa a edição. Nada é escrito quando Preview está ligado: o
// resultado traz o diff do que aconteceria.
func Apply(root string, st *store.Store, req Request) (Result, error) {
	w := &workspace{root: root, store: st, files: map[string]*openFile{}, paths: map[int64]string{}}
	defer w.release()
	rel := relative(root, req.File)
	f, err := w.load(rel)
	if err != nil {
		return Result{}, err
	}
	sym, err := locate(st, f.file.ID, req.Symbol)
	if err != nil {
		return Result{}, err
	}
	res := Result{Action: req.Action, File: rel, Symbol: sym.QualifiedName, Kind: sym.Kind, Preview: req.Preview}
	switch req.Action {
	case Replace, InsertBefore, InsertAfter:
		err = w.splice(&res, f, sym, req)
	case ReplaceIn:
		err = w.replaceIn(&res, f, sym, req)
	case Delete:
		err = w.remove(&res, f, sym, req)
	case Rename:
		err = w.rename(&res, f, sym, req)
	default:
		err = fmt.Errorf("unknown action %q (%s)", req.Action, strings.Join(Actions, ", "))
	}
	if err != nil {
		return Result{}, err
	}
	res.Files = w.summary()
	if req.Preview {
		res.Diff = w.diff()
		return res, nil
	}
	if err := w.write(); err != nil {
		return Result{}, err
	}
	return res, nil
}

// splice troca ou insere o texto no range do símbolo.
func (w *workspace) splice(res *Result, f *openFile, sym store.Symbol, req Request) error {
	text := strings.TrimRight(req.Text, "\n")
	if text == "" {
		return fmt.Errorf("no text for %s: pass the new lines", req.Action)
	}
	repl := strings.Split(text, "\n")
	res.Lines = len(repl)
	switch req.Action {
	case Replace:
		f.change(sym.StartLine, sym.EndLine, repl)
		res.StartLine, res.EndLine = sym.StartLine, sym.StartLine+len(repl)-1
		incoming, err := w.refsTo(sym, false)
		if err != nil {
			return err
		}
		res.incoming = shifted(incoming, f.file.Path, sym.StartLine, sym.EndLine, len(repl))
	case InsertBefore:
		f.change(sym.StartLine, sym.StartLine-1, repl)
		res.StartLine, res.EndLine = sym.StartLine, sym.StartLine+len(repl)-1
	case InsertAfter:
		f.change(sym.EndLine+1, sym.EndLine, repl)
		res.StartLine, res.EndLine = sym.EndLine+1, sym.EndLine+len(repl)
	}
	return nil
}

// shifted ajusta as linhas das refs do mesmo arquivo depois de trocar
// [start, end] por n linhas: as de baixo escorregam, as de dentro somem
// (fazem parte do texto novo e não há como saber onde caem).
func shifted(sites []Site, file string, start, end, n int) []Site {
	var out []Site
	for _, s := range sites {
		if s.File == file && s.Line >= start && s.Line <= end {
			continue
		}
		if s.File == file && s.Line > end {
			s.Line += n - (end - start + 1)
		}
		out = append(out, s)
	}
	return out
}

// workspace guarda os arquivos abertos e as mudanças pendentes: tudo é
// calculado em memória e só vai para o disco no fim, para um rename de
// vários arquivos não deixar metade aplicada por um erro no meio.
type workspace struct {
	root  string
	store *store.Store
	files map[string]*openFile
	order []string
	paths map[int64]string // id do arquivo -> caminho, para não reler a cada ref
}

func (w *workspace) pathOf(id int64) (string, bool, error) {
	if p, ok := w.paths[id]; ok {
		return p, true, nil
	}
	f, ok, err := w.store.FileByID(id)
	if err != nil || !ok {
		return "", ok, err
	}
	w.paths[id] = f.Path
	return f.Path, true, nil
}

type openFile struct {
	file    store.File
	abs     string
	mode    os.FileMode
	lines   []string
	changes []change
	tree    *parser.Tree // só quando o rename precisa achar o nome na declaração
}

// release devolve as árvores reparseadas (memória do lado C).
func (w *workspace) release() {
	for _, f := range w.files {
		if f.tree != nil {
			f.tree.Release()
		}
	}
}

// change troca as linhas [start, end] (1-based, inclusivas) por repl;
// end < start insere antes de start, repl vazio apaga.
type change struct {
	start, end int
	repl       []string
}

func (f *openFile) change(start, end int, repl []string) {
	f.changes = append(f.changes, change{start: start, end: end, repl: repl})
}

// line devolve a linha 1-based, ou "" fora do arquivo.
func (f *openFile) line(n int) string {
	if n < 1 || n > len(f.lines) {
		return ""
	}
	return f.lines[n-1]
}

func (w *workspace) load(rel string) (*openFile, error) {
	if f, ok := w.files[rel]; ok {
		return f, nil
	}
	f, ok, err := w.store.FileByPath(rel)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("file %s is not indexed", rel)
	}
	abs := filepath.Join(w.root, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	if fp := hasher.FingerprintOf(info); fp.Size != f.Size || fp.ModTime != f.ModTime {
		return nil, fmt.Errorf("%s changed since it was indexed; run index (or any query, which refreshes) and retry", rel)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	open := &openFile{file: f, abs: abs, mode: info.Mode(), lines: strings.Split(string(data), "\n")}
	w.files[rel] = open
	w.order = append(w.order, rel)
	return open, nil
}

// write calcula todos os arquivos antes de gravar o primeiro: um conflito
// em qualquer um cancela a edição inteira.
func (w *workspace) write() error {
	contents := map[string][]string{}
	for _, rel := range w.order {
		f := w.files[rel]
		if len(f.changes) == 0 {
			continue
		}
		out, err := f.result()
		if err != nil {
			return err
		}
		contents[rel] = out
	}
	for _, rel := range w.order {
		out, ok := contents[rel]
		if !ok {
			continue
		}
		f := w.files[rel]
		if err := os.WriteFile(f.abs, []byte(strings.Join(out, "\n")), f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}
	return nil
}

// result aplica as mudanças de trás para frente, para as linhas de cima
// não escorregarem.
func (f *openFile) result() ([]string, error) {
	sorted := append([]change(nil), f.changes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start > sorted[j].start })
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if cur.end >= cur.start && cur.end >= prev.start {
			return nil, fmt.Errorf("%s: overlapping edits at lines %d-%d and %d", f.file.Path, cur.start, cur.end, prev.start)
		}
	}
	out := f.lines
	for _, c := range sorted {
		out = splice(out, c.start, c.end, c.repl)
	}
	return out, nil
}

func (w *workspace) summary() []FileChange {
	var out []FileChange
	for _, rel := range w.order {
		f := w.files[rel]
		if len(f.changes) == 0 {
			continue
		}
		delta := 0
		for _, c := range f.changes {
			delta += len(c.repl)
			if c.end >= c.start {
				delta -= c.end - c.start + 1
			}
		}
		out = append(out, FileChange{File: rel, Changes: len(f.changes), Lines: delta})
	}
	return out
}

// splice troca as linhas [start, end] (1-based, inclusivas; end < start =
// inserção em start) por repl.
func splice(lines []string, start, end int, repl []string) []string {
	out := make([]string, 0, len(lines)+len(repl))
	out = append(out, lines[:start-1]...)
	out = append(out, repl...)
	if end >= start {
		out = append(out, lines[end:]...)
	} else {
		out = append(out, lines[start-1:]...)
	}
	return out
}

// locate acha o símbolo pelo nome simples, `Container.member` ou nome
// qualificado. Sobrecargas dividem o nome: `Owner.getPet:126` escolhe a
// que contém a linha. Mais de um candidato nunca vira o primeiro da lista.
func locate(st *store.Store, fileID int64, symbol string) (store.Symbol, error) {
	name, line := splitLine(symbol)
	symbols, err := st.SymbolsOfFile(fileID)
	if err != nil {
		return store.Symbol{}, err
	}
	var exact, matches []store.Symbol
	for _, s := range symbols {
		if line > 0 && (line < s.StartLine || line > s.EndLine) {
			continue
		}
		switch {
		case s.QualifiedName == name:
			exact = append(exact, s)
		case s.Name == name || strings.HasSuffix(s.QualifiedName, "."+name): // `Money.plus` casa com `com.acme.pricing.Money.plus`
			matches = append(matches, s)
		}
	}
	if len(exact) > 0 {
		matches = exact
	}
	matches = withoutConstructors(matches)
	switch len(matches) {
	case 0:
		if line > 0 {
			return store.Symbol{}, fmt.Errorf("symbol %s not found at line %d in this file (use symbols to list them)", name, line)
		}
		return store.Symbol{}, fmt.Errorf("symbol %s not found in this file (use symbols to list them)", name)
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, m := range matches {
		names = append(names, fmt.Sprintf("%s:%d (%d-%d)", name, m.StartLine, m.StartLine, m.EndLine))
	}
	return store.Symbol{}, fmt.Errorf("symbol %s is ambiguous in this file: %s; pass one of these", name, strings.Join(names, ", "))
}

// splitLine separa `Owner.getPet:126` em nome e linha; sem sufixo numérico
// a linha é 0.
func splitLine(symbol string) (string, int) {
	i := strings.LastIndex(symbol, ":")
	if i < 0 {
		return symbol, 0
	}
	n, err := strconv.Atoi(symbol[i+1:])
	if err != nil || n <= 0 {
		return symbol, 0
	}
	return symbol[:i], n
}

// withoutConstructors tira os construtores Java que dividem o nome com a
// própria classe entre os candidatos: pelo nome simples, `Money` é a
// classe; o construtor continua acessível como `Money.Money`.
func withoutConstructors(matches []store.Symbol) []store.Symbol {
	if len(matches) < 2 {
		return matches
	}
	types := map[string]bool{}
	for _, m := range matches {
		if m.Kind != extract.KindConstructor {
			types[m.Name] = true
		}
	}
	var out []store.Symbol
	for _, m := range matches {
		if m.Kind == extract.KindConstructor && types[m.Container] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// refsTo lista as referências resolvidas para o símbolo; withMembers
// inclui as dos membros declarados dentro dele (apagar uma classe quebra
// quem chama os métodos dela).
func (w *workspace) refsTo(sym store.Symbol, withMembers bool) ([]Site, error) {
	targets := []store.Symbol{sym}
	if withMembers {
		members, err := w.store.SymbolsOfFile(sym.FileID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m.ID != sym.ID && m.StartLine >= sym.StartLine && m.EndLine <= sym.EndLine {
				targets = append(targets, m)
			}
		}
	}
	var out []Site
	seen := map[string]bool{}
	for _, t := range targets {
		refs, err := w.store.RefsTo(t.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			file, ok, err := w.pathOf(r.FileID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d", file, r.Line, r.Col)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Site{File: file, Line: r.Line, Name: r.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func relative(root, file string) string {
	if filepath.IsAbs(file) {
		if rel, err := filepath.Rel(root, file); err == nil {
			file = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(file))
}

// isComment diz se a linha é um comentário solto (para levar junto a
// documentação de um símbolo apagado).
func isComment(line string, language lang.Lang) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if language == lang.Python {
		return strings.HasPrefix(t, "#")
	}
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}
