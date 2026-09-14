package graph

import (
	"sort"
	"strings"

	"github.com/JonathanSantos/mira/internal/store"
)

// FileEntry é um arquivo na árvore.
type FileEntry struct {
	Path       string `json:"path"`
	Lang       string `json:"lang"`
	Symbols    int    `json:"symbols"`
	ParseError bool   `json:"parse_error,omitempty"`
}

// DirEntry é um diretório na árvore, com totais recursivos. Label é o nome
// a mostrar sob o pai: para uma cadeia colapsada (src/main/java/com/acme)
// é a cadeia inteira, para que main e test não fiquem iguais.
type DirEntry struct {
	Path    string      `json:"path"`
	Label   string      `json:"label"`
	Files   int         `json:"files"`
	Symbols int         `json:"symbols"`
	Dirs    []DirEntry  `json:"dirs,omitempty"`
	Entries []FileEntry `json:"entries,omitempty"`
}

// FilesResult é a saída de files: a árvore dos arquivos indexados.
type FilesResult struct {
	Root    string   `json:"root"`
	Depth   int      `json:"depth"`
	Files   int      `json:"files"`
	Symbols int      `json:"symbols"`
	Tree    DirEntry `json:"tree"`
}

// Files devolve a árvore de arquivos indexados sob um prefixo, até a
// profundidade dada (0 = só o nível pedido; -1 = tudo). Diretórios abaixo
// da profundidade aparecem só com totais.
func (g *Graph) Files(prefix string, depth int, excludeTests, dirsOnly bool) (FilesResult, error) {
	prefix = strings.Trim(strings.TrimPrefix(prefix, "./"), "/")
	summaries, err := g.store.FileSummaries(prefix)
	if err != nil {
		return FilesResult{}, err
	}
	root := &node{path: prefix}
	for _, f := range summaries {
		if prefix != "" && f.Path != prefix && !strings.HasPrefix(f.Path, prefix+"/") {
			continue
		}
		if excludeTests && IsTestPath(f.Path) {
			continue
		}
		root.add(f, prefix)
	}
	result := FilesResult{Root: prefix, Depth: depth, Files: root.files, Symbols: root.symbols}
	if result.Root == "" {
		result.Root = "."
	}
	result.Tree = root.entry(depth)
	if dirsOnly {
		dropEntries(&result.Tree)
	}
	return result, nil
}

// dropEntries deixa só diretórios com totais: a primeira olhada num repo
// desconhecido não precisa de cada arquivo.
func dropEntries(d *DirEntry) {
	d.Entries = nil
	for i := range d.Dirs {
		dropEntries(&d.Dirs[i])
	}
}

// node é a árvore mutável usada para agregar antes de virar DirEntry.
type node struct {
	path    string
	files   int
	symbols int
	dirs    map[string]*node
	entries []FileEntry
}

func (n *node) add(f store.FileSummary, prefix string) {
	rel := strings.TrimPrefix(strings.TrimPrefix(f.Path, prefix), "/")
	n.insert(strings.Split(rel, "/"), f)
}

func (n *node) insert(segs []string, f store.FileSummary) {
	n.files++
	n.symbols += f.Symbols
	if len(segs) == 1 {
		n.entries = append(n.entries, FileEntry{Path: f.Path, Lang: string(f.Lang), Symbols: f.Symbols, ParseError: f.ParseError})
		return
	}
	if n.dirs == nil {
		n.dirs = map[string]*node{}
	}
	child, ok := n.dirs[segs[0]]
	if !ok {
		child = &node{path: join(n.path, segs[0])}
		n.dirs[segs[0]] = child
	}
	child.insert(segs[1:], f)
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// entry materializa a árvore até a profundidade: abaixo dela só os totais
// dos diretórios ficam, sem entradas. Cadeias de diretório único
// (src/main/java/org/acme) são colapsadas num nível só, sem gastar
// profundidade, como o GitHub faz.
func (n *node) entry(depth int) DirEntry {
	e := n.collapsed().materialize(depth)
	e.Label = labelUnder(n.parentPath(), e.Path)
	return e
}

// collapsed desce cadeias de diretório único.
func (n *node) collapsed() *node {
	for len(n.dirs) == 1 && len(n.entries) == 0 {
		for _, only := range n.dirs {
			n = only
		}
	}
	return n
}

func (n *node) parentPath() string {
	if i := strings.LastIndex(n.path, "/"); i >= 0 {
		return n.path[:i]
	}
	return ""
}

func (n *node) materialize(depth int) DirEntry {
	e := DirEntry{Path: n.path, Files: n.files, Symbols: n.symbols}
	if e.Path == "" {
		e.Path = "."
	}
	names := make([]string, 0, len(n.dirs))
	for name := range n.dirs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := n.dirs[name].collapsed()
		var d DirEntry
		if depth == 0 {
			d = DirEntry{Path: child.path, Files: child.files, Symbols: child.symbols}
		} else {
			d = child.materialize(depth - 1)
		}
		d.Label = labelUnder(n.path, child.path)
		e.Dirs = append(e.Dirs, d)
	}
	e.Entries = n.entries
	return e
}

// labelUnder devolve o caminho de child relativo a parent.
func labelUnder(parent, child string) string {
	if parent == "" {
		return child
	}
	return strings.TrimPrefix(child, parent+"/")
}
