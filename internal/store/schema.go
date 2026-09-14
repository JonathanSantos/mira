package store

// migrations são aplicadas em ordem; schema_version guarda a última aplicada.
// Para mudar o schema, acrescente uma nova entrada (nunca edite as antigas).
var migrations = []string{
	`
CREATE TABLE files (
	id          INTEGER PRIMARY KEY,
	path        TEXT    NOT NULL UNIQUE,
	hash        TEXT    NOT NULL,
	size        INTEGER NOT NULL,
	mtime       INTEGER NOT NULL,
	lang        TEXT    NOT NULL,
	package     TEXT    NOT NULL DEFAULT '',
	parse_error INTEGER NOT NULL DEFAULT 0,
	indexed_at  INTEGER NOT NULL
);

CREATE TABLE words (
	word    TEXT    NOT NULL,
	file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
	line    INTEGER NOT NULL
);
CREATE INDEX idx_words_word ON words(word);
CREATE INDEX idx_words_file ON words(file_id);

CREATE TABLE symbols (
	id             INTEGER PRIMARY KEY,
	file_id        INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
	name           TEXT    NOT NULL,
	qualified_name TEXT    NOT NULL,
	kind           TEXT    NOT NULL,
	container      TEXT    NOT NULL DEFAULT '',
	exported       INTEGER NOT NULL DEFAULT 0,
	export_name    TEXT    NOT NULL DEFAULT '',
	jsx            INTEGER NOT NULL DEFAULT 0,
	start_line     INTEGER NOT NULL,
	end_line       INTEGER NOT NULL,
	start_byte     INTEGER NOT NULL,
	end_byte       INTEGER NOT NULL,
	signature      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_qualified ON symbols(qualified_name);
CREATE INDEX idx_symbols_file ON symbols(file_id);

CREATE TABLE refs (
	id                  INTEGER PRIMARY KEY,
	file_id             INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
	name                TEXT    NOT NULL,
	kind                TEXT    NOT NULL,
	line                INTEGER NOT NULL,
	col                 INTEGER NOT NULL,
	receiver            TEXT    NOT NULL DEFAULT '',
	receiver_type       TEXT    NOT NULL DEFAULT '',
	container_symbol_id INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
	resolved_symbol_id  INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
	resolution          TEXT    NOT NULL DEFAULT 'unresolved'
);
CREATE INDEX idx_refs_name ON refs(name);
CREATE INDEX idx_refs_file ON refs(file_id);
CREATE INDEX idx_refs_resolved ON refs(resolved_symbol_id);

CREATE TABLE imports (
	id                 INTEGER PRIMARY KEY,
	file_id            INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
	kind               TEXT    NOT NULL,
	module             TEXT    NOT NULL,
	imported_name      TEXT    NOT NULL,
	local_name         TEXT    NOT NULL,
	is_reexport        INTEGER NOT NULL DEFAULT 0,
	is_wildcard        INTEGER NOT NULL DEFAULT 0,
	line               INTEGER NOT NULL DEFAULT 0,
	resolved_file_id   INTEGER REFERENCES files(id) ON DELETE SET NULL,
	resolved_symbol_id INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
	resolution         TEXT    NOT NULL DEFAULT 'unresolved'
);
CREATE INDEX idx_imports_file ON imports(file_id);
CREATE INDEX idx_imports_resolved_file ON imports(resolved_file_id);

CREATE TABLE edges (
	from_symbol_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
	to_symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
	kind           TEXT    NOT NULL,
	PRIMARY KEY (from_symbol_id, to_symbol_id, kind)
);
CREATE INDEX idx_edges_to ON edges(to_symbol_id);
`,
	// v2: palavras vindas de literais de string (kind), anotações e linha do
	// nome nos símbolos. Apaga os arquivos para forçar reindexação completa,
	// já que a extração mudou.
	`
ALTER TABLE words ADD COLUMN kind TEXT NOT NULL DEFAULT 'ident';
ALTER TABLE symbols ADD COLUMN annotations TEXT NOT NULL DEFAULT '';
ALTER TABLE symbols ADD COLUMN name_line INTEGER NOT NULL DEFAULT 0;
DELETE FROM files;
`,
	// v3: número de argumentos das chamadas, para escolher entre sobrecargas.
	// Reindexa tudo, porque a extração passou a preencher a coluna.
	`
ALTER TABLE refs ADD COLUMN arity INTEGER NOT NULL DEFAULT -1;
ALTER TABLE refs ADD COLUMN arg_types TEXT NOT NULL DEFAULT '';
DELETE FROM files;
`,
	// v4: caminho de membros entre o receiver e o nome (`a.b().c()`), para
	// resolver chamadas encadeadas pelo tipo de retorno.
	`
ALTER TABLE refs ADD COLUMN receiver_path TEXT NOT NULL DEFAULT '';
DELETE FROM files;
`,
	// v5: tipo de retorno inferido de funções TS sem anotação.
	`
ALTER TABLE symbols ADD COLUMN return_hint TEXT NOT NULL DEFAULT '';
DELETE FROM files;
`,
	// v6: índices nas colunas que o SQLite procura ao apagar um símbolo
	// (ON DELETE SET NULL). Sem eles, cada símbolo apagado varre refs e
	// imports inteiros: minutos para reindexar um repositório grande. Não
	// muda dados, então não precisa reindexar.
	`
CREATE INDEX idx_refs_container ON refs(container_symbol_id);
CREATE INDEX idx_imports_resolved_symbol ON imports(resolved_symbol_id);
`,
}
