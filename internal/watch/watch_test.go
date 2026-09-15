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
	w, err := New(root, t.TempDir(), func(name string) bool { return name == "node_modules" })
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

func TestSyncSeesChangesMadeJustBefore(t *testing.T) {
	tests := []struct {
		name   string
		change func(root string) error
		dirty  bool
	}{
		{name: "no change", change: func(string) error { return nil }, dirty: false},
		{name: "edit", change: func(root string) error {
			return os.WriteFile(filepath.Join(root, "a.ts"), []byte("changed"), 0o644)
		}, dirty: true},
		{name: "new file", change: func(root string) error {
			return os.WriteFile(filepath.Join(root, "b.ts"), []byte("b"), 0o644)
		}, dirty: true},
		{name: "removed file", change: func(root string) error {
			return os.Remove(filepath.Join(root, "a.ts"))
		}, dirty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, ".mira")
			require.NoError(t, os.MkdirAll(data, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "a.ts"), []byte("a"), 0o644))
			w, err := New(root, data, func(name string) bool { return name == ".mira" })
			require.NoError(t, err)
			defer func() { _ = w.Close() }()
			require.True(t, w.Sync(3*time.Second))
			w.TakeDirty()

			require.NoError(t, tt.change(root))
			require.True(t, w.Sync(3*time.Second), "the cookie event arrives")
			assert.Equal(t, tt.dirty, w.TakeDirty(), "checked with no sleep after the change; cookies never mark dirty")
		})
	}
}

func TestSyncGivesUpWhenEventsStop(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, t.TempDir(), func(string) bool { return false })
	require.NoError(t, err)
	require.NoError(t, w.Close())

	start := time.Now()
	assert.False(t, w.Sync(50*time.Millisecond), "a closed watcher never sees the cookie")
	assert.False(t, w.Sync(time.Hour), "after one timeout Sync stops waiting")
	assert.Less(t, time.Since(start), time.Second)
}
