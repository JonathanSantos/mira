package graph

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/store"
)

// SearchOptions filtra e limita uma busca.
type SearchOptions struct {
	Path         string // prefixo de caminho ("src/logic") ou glob ("src/**/*.ts")
	ExcludeTests bool   // pula arquivos e diretórios de teste
	Limit        int    // máximo de hits no total (default 100)
	PerFile      int    // máximo de hits por arquivo (default 20)
	Regex        bool   // trata a consulta como regexp sobre o texto das linhas
	Context      int    // linhas antes e depois de cada hit (0 = só a linha)
}

// Hit é uma ocorrência. Kind diz de onde veio a palavra: "" (identificador
// no código), string, comment, text (arquivo sem código) ou part (sub-token
// de um identificador composto). Container é o símbolo que envolve a linha.
type Hit struct {
	Line           int    `json:"line"`
	Text           string `json:"text"`
	Definition     bool   `json:"definition,omitempty"` // linha onde um símbolo com esse nome é declarado
	Kind           string `json:"kind,omitempty"`
	Container      string `json:"container,omitempty"`
	ContainerStart int    `json:"container_start,omitempty"`
	ContainerEnd   int    `json:"container_end,omitempty"`
	Context        string `json:"context,omitempty"`       // linhas ao redor, quando pedido
	ContextStart   int    `json:"context_start,omitempty"` // número da primeira linha de Context
}

// FileHits agrupa os hits de um arquivo.
type FileHits struct {
	File      string `json:"file"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated,omitempty"`
	Hits      []Hit  `json:"hits"`
}

// SearchResult é a saída de search: arquivos com definição vêm primeiro.
type SearchResult struct {
	Query     string     `json:"query"`
	Regex     bool       `json:"regex,omitempty"`
	Files     []FileHits `json:"files"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated,omitempty"`
	Hint      string     `json:"hint,omitempty"`
}

const (
	defaultLimit   = 100
	defaultPerFile = 20
	// fetchCap limita o que o índice devolve antes do filtro em Go.
	fetchCap = 5000
)

// Search consulta o índice lexical (ou, com Regex, o texto dos arquivos
// indexados) e agrupa por arquivo.
func (g *Graph) Search(query string, opts SearchOptions) (SearchResult, error) {
	opts = withDefaults(opts)
	if opts.Regex {
		return g.searchRegex(query, opts)
	}
	prefix, glob := splitPath(opts.Path)
	raw, err := g.store.WordHits(query, prefix, fetchCap)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Query: query, Files: []FileHits{}}
	groups := map[string]*FileHits{}
	var order []string
	for _, h := range raw {
		if !pathAllowed(h.Path, glob, opts.ExcludeTests) {
			continue
		}
		result.Total++
		fh, ok := groups[h.Path]
		if !ok {
			fh = &FileHits{File: h.Path}
			groups[h.Path] = fh
			order = append(order, h.Path)
		}
		fh.Count++
		if len(fh.Hits) >= opts.PerFile {
			fh.Truncated = true
			continue
		}
		hit, err := g.hit(h.FileID, h.Path, h.Line, opts.Context)
		if err != nil {
			return SearchResult{}, err
		}
		hit.Definition = h.Definition
		if h.Kind != store.WordIdent {
			hit.Kind = h.Kind
		}
		fh.Hits = append(fh.Hits, hit)
	}
	result.Files = orderFiles(groups, order)
	result.Files, result.Truncated = capHits(result.Files, opts.Limit)
	if result.Total == 0 {
		result.Hint = "no identifier matches; try --regex for free text, or resolve for a symbol name"
	}
	return result, nil
}

// searchRegex faz o papel do grep: percorre o texto dos arquivos indexados.
func (g *Graph) searchRegex(pattern string, opts SearchOptions) (SearchResult, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return SearchResult{}, fmt.Errorf("invalid regex: %w", err)
	}
	prefix, glob := splitPath(opts.Path)
	files, err := g.store.FileSummaries(prefix)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Query: pattern, Regex: true, Files: []FileHits{}}
	for _, f := range files {
		if !pathAllowed(f.Path, glob, opts.ExcludeTests) {
			continue
		}
		fh, err := g.grepFile(f, re, opts)
		if err != nil {
			return SearchResult{}, err
		}
		if fh.Count == 0 {
			continue
		}
		result.Total += fh.Count
		result.Files = append(result.Files, fh)
	}
	result.Files, result.Truncated = capHits(result.Files, opts.Limit)
	return result, nil
}

func (g *Graph) grepFile(file store.FileSummary, re *regexp.Regexp, opts SearchOptions) (FileHits, error) {
	f, err := os.Open(filepath.Join(g.root, filepath.FromSlash(file.Path)))
	if err != nil {
		return FileHits{}, fmt.Errorf("reading %s: %w", file.Path, err)
	}
	defer func() { _ = f.Close() }()
	fh := FileHits{File: file.Path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	line := 0
	for scanner.Scan() {
		line++
		if !re.MatchString(scanner.Text()) {
			continue
		}
		fh.Count++
		if len(fh.Hits) >= opts.PerFile {
			fh.Truncated = true
			continue
		}
		hit, err := g.hit(file.ID, file.Path, line, opts.Context)
		if err != nil {
			return FileHits{}, err
		}
		if file.Lang == lang.Text {
			hit.Kind = store.WordText
		}
		fh.Hits = append(fh.Hits, hit)
	}
	return fh, scanner.Err()
}

// hit monta a ocorrência: a linha, o símbolo que a envolve e, quando
// pedido, o contexto numerado ao redor.
func (g *Graph) hit(fileID int64, rel string, line, context int) (Hit, error) {
	lines, err := g.lines(rel)
	if err != nil {
		return Hit{}, err
	}
	hit := Hit{Line: line, Text: strings.TrimSpace(slice(lines, line, line))}
	if sym, ok, err := g.store.SymbolAt(fileID, line); err == nil && ok {
		hit.Container, hit.ContainerStart, hit.ContainerEnd = sym.QualifiedName, sym.StartLine, sym.EndLine
	}
	if context > 0 {
		start := max(1, line-context)
		hit.Context = slice(lines, start, line+context)
		hit.ContextStart = start
	}
	return hit, nil
}

func withDefaults(opts SearchOptions) SearchOptions {
	if opts.Limit <= 0 {
		opts.Limit = defaultLimit
	}
	if opts.PerFile <= 0 {
		opts.PerFile = defaultPerFile
	}
	return opts
}

// splitPath separa um filtro de caminho em prefixo (para o SQL) e glob
// (para o filtro em Go). "src/logic" é prefixo; "src/**/*.ts" é glob com o
// prefixo literal "src/".
func splitPath(p string) (prefix, glob string) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if p == "" {
		return "", ""
	}
	if !strings.ContainsAny(p, "*?[") {
		return p, ""
	}
	i := strings.IndexAny(p, "*?[")
	return p[:strings.LastIndex(p[:i]+"/", "/")+1], p
}

// pathAllowed aplica o glob (com ** para qualquer profundidade) e o filtro
// de testes.
func pathAllowed(rel, glob string, excludeTests bool) bool {
	if excludeTests && IsTestPath(rel) {
		return false
	}
	if glob == "" {
		return true
	}
	return matchGlob(glob, rel)
}

func matchGlob(glob, rel string) bool {
	if !strings.Contains(glob, "**") {
		ok, err := path.Match(glob, rel)
		return err == nil && ok
	}
	// "**" casa qualquer sequência de diretórios: testa cada expansão.
	parts := strings.SplitN(glob, "**", 2)
	head, tail := parts[0], strings.TrimPrefix(parts[1], "/")
	if !strings.HasPrefix(rel, head) {
		return false
	}
	rest := strings.TrimPrefix(rel, head)
	segs := strings.Split(rest, "/")
	for i := 0; i <= len(segs); i++ {
		if matchGlob(tail, strings.Join(segs[i:], "/")) {
			return true
		}
	}
	return false
}

// IsTestPath reconhece arquivos e diretórios de teste pelas convenções
// mais comuns de TS/JS e Java.
func IsTestPath(rel string) bool {
	base := path.Base(rel)
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		switch seg {
		case "test", "tests", "__tests__", "__mocks__", "e2e", "spec", "specs", "testdata", "__typetest__":
			return true
		}
	}
	lower := strings.ToLower(base)
	return strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.") ||
		strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Tests.java") ||
		strings.HasSuffix(base, "IT.java") || strings.Contains(lower, ".test-d.") ||
		strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
		strings.HasSuffix(base, "_test.py") || base == "conftest.py"
}

// orderFiles põe primeiro os arquivos com definição, depois mantém a
// ordem de caminho.
func orderFiles(groups map[string]*FileHits, order []string) []FileHits {
	out := make([]FileHits, 0, len(order))
	for _, p := range order {
		out = append(out, *groups[p])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return hasDefinition(out[i]) && !hasDefinition(out[j])
	})
	return out
}

func hasDefinition(fh FileHits) bool {
	for _, h := range fh.Hits {
		if h.Definition {
			return true
		}
	}
	return false
}

// capHits corta a lista para o limite total de hits, marcando truncamento.
func capHits(files []FileHits, limit int) ([]FileHits, bool) {
	remaining := limit
	for i := range files {
		if remaining <= 0 {
			return files[:i], true
		}
		if len(files[i].Hits) > remaining {
			files[i].Hits = files[i].Hits[:remaining]
			files[i].Truncated = true
			return files[:i+1], true
		}
		remaining -= len(files[i].Hits)
	}
	return files, false
}
