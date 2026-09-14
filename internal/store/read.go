package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/JonathanSantos/mira/internal/lang"
)

const fileColumns = `id, path, hash, size, mtime, lang, package, parse_error, indexed_at`

func scanFile(row interface{ Scan(...any) error }) (File, error) {
	var f File
	var langName string
	var parseErr int
	err := row.Scan(&f.ID, &f.Path, &f.Hash, &f.Size, &f.ModTime, &langName, &f.Package, &parseErr, &f.IndexedAt)
	if err != nil {
		return File{}, err
	}
	f.Lang = lang.Lang(langName)
	f.ParseError = parseErr != 0
	return f, nil
}

// Files devolve todos os arquivos ordenados por caminho.
func (s *Store) Files() ([]File, error) {
	rows, err := s.db.Query(`SELECT ` + fileColumns + ` FROM files ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("listing files: %w", err)
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *Store) FileByPath(path string) (File, bool, error) {
	f, err := scanFile(s.db.QueryRow(`SELECT `+fileColumns+` FROM files WHERE path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("reading file %s: %w", path, err)
	}
	return f, true, nil
}

func (s *Store) FileByID(id int64) (File, bool, error) {
	f, err := scanFile(s.db.QueryRow(`SELECT `+fileColumns+` FROM files WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("reading file %d: %w", id, err)
	}
	return f, true, nil
}

const symbolColumns = `s.id, s.file_id, s.name, s.qualified_name, s.kind, s.container, s.exported, s.export_name, s.jsx,
	s.start_line, s.end_line, s.start_byte, s.end_byte, s.signature, s.name_line, s.annotations, s.return_hint`

// querySymbols junta files para ordenar por caminho: ids de arquivo
// dependem da ordem em que os workers terminam e não são determinísticos.
func (s *Store) querySymbols(query string, args ...any) ([]Symbol, error) {
	rows, err := s.db.Query(`SELECT `+symbolColumns+` FROM symbols s JOIN files f ON f.id = s.file_id `+query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying symbols: %w", err)
	}
	defer rows.Close()
	var out []Symbol
	for rows.Next() {
		var sym Symbol
		var exported, jsx int
		var annotations string
		if err := rows.Scan(&sym.ID, &sym.FileID, &sym.Name, &sym.QualifiedName, &sym.Kind, &sym.Container,
			&exported, &sym.ExportName, &jsx, &sym.StartLine, &sym.EndLine, &sym.StartByte, &sym.EndByte, &sym.Signature,
			&sym.NameLine, &annotations, &sym.ReturnHint); err != nil {
			return nil, fmt.Errorf("scanning symbol: %w", err)
		}
		sym.Exported = exported != 0
		sym.JSX = jsx != 0
		if annotations != "" {
			sym.Annotations = strings.Split(annotations, "\n")
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

func (s *Store) SymbolsOfFile(fileID int64) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.file_id = ? ORDER BY s.start_line, s.start_byte`, fileID)
}

func (s *Store) SymbolsByName(name string) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.name = ? ORDER BY f.path, s.start_line`, name)
}

func (s *Store) SymbolsByQualifiedName(qualified string) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.qualified_name = ? ORDER BY f.path, s.start_line`, qualified)
}

// SymbolsByExportName acha o símbolo que um arquivo exporta sob um nome
// público ("default", alias, etc.).
func (s *Store) SymbolsByExportName(fileID int64, exportName string) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.file_id = ? AND s.exported = 1 AND s.export_name = ? ORDER BY s.start_line`, fileID, exportName)
}

// SymbolsInContainer acha membros (métodos, campos) de um tipo no arquivo.
func (s *Store) SymbolsInContainer(fileID int64, container, name string) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.file_id = ? AND s.container = ? AND s.name = ? ORDER BY s.start_line`, fileID, container, name)
}

// SymbolAt acha o símbolo mais interno que cobre a linha.
// SymbolsInPackage lista os símbolos de topo com o nome em todos os
// arquivos do pacote (Go: o diretório é o pacote).
func (s *Store) SymbolsInPackage(pkg, name string) ([]Symbol, error) {
	return s.querySymbols(`WHERE f.package = ? AND s.name = ? AND s.container = '' ORDER BY f.path, s.start_line`, pkg, name)
}

// MembersInPackage lista os membros de um tipo com o nome em todos os
// arquivos do pacote: em Go os métodos de um tipo se espalham por arquivos.
func (s *Store) MembersInPackage(pkg, container, name string) ([]Symbol, error) {
	return s.querySymbols(`WHERE f.package = ? AND s.container = ? AND s.name = ? ORDER BY f.path, s.start_line`, pkg, container, name)
}

func (s *Store) SymbolAt(fileID int64, line int) (Symbol, bool, error) {
	syms, err := s.querySymbols(`WHERE s.file_id = ? AND s.start_line <= ? AND s.end_line >= ?
		ORDER BY (s.end_line - s.start_line) ASC LIMIT 1`, fileID, line, line)
	if err != nil || len(syms) == 0 {
		return Symbol{}, false, err
	}
	return syms[0], true, nil
}

func (s *Store) SymbolByID(id int64) (Symbol, bool, error) {
	syms, err := s.querySymbols(`WHERE s.id = ?`, id)
	if err != nil || len(syms) == 0 {
		return Symbol{}, false, err
	}
	return syms[0], true, nil
}

const refColumns = `r.id, r.file_id, r.name, r.kind, r.line, r.col, r.receiver, r.receiver_type, r.container_symbol_id, r.resolved_symbol_id, r.resolution, r.arity, r.arg_types, r.receiver_path`

func (s *Store) queryRefs(query string, args ...any) ([]Ref, error) {
	rows, err := s.db.Query(`SELECT `+refColumns+` FROM refs r JOIN files f ON f.id = r.file_id `+query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying refs: %w", err)
	}
	defer rows.Close()
	var out []Ref
	for rows.Next() {
		var r Ref
		var container, resolved sql.NullInt64
		var argTypes, receiverPath string
		if err := rows.Scan(&r.ID, &r.FileID, &r.Name, &r.Kind, &r.Line, &r.Col, &r.Receiver, &r.ReceiverType,
			&container, &resolved, &r.Resolution, &r.Arity, &argTypes, &receiverPath); err != nil {
			return nil, fmt.Errorf("scanning ref: %w", err)
		}
		if r.Arity > 0 {
			r.ArgTypes = strings.Split(argTypes, ",")
		}
		if receiverPath != "" {
			r.ReceiverPath = strings.Split(receiverPath, ".")
		}
		r.ContainerIndex = -1
		r.ContainerSymbol = nullable(container)
		r.ResolvedSymbolID = nullable(resolved)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RefsOfFile(fileID int64) ([]Ref, error) {
	return s.queryRefs(`WHERE r.file_id = ? ORDER BY r.line, r.col`, fileID)
}

func (s *Store) RefsByName(name string) ([]Ref, error) {
	return s.queryRefs(`WHERE r.name = ? ORDER BY f.path, r.line, r.col`, name)
}

// RefsByReceiver lista as refs cujo receptor é o nome (`Money.ZERO` tem
// receptor Money): o rename acha por aqui os acessos pelo nome do tipo.
func (s *Store) RefsByReceiver(receiver string) ([]Ref, error) {
	return s.queryRefs(`WHERE r.receiver = ? ORDER BY f.path, r.line, r.col`, receiver)
}

// RefsTo lista as referências que resolveram para um símbolo específico.
func (s *Store) RefsTo(symbolID int64) ([]Ref, error) {
	return s.queryRefs(`WHERE r.resolved_symbol_id = ? ORDER BY f.path, r.line, r.col`, symbolID)
}

const importColumns = `id, file_id, kind, module, imported_name, local_name, is_reexport, is_wildcard, line,
	resolved_file_id, resolved_symbol_id, resolution`

func (s *Store) queryImports(query string, args ...any) ([]Import, error) {
	rows, err := s.db.Query(`SELECT `+importColumns+` FROM imports `+query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying imports: %w", err)
	}
	defer rows.Close()
	var out []Import
	for rows.Next() {
		var im Import
		var reexport, wildcard int
		var file, sym sql.NullInt64
		if err := rows.Scan(&im.ID, &im.FileID, &im.Kind, &im.Module, &im.ImportedName, &im.LocalName,
			&reexport, &wildcard, &im.Line, &file, &sym, &im.Resolution); err != nil {
			return nil, fmt.Errorf("scanning import: %w", err)
		}
		im.IsReexport = reexport != 0
		im.IsWildcard = wildcard != 0
		im.ResolvedFileID = nullable(file)
		im.ResolvedSymbolID = nullable(sym)
		out = append(out, im)
	}
	return out, rows.Err()
}

func (s *Store) ImportsOfFile(fileID int64) ([]Import, error) {
	return s.queryImports(`WHERE file_id = ? ORDER BY line, id`, fileID)
}

// ImportsResolvedTo lista as linhas de import (e re-export) resolvidas para
// o símbolo: re-exports não viram refs, e o rename os acha por aqui.
func (s *Store) ImportsResolvedTo(symbolID int64) ([]Import, error) {
	return s.queryImports(`WHERE resolved_symbol_id = ? ORDER BY line, id`, symbolID)
}

// Dependents lista os arquivos cuja resolução depende do arquivo dado: os
// que importam dele e os que têm refs resolvidas para símbolos dele.
func (s *Store) Dependents(fileID int64) ([]int64, error) {
	return s.queryIDs(`
		SELECT DISTINCT file_id FROM imports WHERE resolved_file_id = ? AND file_id != ?
		UNION
		SELECT DISTINCT r.file_id FROM refs r JOIN symbols sy ON sy.id = r.resolved_symbol_id
		WHERE sy.file_id = ? AND r.file_id != ?`, fileID, fileID, fileID, fileID)
}

// FilesWithPendingImports lista arquivos com imports não resolvidos que não
// são externos: um arquivo novo pode ser o alvo que faltava.
func (s *Store) FilesWithPendingImports() ([]int64, error) {
	return s.queryIDs(`SELECT DISTINCT file_id FROM imports WHERE resolution = 'unresolved'`)
}

// Importers lista os arquivos cujos imports resolvem para o arquivo dado.
func (s *Store) Importers(fileID int64) ([]int64, error) {
	return s.queryIDs(`SELECT DISTINCT file_id FROM imports WHERE resolved_file_id = ? AND file_id != ?`, fileID, fileID)
}

// FilesReferencing lista os arquivos, fora o dado, com refs ou imports
// resolvidos para algum dos símbolos: são os que um símbolo alterado ou
// removido obriga a resolver de novo.
func (s *Store) FilesReferencing(symbolIDs []int64, except int64) ([]int64, error) {
	values := make([]any, len(symbolIDs))
	for i, id := range symbolIDs {
		values[i] = id
	}
	refs, err := s.filesMatching(`SELECT DISTINCT file_id FROM refs WHERE resolved_symbol_id IN (%s) AND file_id != ?`, values, except)
	if err != nil {
		return nil, err
	}
	imports, err := s.filesMatching(`SELECT DISTINCT file_id FROM imports WHERE resolved_symbol_id IN (%s) AND file_id != ?`, values, except)
	if err != nil {
		return nil, err
	}
	return append(refs, imports...), nil
}

// FilesWithAmbiguousRefsNamed lista arquivos com refs ambíguas para algum dos
// nomes: só uma definição com o mesmo nome pode mudar o veredito.
func (s *Store) FilesWithAmbiguousRefsNamed(names []string) ([]int64, error) {
	return s.filesMatching(`SELECT DISTINCT file_id FROM refs WHERE resolution = 'ambiguous' AND name IN (%s) AND file_id != ?`, stringValues(names), -1)
}

// FilesWithPendingImportsNamed lista arquivos com imports não resolvidos de
// algum dos nomes: o nome apareceu, ou voltou, em algum arquivo.
func (s *Store) FilesWithPendingImportsNamed(names []string) ([]int64, error) {
	return s.filesMatching(`SELECT DISTINCT file_id FROM imports WHERE resolution = 'unresolved' AND imported_name IN (%s) AND file_id != ?`, stringValues(names), -1)
}

// maxParams limita os valores de um IN por consulta: o SQLite aceita um
// número finito de parâmetros por comando.
const maxParams = 500

// filesMatching roda query em lotes, com %s no lugar da lista do IN e o
// último ? para o arquivo a excluir, e junta os ids sem repetir.
func (s *Store) filesMatching(query string, values []any, except int64) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	for start := 0; start < len(values); start += maxParams {
		chunk := values[start:min(start+maxParams, len(values))]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		ids, err := s.queryIDs(strings.Replace(query, "%s", marks, 1), append(append([]any{}, chunk...), except)...)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func stringValues(names []string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

// FilesInPackage lista arquivos Java de uma package (resolução implícita de
// mesma package não passa por imports).
func (s *Store) FilesInPackage(pkg string, except int64) ([]int64, error) {
	return s.queryIDs(`SELECT id FROM files WHERE package = ? AND package != '' AND id != ?`, pkg, except)
}

func (s *Store) queryIDs(query string, args ...any) ([]int64, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying ids: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Callers devolve os símbolos com edge (kind) apontando para o símbolo.
func (s *Store) Callers(symbolID int64, kind string) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.id IN (SELECT from_symbol_id FROM edges WHERE to_symbol_id = ? AND kind = ?)
		ORDER BY f.path, s.start_line`, symbolID, kind)
}

// Callees devolve os símbolos alcançados por edges (kind) que saem do símbolo.
func (s *Store) Callees(symbolID int64, kind string) ([]Symbol, error) {
	return s.querySymbols(`WHERE s.id IN (SELECT to_symbol_id FROM edges WHERE from_symbol_id = ? AND kind = ?)
		ORDER BY f.path, s.start_line`, symbolID, kind)
}

// EdgesFrom lista as edges que saem de um símbolo.
func (s *Store) EdgesFrom(symbolID int64) ([]Edge, error) {
	rows, err := s.db.Query(`SELECT from_symbol_id, to_symbol_id, kind FROM edges WHERE from_symbol_id = ?`, symbolID)
	if err != nil {
		return nil, fmt.Errorf("querying edges: %w", err)
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To, &e.Kind); err != nil {
			return nil, fmt.Errorf("scanning edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WordHit é um resultado do índice lexical. Definition marca a linha em que
// um símbolo com esse nome é declarado (para sub-tokens, um símbolo cujo
// nome contém o pedaço).
type WordHit struct {
	FileID     int64
	Path       string
	Line       int
	Kind       string
	Definition bool
}

// WordHits busca a palavra exata (identificador, string, comentário, texto)
// e, em minúsculas, como sub-token de identificadores compostos. Exatos e
// definições vêm primeiro.
func (s *Store) WordHits(word, pathPrefix string, limit int) ([]WordHit, error) {
	rows, err := s.db.Query(`
		SELECT f.id, f.path, w.line, w.kind,
			EXISTS(SELECT 1 FROM symbols sy WHERE sy.file_id = w.file_id AND sy.name_line = w.line
				AND (sy.name = w.word OR (w.kind = 'part' AND instr(lower(sy.name), w.word) > 0))) AS def
		FROM words w JOIN files f ON f.id = w.file_id
		WHERE ((w.word = ? AND w.kind != 'part') OR (w.word = ? AND w.kind = 'part')) AND f.path LIKE ? || '%'
		ORDER BY def DESC, (w.kind = 'part') ASC, f.path, w.line LIMIT ?`, word, strings.ToLower(word), pathPrefix, limit)
	if err != nil {
		return nil, fmt.Errorf("searching %q: %w", word, err)
	}
	defer rows.Close()
	var hits []WordHit
	for rows.Next() {
		var h WordHit
		var def int
		if err := rows.Scan(&h.FileID, &h.Path, &h.Line, &h.Kind, &def); err != nil {
			return nil, fmt.Errorf("scanning hit: %w", err)
		}
		h.Definition = def != 0
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// FileSummary é um arquivo com a contagem de símbolos, para a árvore.
type FileSummary struct {
	ID         int64
	Path       string
	Lang       lang.Lang
	ParseError bool
	Symbols    int
}

// FileSummaries lista os arquivos (opcionalmente sob um prefixo) com a
// contagem de símbolos, ordenados por caminho.
func (s *Store) FileSummaries(pathPrefix string) ([]FileSummary, error) {
	rows, err := s.db.Query(`
		SELECT f.id, f.path, f.lang, f.parse_error, (SELECT COUNT(*) FROM symbols sy WHERE sy.file_id = f.id)
		FROM files f WHERE f.path LIKE ? || '%' ORDER BY f.path`, pathPrefix)
	if err != nil {
		return nil, fmt.Errorf("listing files: %w", err)
	}
	defer rows.Close()
	var out []FileSummary
	for rows.Next() {
		var f FileSummary
		var langName string
		var parseErr int
		if err := rows.Scan(&f.ID, &f.Path, &langName, &parseErr, &f.Symbols); err != nil {
			return nil, fmt.Errorf("scanning file summary: %w", err)
		}
		f.Lang = lang.Lang(langName)
		f.ParseError = parseErr != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// LangCounts são as contagens de uma linguagem.
type LangCounts struct {
	Files   int `json:"files"`
	Symbols int `json:"symbols"`
	Refs    int `json:"refs"`
	Imports int `json:"imports"`
	Words   int `json:"words"`
}

// Counts devolve contagens totais e por linguagem.
func (s *Store) Counts() (total LangCounts, edges int, byLang map[string]LangCounts, err error) {
	byLang = map[string]LangCounts{}
	rows, err := s.db.Query(`
		SELECT f.lang, COUNT(DISTINCT f.id),
			(SELECT COUNT(*) FROM symbols sy JOIN files ff ON ff.id = sy.file_id WHERE ff.lang = f.lang),
			(SELECT COUNT(*) FROM refs r JOIN files ff ON ff.id = r.file_id WHERE ff.lang = f.lang),
			(SELECT COUNT(*) FROM imports i JOIN files ff ON ff.id = i.file_id WHERE ff.lang = f.lang),
			(SELECT COUNT(*) FROM words w JOIN files ff ON ff.id = w.file_id WHERE ff.lang = f.lang)
		FROM files f GROUP BY f.lang`)
	if err != nil {
		return total, 0, nil, fmt.Errorf("counting: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var c LangCounts
		if err := rows.Scan(&name, &c.Files, &c.Symbols, &c.Refs, &c.Imports, &c.Words); err != nil {
			return total, 0, nil, fmt.Errorf("scanning counts: %w", err)
		}
		byLang[name] = c
		total.Files += c.Files
		total.Symbols += c.Symbols
		total.Refs += c.Refs
		total.Imports += c.Imports
		total.Words += c.Words
	}
	if err := rows.Err(); err != nil {
		return total, 0, nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM edges`).Scan(&edges); err != nil {
		return total, 0, nil, fmt.Errorf("counting edges: %w", err)
	}
	return total, edges, byLang, nil
}

func nullable(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// PackageExists diz se alguma package Java indexada tem esse nome.
func (s *Store) PackageExists(pkg string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files WHERE package = ?`, pkg).Scan(&n); err != nil {
		return false, fmt.Errorf("checking package %s: %w", pkg, err)
	}
	return n > 0, nil
}

// RefsInContainer lista as referências feitas de dentro de um símbolo (o
// escopo próprio: refs de funções aninhadas indexadas pertencem a elas).
func (s *Store) RefsInContainer(symbolID int64) ([]Ref, error) {
	return s.queryRefs(`WHERE r.container_symbol_id = ? ORDER BY r.line, r.col`, symbolID)
}
