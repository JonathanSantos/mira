package store

import (
	"fmt"
	"strings"
)

// SQL das inserções em lote: constantes para que Tx.exec reaproveite o
// statement preparado entre os arquivos de uma transação.
const (
	insertWordSQL = `INSERT INTO words (word, file_id, line, kind) VALUES (?, ?, ?, ?)`
	insertRefSQL  = `
		INSERT INTO refs (file_id, name, kind, line, col, receiver, receiver_type, container_symbol_id, resolution, arity, arg_types, receiver_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertSymbolSQL = `
		INSERT INTO symbols (file_id, name, qualified_name, kind, container, exported, export_name, jsx,
		                     start_line, end_line, start_byte, end_byte, signature, name_line, annotations, return_hint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertImportSQL = `
		INSERT INTO imports (file_id, kind, module, imported_name, local_name, is_reexport, is_wildcard, line, resolution)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
)

// DeleteFile remove o arquivo pelo caminho; symbols, refs, imports, words e
// edges vão junto pelo CASCADE. Refs de outros arquivos que apontavam para
// símbolos daqui ficam com resolved_symbol_id NULL (SET NULL) e precisam ser
// re-resolvidas: use Dependents antes de chamar isto.
func (t *Tx) DeleteFile(path string) error {
	if _, err := t.exec(`DELETE FROM files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("deleting file %s: %w", path, err)
	}
	return nil
}

// UpdateFile regrava os metadados de um arquivo alterado mantendo o id: os
// imports de outros arquivos apontam para ele.
func (t *Tx) UpdateFile(id int64, f File) error {
	if _, err := t.exec(`UPDATE files SET hash = ?, size = ?, mtime = ?, lang = ?, package = ?, parse_error = ?, indexed_at = ? WHERE id = ?`,
		f.Hash, f.Size, f.ModTime, string(f.Lang), f.Package, boolInt(f.ParseError), f.IndexedAt, id); err != nil {
		return fmt.Errorf("updating file %s: %w", f.Path, err)
	}
	return nil
}

// ClearFileContents apaga palavras, refs e imports do arquivo, que são
// regravados inteiros; os símbolos ficam, para manter os ids.
func (t *Tx) ClearFileContents(fileID int64) error {
	for _, table := range []string{"words", "refs", "imports"} {
		if _, err := t.exec(`DELETE FROM `+table+` WHERE file_id = ?`, fileID); err != nil {
			return fmt.Errorf("clearing %s of file %d: %w", table, fileID, err)
		}
	}
	return nil
}

// UpdateSymbol regrava um símbolo que continua no arquivo: posição,
// assinatura e o resto podem ter mudado, o id não.
func (t *Tx) UpdateSymbol(id int64, s Symbol) error {
	if _, err := t.exec(`
		UPDATE symbols SET name = ?, qualified_name = ?, kind = ?, container = ?, exported = ?, export_name = ?, jsx = ?,
			   start_line = ?, end_line = ?, start_byte = ?, end_byte = ?, signature = ?, name_line = ?, annotations = ?, return_hint = ?
		WHERE id = ?`,
		s.Name, s.QualifiedName, s.Kind, s.Container, boolInt(s.Exported), s.ExportName, boolInt(s.JSX),
		s.StartLine, s.EndLine, s.StartByte, s.EndByte, s.Signature, s.NameLine, strings.Join(s.Annotations, "\n"), s.ReturnHint, id); err != nil {
		return fmt.Errorf("updating symbol %s: %w", s.Name, err)
	}
	return nil
}

func (t *Tx) DeleteSymbol(id int64) error {
	if _, err := t.exec(`DELETE FROM symbols WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting symbol %d: %w", id, err)
	}
	return nil
}

func (t *Tx) InsertFile(f File) (int64, error) {
	res, err := t.exec(`
		INSERT INTO files (path, hash, size, mtime, lang, package, parse_error, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.Path, f.Hash, f.Size, f.ModTime, string(f.Lang), f.Package, boolInt(f.ParseError), f.IndexedAt)
	if err != nil {
		return 0, fmt.Errorf("inserting file %s: %w", f.Path, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading id of %s: %w", f.Path, err)
	}
	return id, nil
}

// UpdateFingerprint atualiza size/mtime de um arquivo cujo conteúdo não
// mudou (hash igual), para que a próxima checagem barata pule o arquivo.
func (t *Tx) UpdateFingerprint(id, size, mtime int64) error {
	if _, err := t.exec(`UPDATE files SET size = ?, mtime = ? WHERE id = ?`, size, mtime, id); err != nil {
		return fmt.Errorf("updating fingerprint of file %d: %w", id, err)
	}
	return nil
}

func (t *Tx) InsertWords(fileID int64, words []Word) error {
	for _, w := range words {
		kind := w.Kind
		if kind == "" {
			kind = WordIdent
		}
		if _, err := t.exec(insertWordSQL, w.Text, fileID, w.Line, kind); err != nil {
			return fmt.Errorf("inserting word %q: %w", w.Text, err)
		}
	}
	return nil
}

// InsertSymbols devolve os ids gerados na mesma ordem da entrada, para que
// refs possam apontar o container por índice.
func (t *Tx) InsertSymbols(fileID int64, symbols []Symbol) ([]int64, error) {
	ids := make([]int64, 0, len(symbols))
	for _, s := range symbols {
		res, err := t.exec(insertSymbolSQL, fileID, s.Name, s.QualifiedName, s.Kind, s.Container, boolInt(s.Exported),
			s.ExportName, boolInt(s.JSX), s.StartLine, s.EndLine, s.StartByte, s.EndByte, s.Signature,
			s.NameLine, strings.Join(s.Annotations, "\n"), s.ReturnHint)
		if err != nil {
			return nil, fmt.Errorf("inserting symbol %s: %w", s.Name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("reading id of symbol %s: %w", s.Name, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// InsertRefs grava refs ainda não resolvidas; symbolIDs mapeia
// Ref.ContainerIndex para o id real do símbolo container.
func (t *Tx) InsertRefs(fileID int64, refs []Ref, symbolIDs []int64) error {
	for _, r := range refs {
		var container *int64
		if r.ContainerIndex >= 0 && r.ContainerIndex < len(symbolIDs) {
			container = &symbolIDs[r.ContainerIndex]
		}
		if _, err := t.exec(insertRefSQL, fileID, r.Name, r.Kind, r.Line, r.Col, r.Receiver, r.ReceiverType, container, Unresolved,
			r.Arity, strings.Join(r.ArgTypes, ","), strings.Join(r.ReceiverPath, ".")); err != nil {
			return fmt.Errorf("inserting ref %s: %w", r.Name, err)
		}
	}
	return nil
}

func (t *Tx) InsertImports(fileID int64, imports []Import) error {
	for _, im := range imports {
		if _, err := t.exec(insertImportSQL, fileID, im.Kind, im.Module, im.ImportedName, im.LocalName,
			boolInt(im.IsReexport), boolInt(im.IsWildcard), im.Line, Unresolved); err != nil {
			return fmt.Errorf("inserting import %s: %w", im.Module, err)
		}
	}
	return nil
}

// ClearResolution volta refs e imports do arquivo para "unresolved" e apaga
// as edges que saem dos símbolos dele. É o primeiro passo de uma
// re-resolução.
func (t *Tx) ClearResolution(fileID int64) error {
	stmts := []string{
		`UPDATE refs SET resolved_symbol_id = NULL, resolution = 'unresolved' WHERE file_id = ?`,
		`UPDATE imports SET resolved_file_id = NULL, resolved_symbol_id = NULL, resolution = 'unresolved' WHERE file_id = ?`,
		`DELETE FROM edges WHERE from_symbol_id IN (SELECT id FROM symbols WHERE file_id = ?)`,
	}
	for _, q := range stmts {
		if _, err := t.exec(q, fileID); err != nil {
			return fmt.Errorf("clearing resolution of file %d: %w", fileID, err)
		}
	}
	return nil
}

func (t *Tx) SetRefResolution(refID int64, symbolID *int64, resolution string) error {
	if _, err := t.exec(`UPDATE refs SET resolved_symbol_id = ?, resolution = ? WHERE id = ?`,
		symbolID, resolution, refID); err != nil {
		return fmt.Errorf("resolving ref %d: %w", refID, err)
	}
	return nil
}

func (t *Tx) SetImportResolution(importID int64, fileID, symbolID *int64, resolution string) error {
	if _, err := t.exec(`UPDATE imports SET resolved_file_id = ?, resolved_symbol_id = ?, resolution = ? WHERE id = ?`,
		fileID, symbolID, resolution, importID); err != nil {
		return fmt.Errorf("resolving import %d: %w", importID, err)
	}
	return nil
}

func (t *Tx) InsertEdge(e Edge) error {
	if _, err := t.exec(`INSERT OR IGNORE INTO edges (from_symbol_id, to_symbol_id, kind) VALUES (?, ?, ?)`,
		e.From, e.To, e.Kind); err != nil {
		return fmt.Errorf("inserting edge %d->%d: %w", e.From, e.To, err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
