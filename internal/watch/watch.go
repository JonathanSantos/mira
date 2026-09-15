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
	"time"

	"github.com/fsnotify/fsnotify"
)

// maxDirs limita os diretórios observados: no macOS cada um é um descritor
// de arquivo, e o limite padrão do processo é baixo. Acima disso o chamador
// volta a caminhar o repositório a cada consulta.
const maxDirs = 1500

// Watcher marca dirty a cada evento em qualquer diretório observado.
type Watcher struct {
	w         *fsnotify.Watcher
	skip      func(name string) bool
	cookieDir string
	mu        sync.Mutex
	dirty     bool
	dirs      int
	seq       int
	cookies   map[string]chan struct{} // cookie criado por Sync -> quem espera o evento dele
	broken    bool                     // um cookie não chegou: Sync não espera mais
}

// New observa root e todos os subdiretórios que skip não rejeita, mais
// cookieDir, onde Sync cria seus cookies (o diretório do índice). Começa
// dirty, para a primeira consulta atualizar o índice.
func New(root, cookieDir string, skip func(name string) bool) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("starting watcher: %w", err)
	}
	w := &Watcher{w: fw, skip: skip, cookieDir: filepath.Clean(cookieDir), dirty: true, cookies: map[string]chan struct{}{}}
	if err := w.addTree(root); err != nil {
		_ = fw.Close()
		return nil, err
	}
	if err := fw.Add(w.cookieDir); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("watching %s: %w", w.cookieDir, err)
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
			// No diretório dos cookies só mudam o índice e os próprios cookies:
			// nada ali é código, então não marca dirty.
			if ev.Name == w.cookieDir || filepath.Dir(ev.Name) == w.cookieDir {
				w.wake(filepath.Base(ev.Name))
				continue
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

// Sync espera o watcher receber tudo o que aconteceu no disco antes da
// chamada, como os cookies do Watchman: cria um arquivo em cookieDir e
// aguarda o evento dele. Os eventos chegam na ordem em que aconteceram,
// então, quando o do cookie chega, as mudanças anteriores já marcaram dirty.
// São dois cookies seguidos porque o kqueue (macOS) junta eventos do mesmo
// diretório: o primeiro cookie pode vir grudado no evento de um cookie
// anterior, que está na fila antes de uma mudança recente; o segundo só é
// criado depois que essa fila andou. Devolve false se não deu para
// sincronizar; se um cookie não chegou dentro de timeout, o watcher deixa de
// ser confiável e Sync passa a devolver false sem esperar.
func (w *Watcher) Sync(timeout time.Duration) bool {
	for range 2 {
		if !w.cookie(timeout) {
			return false
		}
	}
	return true
}

// cookie cria um arquivo em cookieDir e espera o evento dele.
func (w *Watcher) cookie(timeout time.Duration) bool {
	w.mu.Lock()
	if w.broken {
		w.mu.Unlock()
		return false
	}
	w.seq++
	name := fmt.Sprintf("cookie-%d-%d", os.Getpid(), w.seq)
	seen := make(chan struct{})
	w.cookies[name] = seen
	w.mu.Unlock()
	defer w.wake(name) // se o evento não veio, tira o cookie da espera

	path := filepath.Join(w.cookieDir, name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return false
	}
	defer func() { _ = os.Remove(path) }()
	select {
	case <-seen:
		return true
	case <-time.After(timeout):
		w.mu.Lock()
		w.broken = true
		w.mu.Unlock()
		return false
	}
}

// wake acorda quem espera o cookie name, se alguém espera.
func (w *Watcher) wake(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if seen, ok := w.cookies[name]; ok {
		close(seen)
		delete(w.cookies, name)
	}
}

// TakeDirty devolve se houve mudança desde a última chamada e limpa a
// marca. Chame Sync antes para incluir gravações feitas logo antes da
// consulta. Um evento que chegue entre o Take e a atualização do índice fica
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
