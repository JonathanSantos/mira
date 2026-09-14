package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitDirty(t *testing.T, w *Watcher) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w.TakeDirty() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestWatcherMarksDirtyOnChanges(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src", "node_modules", "dep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "a.ts"), []byte("a"), 0o644))
	w, err := New(root, func(name string) bool { return name == "node_modules" })
	require.NoError(t, err)
	defer func() { _ = w.Close() }()
	assert.Equal(t, 2, w.Dirs(), "root and src; node_modules is skipped")
	assert.True(t, w.TakeDirty(), "starts dirty so the first query refreshes")
	assert.False(t, w.TakeDirty())

	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "a.ts"), []byte("changed"), 0o644))
	assert.True(t, waitDirty(t, w), "a write marks dirty")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "src", "new"), 0o755))
	assert.True(t, waitDirty(t, w))
	time.Sleep(50 * time.Millisecond) // o diretório novo entra na observação
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "new", "b.ts"), []byte("b"), 0o644))
	assert.True(t, waitDirty(t, w), "files inside a new directory are seen")
}
