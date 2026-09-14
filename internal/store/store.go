// Package store é a camada SQLite: schema, migrações e todas as queries.
// Nenhum outro pacote escreve SQL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	sqlite "modernc.org/sqlite" // driver "sqlite", Go puro

	"github.com/JonathanSantos/mira/internal/lang"
)

// Estados de resolução de refs e imports.
const (
	Resolved   = "resolved"
	Ambiguous  = "ambiguous"
	External   = "external"
	Unresolved = "unresolved"
)

// Kinds de edge no grafo.
const (
	EdgeCalls       = "CALLS"
	EdgeReferences  = "REFERENCES"
	EdgeExtends     = "EXTENDS"
	EdgeImplements  = "IMPLEMENTS"
	EdgeAnnotatedBy = "ANNOTATED_BY"
)

// Store é uma conexão aberta com o índice.
type Store struct {
	db   *sql.DB
	path string
}

// Open abre (ou cria) o banco e aplica as migrações pendentes.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?" + url.Values{
		// WAL + synchronous=NORMAL: escrita rápida sem perder durabilidade
		// entre transações. foreign_keys liga o ON DELETE CASCADE.
		"_pragma": []string{
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"foreign_keys(1)",
			"busy_timeout(5000)",
		},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// O pool pode abrir várias conexões: em WAL, leituras convivem com um
	// escritor, e o único escritor é garantido pelo desenho do indexer, não
	// pelo pool. Limitar a uma conexão travaria qualquer leitura feita com
	// uma transação aberta.
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Path é o arquivo do banco.
func (s *Store) Path() string {
	return s.path
}

// ReplaceWith troca todo o conteúdo do banco pelo do banco em path, numa única
// escrita (API de backup do SQLite). Quem lê pelo WAL continua vendo o índice
// antigo até o fim da cópia e vê o novo inteiro depois, sem reabrir: um
// servidor MCP com o banco aberto nunca vê um índice vazio ou pela metade. Os
// dois bancos são criados por Open, com o mesmo schema e tamanho de página.
func (s *Store) ReplaceWith(path string) error {
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting a connection to replace the index: %w", err)
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(driverConn any) error {
		restorer, ok := driverConn.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite driver cannot restore backups")
		}
		backup, err := restorer.NewRestore(path)
		if err != nil {
			return err
		}
		for more := true; more; {
			if more, err = backup.Step(-1); err != nil {
				_ = backup.Finish()
				return err
			}
		}
		return backup.Finish()
	})
	if err != nil {
		return fmt.Errorf("replacing index with %s: %w", path, err)
	}
	return nil
}

// RemoveFiles apaga o banco em path com os arquivos -wal e -shm dele.
func RemoveFiles(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", p, err)
		}
	}
	return nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}
	current, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	for i := current; i < len(migrations); i++ {
		if err := s.applyMigration(i+1, migrations[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(version int, ddl string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning migration %d: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("applying migration %d: %w", version, err)
	}
	if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
		return fmt.Errorf("resetting schema_version: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("recording migration %d: %w", version, err)
	}
	return tx.Commit()
}

// SchemaVersion devolve a versão aplicada (0 = banco vazio).
func (s *Store) SchemaVersion() (int, error) {
	var version int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("reading schema_version: %w", err)
	}
	return version, nil
}

// File é uma linha de files.
type File struct {
	ID         int64
	Path       string
	Hash       string
	Size       int64
	ModTime    int64
	Lang       lang.Lang
	Package    string
	ParseError bool
	IndexedAt  int64
}

// Kinds de palavra no índice lexical.
const (
	WordIdent   = "ident"   // identificador no código
	WordString  = "string"  // palavra dentro de um literal de string
	WordComment = "comment" // palavra de comentário
	WordText    = "text"    // palavra de arquivo sem código
	WordPart    = "part"    // sub-token de identificador composto, em minúsculas
)

// Word é uma linha de words.
type Word struct {
	Text string
	Line int
	Kind string
}

// Symbol é uma linha de symbols.
type Symbol struct {
	ID            int64
	FileID        int64
	Name          string
	QualifiedName string
	Kind          string
	Container     string
	Exported      bool
	ExportName    string
	JSX           bool
	StartLine     int
	EndLine       int
	StartByte     int
	EndByte       int
	Signature     string
	NameLine      int
	Annotations   []string
	ReturnHint    string // tipo de retorno inferido (TS sem anotação) ou "call:f"
}

// Ref é uma linha de refs. ContainerIndex é usado só na escrita: índice do
// símbolo (no mesmo arquivo) que envolve a referência, ou -1.
type Ref struct {
	ID               int64
	FileID           int64
	Name             string
	Kind             string
	Line             int
	Col              int
	Receiver         string
	ReceiverType     string
	ContainerIndex   int
	ContainerSymbol  *int64
	ResolvedSymbolID *int64
	Resolution       string
	Arity            int      // argumentos da chamada, ou -1
	ArgTypes         []string // tipo de cada argumento ("" = desconhecido); nil sem aridade
	ReceiverPath     []string // membros entre o receiver e o nome numa cadeia (`a.b().c()` -> [b])
}

// Import é uma linha de imports.
type Import struct {
	ID               int64
	FileID           int64
	Kind             string
	Module           string
	ImportedName     string
	LocalName        string
	IsReexport       bool
	IsWildcard       bool
	Line             int
	ResolvedFileID   *int64
	ResolvedSymbolID *int64
	Resolution       string
}

// Edge é uma linha de edges.
type Edge struct {
	From int64
	To   int64
	Kind string
}

// Tx é uma transação de escrita. Cada SQL é preparado uma vez por transação:
// a resolução grava uma linha por ref, e preparar a cada chamada custava mais
// que executar.
type Tx struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func (s *Store) Begin() (*Tx, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	return &Tx{tx: tx, stmts: map[string]*sql.Stmt{}}, nil
}

// exec roda query com o statement preparado nesta transação; o database/sql
// fecha os statements no commit ou no rollback.
func (t *Tx) exec(query string, args ...any) (sql.Result, error) {
	stmt, ok := t.stmts[query]
	if !ok {
		var err error
		if stmt, err = t.tx.Prepare(query); err != nil {
			return nil, err
		}
		t.stmts[query] = stmt
	}
	return stmt.Exec(args...)
}

func (t *Tx) Commit() error {
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}

// Rollback é seguro de chamar depois de Commit (vira no-op).
func (t *Tx) Rollback() {
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		_ = err
	}
}

// MigrationCount é a versão de schema que este binário espera; Open migra
// bancos mais antigos ao abrir.
func MigrationCount() int {
	return len(migrations)
}
