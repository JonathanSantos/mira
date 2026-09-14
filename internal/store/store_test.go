package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/lang"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedFile grava um arquivo com um símbolo, uma ref, um import e uma palavra
// e devolve (file id, symbol id).
func seedFile(t *testing.T, s *Store, path string) (int64, int64) {
	t.Helper()
	tx, err := s.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	fileID, err := tx.InsertFile(File{Path: path, Hash: "h", Size: 1, ModTime: 1, Lang: lang.TypeScript, IndexedAt: 1})
	require.NoError(t, err)
	ids, err := tx.InsertSymbols(fileID, []Symbol{{Name: "f", QualifiedName: "f", Kind: "function", StartLine: 1, EndLine: 3, EndByte: 10,
		NameLine: 1, Annotations: []string{"@A", "@B(x)"}}})
	require.NoError(t, err)
	require.NoError(t, tx.InsertRefs(fileID, []Ref{{Name: "g", Kind: "call", Line: 2, Col: 3, ContainerIndex: 0}}, ids))
	require.NoError(t, tx.InsertImports(fileID, []Import{{Kind: "esm", Module: "./g", ImportedName: "g", LocalName: "g"}}))
	require.NoError(t, tx.InsertWords(fileID, []Word{{Text: "f", Line: 1}, {Text: "g", Line: 2}}))
	require.NoError(t, tx.Commit())
	return fileID, ids[0]
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	require.NoError(t, err)
	v, err := s.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, len(migrations), v)
	require.NoError(t, s.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	v, err = s.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, len(migrations), v)
}

func TestDeleteFileCascades(t *testing.T) {
	s := openTest(t)
	fileID, symID := seedFile(t, s, "a.ts")
	otherID, otherSym := seedFile(t, s, "b.ts")

	// b.ts referencia o símbolo de a.ts e tem uma edge para ele.
	tx, err := s.Begin()
	require.NoError(t, err)
	refs, err := s.RefsOfFile(otherID)
	require.NoError(t, err)
	require.NoError(t, tx.SetRefResolution(refs[0].ID, &symID, Resolved))
	require.NoError(t, tx.InsertEdge(Edge{From: otherSym, To: symID, Kind: EdgeCalls}))
	require.NoError(t, tx.Commit())

	deps, err := s.Dependents(fileID)
	require.NoError(t, err)
	assert.Equal(t, []int64{otherID}, deps)

	tx, err = s.Begin()
	require.NoError(t, err)
	require.NoError(t, tx.DeleteFile("a.ts"))
	require.NoError(t, tx.Commit())

	syms, err := s.SymbolsOfFile(fileID)
	require.NoError(t, err)
	assert.Empty(t, syms)
	refs, err = s.RefsOfFile(fileID)
	require.NoError(t, err)
	assert.Empty(t, refs)
	imports, err := s.ImportsOfFile(fileID)
	require.NoError(t, err)
	assert.Empty(t, imports)
	hits, err := s.WordHits("f", "", 10)
	require.NoError(t, err)
	assert.Equal(t, []WordHit{{FileID: otherID, Path: "b.ts", Line: 1, Kind: WordIdent, Definition: true}}, hits)

	// A ref de b.ts perdeu o alvo (SET NULL) e a edge sumiu (CASCADE).
	refs, err = s.RefsOfFile(otherID)
	require.NoError(t, err)
	assert.Nil(t, refs[0].ResolvedSymbolID)
	callers, err := s.Callers(symID, EdgeCalls)
	require.NoError(t, err)
	assert.Empty(t, callers)
	edges, err := s.EdgesFrom(otherSym)
	require.NoError(t, err)
	assert.Empty(t, edges)
}

func TestGraphQueries(t *testing.T) {
	s := openTest(t)
	fileA, symA := seedFile(t, s, "a.ts")
	_, symB := seedFile(t, s, "b.ts")

	tx, err := s.Begin()
	require.NoError(t, err)
	require.NoError(t, tx.InsertEdge(Edge{From: symB, To: symA, Kind: EdgeCalls}))
	require.NoError(t, tx.InsertEdge(Edge{From: symB, To: symA, Kind: EdgeCalls})) // duplicata ignorada
	require.NoError(t, tx.Commit())

	callers, err := s.Callers(symA, EdgeCalls)
	require.NoError(t, err)
	require.Len(t, callers, 1)
	assert.Equal(t, symB, callers[0].ID)

	callees, err := s.Callees(symB, EdgeCalls)
	require.NoError(t, err)
	require.Len(t, callees, 1)
	assert.Equal(t, symA, callees[0].ID)

	byName, err := s.SymbolsByName("f")
	require.NoError(t, err)
	assert.Len(t, byName, 2)
	assert.Equal(t, []string{"@A", "@B(x)"}, byName[0].Annotations)
	assert.Equal(t, 1, byName[0].NameLine)

	// Busca por prefixo de caminho e resumo de arquivos.
	hits, err := s.WordHits("f", "b.", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "b.ts", hits[0].Path)
	summaries, err := s.FileSummaries("")
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, 1, summaries[0].Symbols)

	at, ok, err := s.SymbolAt(fileA, 2)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, symA, at.ID)

	total, edges, byLang, err := s.Counts()
	require.NoError(t, err)
	assert.Equal(t, LangCounts{Files: 2, Symbols: 2, Refs: 2, Imports: 2, Words: 4}, total)
	assert.Equal(t, 1, edges)
	assert.Equal(t, 2, byLang["typescript"].Files)

	f, found, err := s.FileByPath("a.ts")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, fileA, f.ID)

	// ClearResolution zera refs/imports e apaga edges que saem do arquivo.
	tx, err = s.Begin()
	require.NoError(t, err)
	require.NoError(t, tx.ClearResolution(f.ID))
	require.NoError(t, tx.Commit())
	refs, err := s.RefsOfFile(f.ID)
	require.NoError(t, err)
	assert.Equal(t, Unresolved, refs[0].Resolution)
}

// TestDeleteIndexesOnUpgrade: um banco na v5 ganha, ao abrir, os índices que
// deixam apagar símbolos barato, sem perder os dados indexados.
func TestDeleteIndexesOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	for _, ddl := range migrations[:5] {
		_, err := raw.Exec(ddl)
		require.NoError(t, err)
	}
	_, err = raw.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL); INSERT INTO schema_version VALUES (5);
		INSERT INTO files (path, hash, size, mtime, lang, indexed_at) VALUES ('a.ts', 'h', 1, 1, 'typescript', 1)`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	s, err := Open(path)
	require.NoError(t, err)
	defer s.Close()
	v, err := s.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, len(migrations), v)
	var indexes []string
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND name IN ('idx_refs_container', 'idx_imports_resolved_symbol') ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		indexes = append(indexes, name)
	}
	assert.Equal(t, []string{"idx_imports_resolved_symbol", "idx_refs_container"}, indexes)
	files, err := s.Files()
	require.NoError(t, err)
	assert.Len(t, files, 1, "the upgrade keeps the indexed files")
}

// TestReplaceWithIsAtomicForReaders: outra conexão com o banco aberto (um
// servidor MCP) vê o índice antigo durante a troca e o novo depois, sem reabrir.
func TestReplaceWithIsAtomicForReaders(t *testing.T) {
	dir := t.TempDir()
	live, err := Open(filepath.Join(dir, "index.db"))
	require.NoError(t, err)
	defer live.Close()
	seedFile(t, live, "old.ts")

	nextPath := filepath.Join(dir, "next.db")
	next, err := Open(nextPath)
	require.NoError(t, err)
	seedFile(t, next, "new1.ts")
	seedFile(t, next, "new2.ts")
	require.NoError(t, next.Close())

	reader, err := Open(live.Path())
	require.NoError(t, err)
	defer reader.Close()
	snapshot, err := reader.db.Begin()
	require.NoError(t, err)
	count := func(q interface{ QueryRow(string, ...any) *sql.Row }) int {
		var n int
		require.NoError(t, q.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n))
		return n
	}
	require.Equal(t, 1, count(snapshot))

	require.NoError(t, live.ReplaceWith(nextPath))
	assert.Equal(t, 1, count(snapshot), "a read already in progress keeps the old index")
	require.NoError(t, snapshot.Rollback())
	assert.Equal(t, 2, count(reader.db), "the next read sees the new index without reopening")
	files, err := live.Files()
	require.NoError(t, err)
	assert.Len(t, files, 2)
	v, err := reader.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, len(migrations), v)
}

func TestFilesMatchingInBatches(t *testing.T) {
	s := openTest(t)
	fileID, _ := seedFile(t, s, "a.ts")
	refs, err := s.RefsOfFile(fileID)
	require.NoError(t, err)
	tx, err := s.Begin()
	require.NoError(t, err)
	require.NoError(t, tx.SetRefResolution(refs[0].ID, nil, Ambiguous))
	require.NoError(t, tx.Commit())
	names := make([]string, 0, 1201)
	for i := 0; i < 1200; i++ {
		names = append(names, fmt.Sprintf("n%d", i))
	}
	names = append(names, "g", "g")
	ids, err := s.FilesWithAmbiguousRefsNamed(names)
	require.NoError(t, err)
	assert.Equal(t, []int64{fileID}, ids, "a match in the last batch is found once")
}
