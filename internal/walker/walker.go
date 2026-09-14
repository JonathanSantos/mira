// Package walker lista os arquivos candidatos à indexação, respeitando
// .gitignore, os ignores fixos e os ignores extras da config.
package walker

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File é um arquivo candidato. Path é relativo à raiz, sempre com "/".
type File struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Options controla o filtro da caminhada.
type Options struct {
	// Extensions é o conjunto de extensões aceitas (com ponto, ex: ".ts").
	Extensions map[string]bool
	// Ignore são padrões extras no estilo .gitignore, relativos à raiz.
	Ignore []string
}

// fixedIgnores são diretórios que nunca fazem sentido indexar, em qualquer
// profundidade.
var fixedIgnores = []string{
	"node_modules", "dist", "build", "out", "target",
	".gradle", "coverage", ".git", ".mira",
}

// Ignored diz se um nome de diretório está na lista fixa de ignorados.
func Ignored(name string) bool {
	for _, d := range fixedIgnores {
		if d == name {
			return true
		}
	}
	return false
}

// Walk devolve os arquivos candidatos ordenados por caminho. Dentro de um
// repositório git usa `git ls-files` (que já aplica o .gitignore); fora dele
// caminha o disco aplicando o subconjunto próprio de .gitignore.
func Walk(root string, opts Options) ([]File, error) {
	var files []File
	var err error
	if isGitRepo(root) {
		files, err = walkGit(root, opts)
		if err == nil {
			return files, nil
		}
		// git indisponível ou falhou: cai para a caminhada manual.
	}
	files, err = walkFS(root, opts)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func isGitRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// walkGit lista arquivos rastreados e não rastreados que o git não ignora
// (--others --exclude-standard), o que cobre "tracked + status" numa chamada.
func walkGit(root string, opts Options) ([]File, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	base := baseIgnores(opts)
	var files []File
	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		rel := filepath.ToSlash(string(raw))
		if !opts.Extensions[filepath.Ext(rel)] || base.ignored(rel, false) {
			continue
		}
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil || info.IsDir() {
			// Arquivo rastreado mas apagado do disco: some do índice na fase
			// de remoção, não aqui.
			continue
		}
		files = append(files, File{Path: rel, Size: info.Size(), ModTime: info.ModTime()})
	}
	sortFiles(files)
	return files, nil
}

func walkFS(root string, opts Options) ([]File, error) {
	var files []File
	// stack guarda os matchers ativos por diretório; a precedência é fixos <
	// .gitignore da raiz < ignores da config < .gitignore aninhados.
	stack := ignoreSet{fixedMatcher()}
	if m, ok := loadGitignore(root, "."); ok {
		stack = append(stack, m)
	}
	if m, ok := configMatcher(opts); ok {
		stack = append(stack, m)
	}
	depthOf := map[string]int{".": len(stack)}

	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking %s: %w", abs, walkErr)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return fmt.Errorf("relativizing %s: %w", abs, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		parent := filepath.ToSlash(filepath.Dir(rel))
		stack = stack[:depthOf[parent]]

		if d.IsDir() {
			if stack.ignored(rel, true) {
				return filepath.SkipDir
			}
			if m, ok := loadGitignore(root, rel); ok {
				stack = append(stack, m)
			}
			depthOf[rel] = len(stack)
			return nil
		}
		if !opts.Extensions[filepath.Ext(rel)] || stack.ignored(rel, false) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", abs, err)
		}
		files = append(files, File{Path: rel, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortFiles(files)
	return files, nil
}

// baseIgnores monta os matchers usados no modo git, em que o .gitignore já
// foi aplicado pelo próprio git: só os ignores fixos e os extras da config.
func baseIgnores(opts Options) ignoreSet {
	set := ignoreSet{fixedMatcher()}
	if m, ok := configMatcher(opts); ok {
		set = append(set, m)
	}
	return set
}

func fixedMatcher() matcher {
	m := matcher{}
	for _, name := range fixedIgnores {
		m.patterns = append(m.patterns, pattern{dirOnly: true, segs: []string{name}})
	}
	return m
}

func configMatcher(opts Options) (matcher, bool) {
	if len(opts.Ignore) == 0 {
		return matcher{}, false
	}
	return parseGitignore("", []byte(strings.Join(opts.Ignore, "\n"))), true
}

func loadGitignore(root, rel string) (matcher, bool) {
	base := rel
	if rel == "." {
		base = ""
	}
	data, err := os.ReadFile(filepath.Join(root, rel, ".gitignore"))
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return matcher{}, false
	}
	return parseGitignore(base, data), true
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}
