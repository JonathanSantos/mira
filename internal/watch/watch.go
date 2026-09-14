// Package watch observa o repositório com fsnotify e responde uma pergunta
// só: "algo mudou desde a última vez que perguntei?". É o que permite ao
// servidor MCP pular a caminhada do repositório antes de cada consulta.
package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// maxDirs limita os diretórios observados: no macOS cada um é um descritor
// de arquivo, e o limite padrão do processo é baixo. Acima disso o chamador
// volta a caminhar o repositório a cada consulta.
const maxDirs = 1500

// Watcher marca dirty a cada evento em qualquer diretório observado.
type Watcher struct {
	w     *fsnotify.Watcher
	skip  func(name string) bool
	mu    sync.Mutex
	dirty bool
	dirs  int
}

// New observa root e todos os subdiretórios que skip não rejeita. Começa
// dirty, para a primeira consulta atualizar o índice.
func New(root string, skip func(name string) bool) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("starting watcher: %w", err)
	}
	w := &Watcher{w: fw, skip: skip, dirty: true}
	if err := w.addTree(root); err != nil {
		_ = fw.Close()
		return nil, err
	}
	go w.loop()
	return w, nil
}

func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // diretório que sumiu ou sem permissão: segue
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && w.skip(d.Name()) {
			return filepath.SkipDir
		}
		if w.dirs >= maxDirs {
			return fmt.Errorf("more than %d directories: watching is off", maxDirs)
		}
		if err := w.w.Add(path); err != nil {
			return fmt.Errorf("watching %s: %w", path, err)
		}
		w.dirs++
		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.mu.Lock()
			w.dirty = true
			w.mu.Unlock()
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() && !w.skip(filepath.Base(ev.Name)) {
					_ = w.addTree(ev.Name) // diretório novo: entra na observação
				}
			}
		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
			w.mu.Lock()
			w.dirty = true // na dúvida, a próxima consulta atualiza
			w.mu.Unlock()
		}
	}
}

// TakeDirty devolve se houve mudança desde a última chamada e limpa a
// marca. Um evento que chegue entre o Take e a atualização do índice fica
// para a consulta seguinte.
func (w *Watcher) TakeDirty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := w.dirty
	w.dirty = false
	return d
}

// Dirs é quantos diretórios estão observados.
func (w *Watcher) Dirs() int { return w.dirs }

func (w *Watcher) Close() error {
	if err := w.w.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
		return err
	}
	return nil
}
