package indexer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

// base: lib.ts é usado por use.ts; pending.ts importa um arquivo que não existe.
var base = map[string]string{
	"lib.ts":     "export function f() {\n  return 1;\n}\n",
	"use.ts":     "import { f } from './lib';\nexport function g() {\n  return f();\n}\n",
	"pending.ts": "import { h } from './missing';\nexport function k() {\n  return h();\n}\n",
}

// setup escreve base num repositório temporário e faz a primeira indexação.
func setup(t *testing.T) (*indexer.Indexer, *store.Store, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range base {
		write(t, root, name, content)
	}
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ix := indexer.New(root, repo.DefaultConfig(), st)
	_, err = ix.Run(context.Background(), indexer.Options{Workers: 2})
	require.NoError(t, err)
	return ix, st, root
}

// write grava o arquivo com mtime adiante, para o fingerprint ver a mudança
// mesmo quando o tamanho não muda.
func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, later, later))
}

func resolution(t *testing.T, st *store.Store, file, name string) string {
	t.Helper()
	f, ok, err := st.FileByPath(file)
	require.NoError(t, err)
	require.True(t, ok, file)
	refs, err := st.RefsOfFile(f.ID)
	require.NoError(t, err)
	for _, r := range refs {
		if r.Name == name && r.Kind == "call" {
			return r.Resolution
		}
	}
	return ""
}

func TestRefreshResolvesOnlyWhatCanChange(t *testing.T) {
	tests := []struct {
		name         string
		edit         func(t *testing.T, root string)
		wantChanged  bool
		wantResolved int
	}{
		{
			name:         "no change resolves nothing",
			edit:         func(t *testing.T, root string) {},
			wantResolved: 0,
		},
		{
			name:         "body edit resolves only the file",
			edit:         func(t *testing.T, root string) { write(t, root, "lib.ts", "export function f() {\n  return 2;\n}\n") },
			wantChanged:  true,
			wantResolved: 1,
		},
		{
			name: "signature edit resolves the file and the users of that symbol",
			edit: func(t *testing.T, root string) {
				write(t, root, "lib.ts", "export function f(x = 0) {\n  return x;\n}\n")
			},
			wantChanged:  true,
			wantResolved: 2,
		},
		{
			name: "new file retries pending imports",
			edit: func(t *testing.T, root string) {
				write(t, root, "missing.ts", "export function h() {\n  return 3;\n}\n")
			},
			wantChanged:  true,
			wantResolved: 2,
		},
		{
			name:         "removed file retries its dependents",
			edit:         func(t *testing.T, root string) { require.NoError(t, os.Remove(filepath.Join(root, "lib.ts"))) },
			wantChanged:  true,
			wantResolved: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix, _, root := setup(t)
			tt.edit(t, root)
			report, changed, err := ix.Refresh(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.wantChanged, changed)
			assert.Equal(t, tt.wantResolved, report.Resolve.Files)
		})
	}
}

func TestNewFileResolvesPendingImport(t *testing.T) {
	ix, st, root := setup(t)
	assert.Equal(t, store.Unresolved, resolution(t, st, "pending.ts", "h"))
	write(t, root, "missing.ts", "export function h() {\n  return 3;\n}\n")
	_, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, store.Resolved, resolution(t, st, "pending.ts", "h"), "the pending import is retried when its target appears")
}

func TestReexportEditRetriesImporters(t *testing.T) {
	ix, st, root := setup(t)
	write(t, root, "barrel.ts", "export const version = 1;\n")
	write(t, root, "consumer.ts", "import { f } from './barrel';\nexport function c() {\n  return f();\n}\n")
	_, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, store.Unresolved, resolution(t, st, "consumer.ts", "f"), "barrel does not export f yet")

	write(t, root, "barrel.ts", "export const version = 1;\nexport { f } from './lib';\n")
	_, _, err = ix.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, store.Resolved, resolution(t, st, "consumer.ts", "f"), "a new re-export changes what the barrel exports, so importers are retried")
}

func TestTSConfigEditResolvesImportsAgain(t *testing.T) {
	ix, st, root := setup(t)
	write(t, root, "tsconfig.json", "{}\n")
	write(t, root, "src/lib/x.ts", "export function x() {\n  return 1;\n}\n")
	write(t, root, "src/app.ts", "import { x } from '@lib/x';\nexport function a() {\n  return x();\n}\n")
	_, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, store.Resolved, resolution(t, st, "src/app.ts", "x"), "without paths @lib/x is a package")

	write(t, root, "tsconfig.json", `{"compilerOptions": {"baseUrl": ".", "paths": {"@lib/*": ["src/lib/*"]}}}`+"\n")
	report, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, store.Resolved, resolution(t, st, "src/app.ts", "x"), "paths in tsconfig.json change where imports point")
	files, err := st.Files()
	require.NoError(t, err)
	assert.Equal(t, len(files), report.Resolve.Files, "a tsconfig edit resolves every file again")
}

// targetOf é o id do símbolo para o qual a chamada name em file resolveu.
func targetOf(t *testing.T, st *store.Store, file, name string) int64 {
	t.Helper()
	f, ok, err := st.FileByPath(file)
	require.NoError(t, err)
	require.True(t, ok, file)
	refs, err := st.RefsOfFile(f.ID)
	require.NoError(t, err)
	for _, r := range refs {
		if r.Name == name && r.Kind == "call" && r.ResolvedSymbolID != nil {
			return *r.ResolvedSymbolID
		}
	}
	require.Failf(t, "no resolved call", "%s in %s", name, file)
	return 0
}

func TestBodyEditKeepsSymbolIDs(t *testing.T) {
	ix, st, root := setup(t)
	before := targetOf(t, st, "use.ts", "f")
	write(t, root, "lib.ts", "// f moves one line down\nexport function f() {\n  return 2;\n}\n")
	report, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, report.Resolve.Files, "only lib.ts is resolved again")
	assert.Equal(t, before, targetOf(t, st, "use.ts", "f"), "use.ts still points at the same symbol id")
	sym, ok, err := st.SymbolByID(before)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 2, sym.StartLine, "the kept symbol has its new position")
}

func TestRemovedDefinitionReresolvesItsUsers(t *testing.T) {
	ix, st, root := setup(t)
	write(t, root, "lib.ts", "export function renamed() {\n  return 1;\n}\n")
	report, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, report.Resolve.Files, "lib.ts and use.ts, which called the removed f")
	assert.Equal(t, store.Unresolved, resolution(t, st, "use.ts", "f"))
}

func TestAmbiguousRefsRetriedByName(t *testing.T) {
	ix, st, root := setup(t)
	write(t, root, "a.ts", "export function dup() {}\n")
	write(t, root, "b.ts", "export function dup() {}\n")
	write(t, root, "amb.ts", "export function run() {\n  return dup();\n}\n")
	_, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, store.Ambiguous, resolution(t, st, "amb.ts", "dup"))

	write(t, root, "b.ts", "export function other() {}\n")
	report, _, err := ix.Refresh(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, store.Ambiguous, resolution(t, st, "amb.ts", "dup"), "a definition named dup disappeared, so amb.ts is retried")
	assert.Equal(t, 2, report.Resolve.Files, "b.ts and amb.ts, nothing else")
}

func TestFullRebuildsAsideAndSwaps(t *testing.T) {
	ix, st, root := setup(t)
	reader, err := store.Open(st.Path())
	require.NoError(t, err)
	defer reader.Close()
	require.NoError(t, os.Remove(filepath.Join(root, "pending.ts")))

	report, err := ix.Run(context.Background(), indexer.Options{Full: true, Workers: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, report.New, "every file on disk is written again")
	assert.Zero(t, report.Changed)
	assert.Equal(t, "swap", report.Phases[len(report.Phases)-1].Name)
	files, err := st.Files()
	require.NoError(t, err)
	assert.Len(t, files, 2, "a file gone from disk is not in the new index")
	assert.Equal(t, store.Resolved, resolution(t, st, "use.ts", "f"))
	others, err := reader.Files()
	require.NoError(t, err)
	assert.Len(t, others, 2, "a connection opened before the rebuild sees the new index")
	_, err = os.Stat(st.Path() + ".full")
	assert.True(t, os.IsNotExist(err), "the side database is removed")
}
