// Package mcp expõe o graph como servidor MCP (stdio). As tools chamam o
// mesmo código da CLI e devolvem o mesmo JSON de --json.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JonathanSantos/mira/internal/editor"
	"github.com/JonathanSantos/mira/internal/graph"
	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/render"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/skill"
	"github.com/JonathanSantos/mira/internal/store"
	"github.com/JonathanSantos/mira/internal/walker"
	"github.com/JonathanSantos/mira/internal/watch"
)

const serverInstructions = `mira indexes this repository by symbol (TypeScript, JavaScript, Java, Python, Go).
Start with list_files (dirs_only=true), then resolve_symbol for the name you
care about: file, line range, signature, numbered snippet, callers, callees.
include_skeleton gives returns, throws, locals and uses without the body;
include_body and include_refs give the body and every use in one call.
find_references lists uses with the calling method; search_text finds
identifiers and words inside strings. Read code only through get_snippet
(explicit lines or a symbol), never whole files; cite the numbered lines as
file:line. Every tool takes max_tokens (default 4000). The index refreshes
itself before every query, so edits are visible at once. The full guide with recipes is the resource ` + SkillURI + `
(also the prompt "guide"); install it in the workspace with ` + "`mira skill --install`" + `.`

// SkillURI é o endereço da skill como resource MCP.
const SkillURI = "mira://skill"

// defaultMaxTokens é o orçamento por resposta quando o cliente não pede
// outro: cabe uma investigação de vários passos sem estourar o contexto.
const defaultMaxTokens = 4000

// Options controla como o servidor expõe as capacidades.
type Options struct {
	// SingleTool expõe uma tool só (`explore`, com `action`) em vez de oito:
	// menos schema por turno, ao custo de descrições menos específicas.
	SingleTool bool
	// NoAutoIndex desliga a indexação incremental antes de cada consulta.
	NoAutoIndex bool
}

// deps são as dependências compartilhadas pelas tools e a memória da
// sessão: uma resposta idêntica já enviada não é repetida (dedup), como o
// Probe faz com blocos de código.
type deps struct {
	root      string
	cfg       repo.Config
	store     *store.Store
	autoIndex bool
	watcher   *watch.Watcher // nil = sem watcher: caminha o repositório a cada consulta
	mu        sync.Mutex
	calls     int
	seen      map[string]int // chave da chamada -> número da chamada que respondeu
}

func newDeps(root string, cfg repo.Config, st *store.Store) *deps {
	return &deps{root: root, cfg: cfg, store: st, autoIndex: true, seen: map[string]int{}}
}

// syncTimeout limita a espera pelo cookie do watcher (ver watch.Sync). Se
// estourar, a consulta caminha o repositório como se não houvesse watcher.
const syncTimeout = time.Second

// refresh atualiza o índice antes de uma consulta; se algo mudou, a
// memória de dedup é descartada, porque as respostas antigas envelheceram.
// Um erro aqui não derruba a consulta: o índice velho ainda responde.
func (d *deps) refresh(ctx context.Context) {
	if !d.autoIndex || d.store == nil {
		return
	}
	// Sync antes do TakeDirty: uma gravação feita logo antes da consulta pode
	// ainda não ter virado evento.
	if d.watcher != nil && d.watcher.Sync(syncTimeout) && !d.watcher.TakeDirty() {
		return // nada mudou desde a última consulta: nem a caminhada é feita
	}
	d.refreshNow(ctx)
}

// refreshNow atualiza o índice sem consultar o watcher (depois de uma
// edição feita por nós, por exemplo).
func (d *deps) refreshNow(ctx context.Context) {
	if d.store == nil {
		return
	}
	_, changed, err := indexer.New(d.root, d.cfg, d.store).Refresh(ctx)
	if err == nil && changed {
		d.forget()
	}
}

// startWatcher liga o fsnotify quando a indexação automática está ativa;
// se não der (repositório enorme, limite de descritores), segue sem ele.
func (d *deps) startWatcher() {
	if !d.autoIndex || d.root == "" || d.store == nil || d.watcher != nil {
		return
	}
	if w, err := watch.New(d.root, filepath.Dir(d.store.Path()), walker.Ignored); err == nil {
		d.watcher = w
	}
}

// close solta o watcher. Ele segura handles dos diretórios observados, e no
// Windows um diretório com handle aberto não pode ser apagado.
func (d *deps) close() {
	if d.watcher != nil {
		_ = d.watcher.Close()
	}
}

// graph cria um Graph por chamada: o cache de linhas dele não deve
// sobreviver entre requisições de um servidor de longa duração.
func (d *deps) graph() *graph.Graph {
	return graph.New(d.root, d.store)
}

// remember registra a chamada e diz se ela repete uma já respondida nesta
// sessão. Chaves ignoram max_tokens e fresh.
func (d *deps) remember(tool string, args any, fresh bool) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	key := tool + ":" + canonical(args)
	if n, ok := d.seen[key]; ok && !fresh {
		return fmt.Sprintf("same as call #%d (%s %s): output not repeated; pass fresh=true to resend it", n, tool, key[len(tool)+1:]), true
	}
	d.seen[key] = d.calls
	return "", false
}

// forget limpa a memória da sessão (depois de um reindex o conteúdo mudou).
func (d *deps) forget() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = map[string]int{}
}

// canonical serializa os argumentos sem os campos que não mudam a resposta.
func canonical(args any) string {
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprint(args)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return string(data)
	}
	delete(m, "max_tokens")
	delete(m, "fresh")
	out, _ := json.Marshal(m)
	return string(out)
}

// Serve bloqueia atendendo o cliente MCP em stdin/stdout até o contexto ser
// cancelado ou o cliente encerrar.
func Serve(ctx context.Context, root string, cfg repo.Config, st *store.Store, opts Options) error {
	d := newDeps(root, cfg, st)
	defer d.close()
	server := NewServer(d, opts)
	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// NewServer monta o servidor com as tools registradas (oito, ou só
// `explore` com SingleTool), mais a skill como resource e prompt.
func NewServer(d *deps, opts Options) *sdk.Server {
	d.autoIndex = !opts.NoAutoIndex
	d.startWatcher()
	server := sdk.NewServer(&sdk.Implementation{Name: "mira", Version: Version},
		&sdk.ServerOptions{Instructions: serverInstructions})
	if opts.SingleTool {
		registerExplore(server, d)
	} else {
		registerTools(server, d)
		registerEditTools(server, d)
	}
	registerSkill(server)
	return server
}

// editInput é o que replace e insert recebem; delete e rename têm tipos
// próprios porque um campo obrigatório vira obrigatório no schema.
type editInput struct {
	File    string `json:"file" jsonschema:"path relative to the repository root"`
	Symbol  string `json:"symbol" jsonschema:"symbol name or Container.member inside that file; name:line picks one of several overloads"`
	Text    string `json:"text" jsonschema:"the new text: for replace, the whole definition (signature and body, annotations included); for insert, the lines to add"`
	Preview bool   `json:"preview,omitempty" jsonschema:"show the diff without writing anything"`
}

type deleteInput struct {
	File    string `json:"file" jsonschema:"path relative to the repository root"`
	Symbol  string `json:"symbol" jsonschema:"symbol name or Container.member inside that file; name:line picks one of several overloads"`
	Force   bool   `json:"force,omitempty" jsonschema:"delete even if other code still references it (those sites are listed)"`
	Preview bool   `json:"preview,omitempty" jsonschema:"show the diff and who still references it, without writing anything"`
}

type replaceInInput struct {
	File    string `json:"file" jsonschema:"path relative to the repository root"`
	Symbol  string `json:"symbol" jsonschema:"symbol name or Container.member inside that file; name:line picks one of several overloads"`
	OldText string `json:"old_text" jsonschema:"exact text to find inside that symbol, indentation included"`
	NewText string `json:"new_text" jsonschema:"text that replaces it (empty deletes it)"`
	All     bool   `json:"all,omitempty" jsonschema:"replace every occurrence inside the symbol instead of requiring exactly one"`
	Preview bool   `json:"preview,omitempty" jsonschema:"show the diff without writing anything"`
}

type renameInput struct {
	File    string `json:"file" jsonschema:"path of the file that declares the symbol"`
	Symbol  string `json:"symbol" jsonschema:"symbol name or Container.member inside that file; name:line picks one of several overloads"`
	NewName string `json:"new_name" jsonschema:"the new identifier"`
	Preview bool   `json:"preview,omitempty" jsonschema:"show every line that would change and what would be skipped, without writing anything"`
}

// registerEditTools expõe a edição por símbolo: o agente troca ou insere
// uma definição inteira sem contar linhas, e o índice é atualizado na
// hora para a próxima consulta já ver o resultado.
func registerEditTools(server *sdk.Server, d *deps) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "replace_symbol_body",
		Description: "Replace the whole definition of a symbol (signature and body, annotations included) with text. " +
			"After writing, the index refreshes and every caller is re-checked: the answer says how many still resolve and which broke, and flags a syntax error.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in editInput) (*sdk.CallToolResult, any, error) {
		return d.edit(ctx, editor.Request{Action: editor.Replace, File: in.File, Symbol: in.Symbol, Text: in.Text, Preview: in.Preview})
	})
	sdk.AddTool(server, &sdk.Tool{
		Name: "replace_in_symbol",
		Description: "Replace an exact piece of text inside one symbol without resending the whole definition: old_text must appear once " +
			"in that symbol (or pass all). Cheaper than replace_symbol_body for a small change in a long function; callers are re-checked after writing.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in replaceInInput) (*sdk.CallToolResult, any, error) {
		return d.edit(ctx, editor.Request{Action: editor.ReplaceIn, File: in.File, Symbol: in.Symbol, Old: in.OldText, Text: in.NewText, All: in.All, Preview: in.Preview})
	})
	sdk.AddTool(server, &sdk.Tool{
		Name:        "insert_before_symbol",
		Description: "Insert text immediately before a symbol's definition (before its annotations). The index refreshes right after.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in editInput) (*sdk.CallToolResult, any, error) {
		return d.edit(ctx, editor.Request{Action: editor.InsertBefore, File: in.File, Symbol: in.Symbol, Text: in.Text, Preview: in.Preview})
	})
	sdk.AddTool(server, &sdk.Tool{
		Name:        "insert_after_symbol",
		Description: "Insert text immediately after a symbol's definition. The index refreshes right after.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in editInput) (*sdk.CallToolResult, any, error) {
		return d.edit(ctx, editor.Request{Action: editor.InsertAfter, File: in.File, Symbol: in.Symbol, Text: in.Text, Preview: in.Preview})
	})
	sdk.AddTool(server, &sdk.Tool{
		Name: "delete_symbol",
		Description: "Delete a symbol's definition together with its doc comment. Refuses while other code still references it " +
			"(or one of its members) and lists where; fix those or pass force.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in deleteInput) (*sdk.CallToolResult, any, error) {
		return d.edit(ctx, editor.Request{Action: editor.Delete, File: in.File, Symbol: in.Symbol, Force: in.Force, Preview: in.Preview})
	})
	sdk.AddTool(server, &sdk.Tool{
		Name: "rename_symbol",
		Description: "Rename a symbol at its declaration and at every reference resolved to it, across all files, in one call. " +
			"Same-named code that resolves to something else is left alone; unresolved uses that may be it are listed, not guessed.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in renameInput) (*sdk.CallToolResult, any, error) {
		return d.edit(ctx, editor.Request{Action: editor.Rename, File: in.File, Symbol: in.Symbol, Text: in.NewName, Preview: in.Preview})
	})
}

// edit aplica a edição, reindexa e verifica: a resposta traz o que mudou,
// o que ficou de fora e o que quebrou, sem o agente precisar consultar.
func (d *deps) edit(ctx context.Context, req editor.Request) (*sdk.CallToolResult, any, error) {
	d.refresh(ctx) // o editor recusa arquivo desatualizado: garante que não está
	res, err := editor.Apply(d.root, d.store, req)
	if err != nil {
		return nil, nil, err
	}
	if !req.Preview {
		d.refreshNow(ctx)
		d.forget()
		if err := editor.Verify(d.store, &res); err != nil {
			return nil, nil, fmt.Errorf("verifying edit: %w", err)
		}
	}
	return text(render.Edit(res), 0, nil)
}

// Version é gravada no binário pelo build (ldflags); "dev" fora de release.
var Version = "dev"

// registerSkill expõe a skill como resource (para quem lê recursos) e como
// prompt (no Claude Code vira um comando de barra). Nada de tool: o schema
// de uma tool é pago em todo turno, e instalar a skill é ação única.
func registerSkill(server *sdk.Server) {
	server.AddResource(&sdk.Resource{
		URI:         SkillURI,
		Name:        "mira-skill",
		Title:       "How to use mira",
		Description: "Agent skill: which tool answers which question, recipes for impact analysis and tracing, how to read the output.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{
			{URI: SkillURI, MIMEType: "text/markdown", Text: skill.Content()},
		}}, nil
	})
	server.AddPrompt(&sdk.Prompt{
		Name:        "guide",
		Title:       "mira guide",
		Description: "Load the mira skill into the conversation: tools, recipes and output format.",
	}, func(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
		return &sdk.GetPromptResult{
			Description: "mira usage guide",
			Messages:    []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: skill.Content()}}},
		}, nil
	})
}

type resolveInput struct {
	Name            string `json:"name" jsonschema:"symbol name, Container.member, fully qualified Java name, or file:line; several names separated by commas or spaces are answered in one call"`
	Depth           int    `json:"depth,omitempty" jsonschema:"levels of callers/callees to expand (default 1)"`
	IncludeBody     bool   `json:"include_body,omitempty" jsonschema:"include the full numbered body of each definition"`
	IncludeSkeleton bool   `json:"include_skeleton,omitempty" jsonschema:"structure instead of the body: returns, throws, locals, nested functions and what it uses grouped by origin (same file, other files, external); cheaper than include_body for long functions"`
	Around          int    `json:"around,omitempty" jsonschema:"show only a window of lines around this line of the definition (a return or throw the skeleton pointed to) instead of the body"`
	Context         int    `json:"context,omitempty" jsonschema:"lines before and after around (default 8)"`
	IncludeRefs     bool   `json:"include_refs,omitempty" jsonschema:"also list references resolved to each definition, grouped by file (saves a find_references call)"`
	Kind            string `json:"kind,omitempty" jsonschema:"only definitions of this kind: class, function, method, type, interface, enum, field, property"`
	Path            string `json:"path,omitempty" jsonschema:"only definitions under this prefix or matching this glob, e.g. src/types"`
	ExcludeTests    bool   `json:"exclude_tests,omitempty" jsonschema:"skip definitions, callers and refs in test files"`
	MaxTokens       int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
	Fresh           bool   `json:"fresh,omitempty" jsonschema:"resend even if this exact call was already answered in this session"`
}

type nameInput struct {
	Name         string `json:"name" jsonschema:"symbol name to look up, or Container.member for only the uses resolved to that definition; several names separated by commas or spaces are answered in one call"`
	Path         string `json:"path,omitempty" jsonschema:"only files under this prefix or matching this glob"`
	ExcludeTests bool   `json:"exclude_tests,omitempty" jsonschema:"skip test files and directories"`
	Kind         string `json:"kind,omitempty" jsonschema:"only references of this kind: call, method, property, type, new, jsx, annotation, import"`
	Limit        int    `json:"limit,omitempty" jsonschema:"maximum references listed (default 200)"`
	MaxTokens    int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
	Fresh        bool   `json:"fresh,omitempty" jsonschema:"resend even if this exact call was already answered in this session"`
}

type wordInput struct {
	Word         string `json:"word" jsonschema:"identifier or word to search for; a Go regexp when regex is true"`
	Path         string `json:"path,omitempty" jsonschema:"only files under this prefix or matching this glob, e.g. src/logic or src/**/*.ts"`
	ExcludeTests bool   `json:"exclude_tests,omitempty" jsonschema:"skip test files and directories"`
	Regex        bool   `json:"regex,omitempty" jsonschema:"treat word as a regexp over line text (grep); default matches whole identifiers"`
	Limit        int    `json:"limit,omitempty" jsonschema:"maximum hits in total (default 100)"`
	PerFile      int    `json:"per_file,omitempty" jsonschema:"maximum hits per file (default 20)"`
	Context      int    `json:"context,omitempty" jsonschema:"lines of context before and after each hit (default 0); every hit already names the enclosing symbol"`
	MaxTokens    int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
	Fresh        bool   `json:"fresh,omitempty" jsonschema:"resend even if this exact call was already answered in this session"`
}

type fileInput struct {
	File      string `json:"file" jsonschema:"path relative to the repository root"`
	Compact   bool   `json:"compact,omitempty" jsonschema:"only name, kind and line range per symbol (no signatures); use for large files"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
	Fresh     bool   `json:"fresh,omitempty" jsonschema:"resend even if this exact call was already answered in this session"`
}

type filesInput struct {
	Path         string `json:"path,omitempty" jsonschema:"directory prefix to list (default: repository root)"`
	Depth        int    `json:"depth,omitempty" jsonschema:"directory levels to expand (default 1, -1 for all)"`
	ExcludeTests bool   `json:"exclude_tests,omitempty" jsonschema:"skip test files and directories"`
	DirsOnly     bool   `json:"dirs_only,omitempty" jsonschema:"list only directories with totals; the cheapest first look at a repository"`
	MaxTokens    int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
}

type snippetInput struct {
	File      string `json:"file" jsonschema:"path relative to the repository root"`
	StartLine int    `json:"start_line,omitempty" jsonschema:"first line, 1-based, inclusive (omit when using symbol)"`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"last line, 1-based, inclusive (omit when using symbol)"`
	Symbol    string `json:"symbol,omitempty" jsonschema:"return the full range of this symbol instead of explicit lines"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
	Fresh     bool   `json:"fresh,omitempty" jsonschema:"resend even if this exact call was already answered in this session"`
}

type reindexInput struct {
	Full bool `json:"full,omitempty" jsonschema:"reindex every file instead of only the changed ones"`
}

type statusInput struct{}

func registerTools(server *sdk.Server, d *deps) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "resolve_symbol",
		Description: "Definition(s) of one or more symbols: kind, file, line range, signature, annotations, numbered body (up to 30 lines) or snippet, " +
			"callers, callees and who exposes it; classes list their members. include_skeleton gives returns, throws, locals and uses without the body; " +
			"include_body and include_refs answer 'what is it, who uses it' in one call. " +
			"Accepts a name, Container.member, a qualified Java name, or file:line. Use before reading files.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in resolveInput) (*sdk.CallToolResult, any, error) {
		d.refresh(ctx)
		return d.resolve(in)
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "find_references",
		Description: "References to one or more names (calls, property accesses, type uses, imports, annotations) grouped by file " +
			"with the calling method and the source line; unresolved/ambiguous/external are marked. Narrow with path, kind, exclude_tests.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in nameInput) (*sdk.CallToolResult, any, error) {
		d.refresh(ctx)
		return d.refs(in)
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "search_text",
		Description: "Identifiers, sub-tokens (search date finds visitDate), words in strings (s), comments (c) and text files such as " +
			".properties/.yml/.html/.sql/.md (t), grouped by file, definition lines first (D), each hit with its enclosing symbol. " +
			"Filter with path (prefix or glob) and exclude_tests. regex=true greps line text.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in wordInput) (*sdk.CallToolResult, any, error) {
		d.refresh(ctx)
		return d.search(in)
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "list_files",
		Description: "Tree of indexed files with symbol counts (ls/tree for code). Use first on an unknown repository " +
			"with dirs_only=true, then narrow with path and depth.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in filesInput) (*sdk.CallToolResult, any, error) {
		d.refresh(ctx)
		return d.files(in)
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "list_symbols",
		Description: "Outline of one file: symbols with kind, line range, annotations and truncated signature, " +
			"members nested under their container. Cheaper than reading the file.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in fileInput) (*sdk.CallToolResult, any, error) {
		d.refresh(ctx)
		return d.symbols(in)
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "get_snippet",
		Description: "Exactly a line range of a file, or a symbol's full range when symbol is given; the header names the enclosing symbol. " +
			"Take ranges from resolve_symbol or list_symbols; never read whole files.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in snippetInput) (*sdk.CallToolResult, any, error) {
		d.refresh(ctx)
		return d.snippet(in)
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "index_status",
		Description: "Shows index counts per language and how many files are out of date without reindexing.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in statusInput) (*sdk.CallToolResult, any, error) {
		return d.status()
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "reindex",
		Description: "Runs an incremental index (only new, changed and removed files). Call it after editing files.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in reindexInput) (*sdk.CallToolResult, any, error) {
		return d.reindex(ctx, in)
	})
}

// exploreInput é a união dos parâmetros das oito tools, para o modo de
// tool única.
type exploreInput struct {
	Action          string `json:"action" jsonschema:"one of: files, resolve, refs, search, symbols, snippet, status, reindex, replace, replace_in, insert_before, insert_after, delete, rename"`
	Name            string `json:"name,omitempty" jsonschema:"resolve/refs: symbol name(s), Container.member, file:line, several separated by commas; search: word or regex"`
	File            string `json:"file,omitempty" jsonschema:"symbols/snippet: path relative to the repository root"`
	Path            string `json:"path,omitempty" jsonschema:"files: directory to list; resolve/refs/search: only files under this prefix or glob"`
	Depth           int    `json:"depth,omitempty" jsonschema:"files: directory levels (default 1); resolve: levels of callers/callees (default 1)"`
	IncludeBody     bool   `json:"include_body,omitempty" jsonschema:"resolve: full numbered body"`
	IncludeSkeleton bool   `json:"include_skeleton,omitempty" jsonschema:"resolve: returns, throws, locals, nested functions and uses grouped by origin"`
	IncludeRefs     bool   `json:"include_refs,omitempty" jsonschema:"resolve: every use grouped by file in the same call"`
	Kind            string `json:"kind,omitempty" jsonschema:"resolve: definition kind; refs: reference kind"`
	ExcludeTests    bool   `json:"exclude_tests,omitempty" jsonschema:"skip test files and directories"`
	Regex           bool   `json:"regex,omitempty" jsonschema:"search: treat name as a regexp over line text"`
	Limit           int    `json:"limit,omitempty" jsonschema:"refs: max references per name (default 200); search: max hits (default 100)"`
	PerFile         int    `json:"per_file,omitempty" jsonschema:"search: max hits per file (default 20)"`
	Context         int    `json:"context,omitempty" jsonschema:"search: lines of context around each hit"`
	Compact         bool   `json:"compact,omitempty" jsonschema:"symbols: only name, kind and lines"`
	Around          int    `json:"around,omitempty" jsonschema:"resolve: window around this line instead of the body"`
	DirsOnly        bool   `json:"dirs_only,omitempty" jsonschema:"files: only directories with totals"`
	StartLine       int    `json:"start_line,omitempty" jsonschema:"snippet: first line (1-based)"`
	EndLine         int    `json:"end_line,omitempty" jsonschema:"snippet: last line (1-based)"`
	Symbol          string `json:"symbol,omitempty" jsonschema:"snippet: full range of this symbol instead of lines; edits: the symbol to change"`
	Full            bool   `json:"full,omitempty" jsonschema:"reindex: every file, not only the changed ones"`
	Text            string `json:"text,omitempty" jsonschema:"replace/insert_before/insert_after: the new text"`
	NewName         string `json:"new_name,omitempty" jsonschema:"rename: the new identifier"`
	Preview         bool   `json:"preview,omitempty" jsonschema:"edits: show the diff without writing"`
	Force           bool   `json:"force,omitempty" jsonschema:"delete: delete even if still referenced"`
	OldText         string `json:"old_text,omitempty" jsonschema:"replace_in: exact text to find inside the symbol (text is the replacement)"`
	All             bool   `json:"all,omitempty" jsonschema:"replace_in: replace every occurrence inside the symbol"`
	MaxTokens       int    `json:"max_tokens,omitempty" jsonschema:"response budget in tokens (default 4000)"`
	Fresh           bool   `json:"fresh,omitempty" jsonschema:"resend even if this exact call was already answered in this session"`
}

// registerExplore expõe tudo por uma tool só: o schema é pago em todo
// turno, e oito tools custam ~5 KB.
func registerExplore(server *sdk.Server, d *deps) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "explore",
		Description: "Navigate this repository by symbol. action=files (tree with symbol counts; dirs_only first), " +
			"resolve (definition with numbered body or snippet, callers, callees, members; include_skeleton for structure, include_refs for uses), " +
			"refs (every use of a name with the calling method), search (identifiers, sub-tokens, strings, comments, text files; regex for grep), " +
			"symbols (outline of a file), snippet (exact lines or a symbol's range), status, reindex, " +
			"replace/insert_before/insert_after (file, symbol, text), replace_in (file, symbol, old_text, text: exact text inside the symbol), " +
			"delete (file, symbol; refuses while referenced), " +
			"rename (file, symbol, new_name; every resolved reference, all files); preview=true shows the diff first. " +
			"Text is compact and numbered: cite file:line. Read the resource mira://skill for recipes.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in exploreInput) (*sdk.CallToolResult, any, error) {
		if in.Action != "reindex" && in.Action != "status" {
			d.refresh(ctx)
		}
		switch in.Action {
		case "files":
			return d.files(filesInput{Path: in.Path, Depth: in.Depth, ExcludeTests: in.ExcludeTests, DirsOnly: in.DirsOnly, MaxTokens: in.MaxTokens})
		case "resolve":
			return d.resolve(resolveInput{Name: in.Name, Depth: in.Depth, IncludeBody: in.IncludeBody, IncludeSkeleton: in.IncludeSkeleton,
				IncludeRefs: in.IncludeRefs, Around: in.Around, Context: in.Context, Kind: in.Kind, Path: in.Path, ExcludeTests: in.ExcludeTests,
				MaxTokens: in.MaxTokens, Fresh: in.Fresh})
		case "refs":
			return d.refs(nameInput{Name: in.Name, Path: in.Path, ExcludeTests: in.ExcludeTests, Kind: in.Kind, Limit: in.Limit, MaxTokens: in.MaxTokens, Fresh: in.Fresh})
		case "search":
			return d.search(wordInput{Word: in.Name, Path: in.Path, ExcludeTests: in.ExcludeTests, Regex: in.Regex, Limit: in.Limit,
				PerFile: in.PerFile, Context: in.Context, MaxTokens: in.MaxTokens, Fresh: in.Fresh})
		case "symbols":
			return d.symbols(fileInput{File: in.File, Compact: in.Compact, MaxTokens: in.MaxTokens, Fresh: in.Fresh})
		case "snippet":
			return d.snippet(snippetInput{File: in.File, StartLine: in.StartLine, EndLine: in.EndLine, Symbol: in.Symbol, MaxTokens: in.MaxTokens, Fresh: in.Fresh})
		case "status":
			return d.status()
		case "reindex":
			return d.reindex(ctx, reindexInput{Full: in.Full})
		case "replace":
			return d.edit(ctx, editor.Request{Action: editor.Replace, File: in.File, Symbol: in.Symbol, Text: in.Text, Preview: in.Preview})
		case "insert_before":
			return d.edit(ctx, editor.Request{Action: editor.InsertBefore, File: in.File, Symbol: in.Symbol, Text: in.Text, Preview: in.Preview})
		case "insert_after":
			return d.edit(ctx, editor.Request{Action: editor.InsertAfter, File: in.File, Symbol: in.Symbol, Text: in.Text, Preview: in.Preview})
		case "replace_in":
			return d.edit(ctx, editor.Request{Action: editor.ReplaceIn, File: in.File, Symbol: in.Symbol, Old: in.OldText, Text: in.Text, All: in.All, Preview: in.Preview})
		case "delete":
			return d.edit(ctx, editor.Request{Action: editor.Delete, File: in.File, Symbol: in.Symbol, Force: in.Force, Preview: in.Preview})
		case "rename":
			return d.edit(ctx, editor.Request{Action: editor.Rename, File: in.File, Symbol: in.Symbol, Text: in.NewName, Preview: in.Preview})
		}
		return nil, nil, fmt.Errorf("unknown action %q (files, resolve, refs, search, symbols, snippet, status, reindex, replace, replace_in, insert_before, insert_after, delete, rename)", in.Action)
	})
}

func (d *deps) resolve(in resolveInput) (*sdk.CallToolResult, any, error) {
	if note, dup := d.remember("resolve", in, in.Fresh); dup {
		return text(note, 0, nil)
	}
	results, err := d.graph().ResolveMany(graph.SplitNames(in.Name), graph.ResolveOptions{
		Depth: in.Depth, IncludeBody: in.IncludeBody, IncludeSkeleton: in.IncludeSkeleton, IncludeRefs: in.IncludeRefs,
		Around: in.Around, Context: in.Context, Kind: in.Kind, Path: in.Path, ExcludeTests: in.ExcludeTests,
	})
	return text(render.ResolveMany(results), in.MaxTokens, err)
}

func (d *deps) refs(in nameInput) (*sdk.CallToolResult, any, error) {
	if note, dup := d.remember("refs", in, in.Fresh); dup {
		return text(note, 0, nil)
	}
	results, err := d.graph().RefsMany(graph.SplitNames(in.Name), graph.RefsOptions{
		Path: in.Path, ExcludeTests: in.ExcludeTests, Kind: in.Kind, Limit: in.Limit,
	})
	return text(render.RefsMany(results), in.MaxTokens, err)
}

func (d *deps) search(in wordInput) (*sdk.CallToolResult, any, error) {
	if note, dup := d.remember("search", in, in.Fresh); dup {
		return text(note, 0, nil)
	}
	res, err := d.graph().Search(in.Word, graph.SearchOptions{
		Path: in.Path, ExcludeTests: in.ExcludeTests, Regex: in.Regex, Limit: in.Limit, PerFile: in.PerFile, Context: in.Context,
	})
	return text(render.Search(res), in.MaxTokens, err)
}

func (d *deps) files(in filesInput) (*sdk.CallToolResult, any, error) {
	depth := in.Depth
	if depth == 0 {
		depth = 1
	}
	res, err := d.graph().Files(in.Path, depth, in.ExcludeTests, in.DirsOnly)
	return text(render.Files(res), in.MaxTokens, err)
}

func (d *deps) symbols(in fileInput) (*sdk.CallToolResult, any, error) {
	if note, dup := d.remember("symbols", in, in.Fresh); dup {
		return text(note, 0, nil)
	}
	res, err := d.graph().Symbols(in.File, in.Compact)
	return text(render.Symbols(res), in.MaxTokens, err)
}

func (d *deps) snippet(in snippetInput) (*sdk.CallToolResult, any, error) {
	if note, dup := d.remember("snippet", in, in.Fresh); dup {
		return text(note, 0, nil)
	}
	var res graph.SnippetResult
	var err error
	if in.Symbol != "" {
		res, err = d.graph().SnippetOfSymbol(in.File, in.Symbol)
	} else {
		res, err = d.graph().Snippet(in.File, in.StartLine, in.EndLine)
	}
	return text(render.Snippet(res), in.MaxTokens, err)
}

func (d *deps) status() (*sdk.CallToolResult, any, error) {
	st, err := indexer.New(d.root, d.cfg, d.store).Status()
	return text(render.Status(st), 0, err)
}

func (d *deps) reindex(ctx context.Context, in reindexInput) (*sdk.CallToolResult, any, error) {
	report, err := indexer.New(d.root, d.cfg, d.store).Run(ctx, indexer.Options{Full: in.Full})
	d.forget()
	return text(render.Report(report), 0, err)
}

// text adapta (texto, erro) ao retorno do handler: o erro vira um tool error
// visível ao modelo; o texto vai só em content, sem structuredContent, que
// duplicava cada resposta no fio. O orçamento corta o texto com uma dica.
func text(s string, maxTokens int, err error) (*sdk.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: render.Budget(s, maxTokens)}}}, nil, nil
}
