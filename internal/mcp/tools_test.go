package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

// indexedFixture copia o node-backend para um diretório temporário e o
// indexa, devolvendo as deps prontas para um servidor.
func indexedFixture(t *testing.T) *deps {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "fixtures", "node-backend")
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(root, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}))
	require.NoError(t, os.MkdirAll(repo.DataDir(root), 0o755))
	st, err := store.Open(repo.DBPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	cfg := repo.DefaultConfig()
	_, err = indexer.New(root, cfg, st).Run(context.Background(), indexer.Options{})
	require.NoError(t, err)
	return newDeps(root, cfg, st)
}

func connect(t *testing.T, d *deps, opts Options) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	_, err := NewServer(d, opts).Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callText(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)
	require.Len(t, res.Content, 1)
	return res.Content[0].(*sdk.TextContent).Text
}

func schemaBytes(t *testing.T, cs *sdk.ClientSession) int {
	t.Helper()
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	data, err := json.Marshal(tools)
	require.NoError(t, err)
	return len(data)
}

func TestSingleToolModeAnswersEveryAction(t *testing.T) {
	d := indexedFixture(t)
	multi := connect(t, d, Options{})
	single := connect(t, indexedFixture(t), Options{SingleTool: true})

	tools, err := single.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	assert.Equal(t, "explore", tools.Tools[0].Name)

	multiBytes, singleBytes := schemaBytes(t, multi), schemaBytes(t, single)
	t.Logf("tools/list schema: every tool %d B, single tool %d B", multiBytes, singleBytes)
	assert.Less(t, singleBytes, multiBytes/2, "single-tool mode must cut the per-turn schema at least in half")

	viaTool := callText(t, multi, "resolve_symbol", map[string]any{"name": "calculateDiscount"})
	viaExplore := callText(t, single, "explore", map[string]any{"action": "resolve", "name": "calculateDiscount"})
	assert.Equal(t, viaTool, viaExplore, "same text either way")

	for action, args := range map[string]map[string]any{
		"files":   {"dirs_only": true},
		"refs":    {"name": "roundMoney"},
		"search":  {"name": "coupon"},
		"symbols": {"file": "src/pricing/discount.ts"},
		"snippet": {"file": "src/utils/money.ts", "start_line": 1, "end_line": 1},
		"status":  {},
	} {
		args["action"] = action
		assert.NotEmpty(t, callText(t, single, "explore", args), action)
	}
	res, err := single.CallTool(context.Background(), &sdk.CallToolParams{Name: "explore", Arguments: map[string]any{"action": "dance"}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "unknown action is a tool error")
}

func TestSessionDedup(t *testing.T) {
	cs := connect(t, indexedFixture(t), Options{})
	args := map[string]any{"file": "src/utils/money.ts", "start_line": 1, "end_line": 2}
	first := callText(t, cs, "get_snippet", args)
	assert.True(t, strings.HasPrefix(first, "src/utils/money.ts:1-2"))

	again := callText(t, cs, "get_snippet", args)
	assert.True(t, strings.HasPrefix(again, "same as call #1 (snippet "), again)
	assert.Contains(t, again, "fresh=true")
	assert.Less(t, len(again), len(first)+80, "the note must be short")

	budgeted := callText(t, cs, "get_snippet", map[string]any{"file": "src/utils/money.ts", "start_line": 1, "end_line": 2, "max_tokens": 50})
	assert.True(t, strings.HasPrefix(budgeted, "same as call #1"), "max_tokens does not change the key")

	fresh := callText(t, cs, "get_snippet", map[string]any{"file": "src/utils/money.ts", "start_line": 1, "end_line": 2, "fresh": true})
	assert.Equal(t, first, fresh)

	callText(t, cs, "reindex", map[string]any{})
	afterReindex := callText(t, cs, "get_snippet", args)
	assert.Equal(t, first, afterReindex, "reindex clears the session memory")

	other := callText(t, cs, "get_snippet", map[string]any{"file": "src/utils/money.ts", "start_line": 1, "end_line": 3})
	assert.False(t, strings.HasPrefix(other, "same as"), "a different range is a different call")
}

func TestAutoIndexBeforeQueries(t *testing.T) {
	d := indexedFixture(t)
	cs := connect(t, d, Options{})
	before := callText(t, cs, "resolve_symbol", map[string]any{"name": "roundMoney"})
	assert.Contains(t, before, "roundMoney function src/utils/money.ts:1-3")

	path := filepath.Join(d.root, "src", "utils", "money.ts")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("// moved\n"+string(data)), 0o644))
	require.NoError(t, os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	after := callText(t, cs, "resolve_symbol", map[string]any{"name": "roundMoney"})
	assert.Contains(t, after, "roundMoney function src/utils/money.ts:2-4", "the edit is visible without reindex")
	assert.False(t, strings.HasPrefix(after, "same as call"), "a changed index drops the dedup memory")

	stale := connect(t, indexedFixture(t), Options{NoAutoIndex: true})
	snippetBefore := callText(t, stale, "get_snippet", map[string]any{"file": "src/utils/money.ts", "start_line": 1, "end_line": 1})
	assert.NotContains(t, snippetBefore, "warning")
}

func TestEditToolsAndWatcher(t *testing.T) {
	d := indexedFixture(t)
	cs := connect(t, d, Options{})
	require.NotNil(t, d.watcher, "the watcher starts with auto-index on")

	out := callText(t, cs, "replace_symbol_body", map[string]any{
		"file": "src/pricing/discount.ts", "symbol": "calculateDiscount",
		"text": "export function calculateDiscount(total: number, coupon?: string): number {\n  const rounded = roundMoney(applyCoupon(total, coupon));\n  return rounded;\n}",
	})
	assert.Equal(t, "replace calculateDiscount: src/pricing/discount.ts:12-15 (4 lines); index refreshed\n"+
		"  verified: 2 of 2 references still resolve\n", out, "callers are re-checked in the same call")

	resolved := callText(t, cs, "resolve_symbol", map[string]any{"name": "calculateDiscount"})
	assert.Contains(t, resolved, "calculateDiscount function src/pricing/discount.ts:12-15 [exported]")
	assert.Contains(t, resolved, "13|   const rounded = roundMoney(applyCoupon(total, coupon));")

	after := callText(t, cs, "insert_after_symbol", map[string]any{
		"file": "src/pricing/discount.ts", "symbol": "calculateDiscount", "text": "\nexport const VERSION = 2;",
	})
	assert.True(t, strings.HasPrefix(after, "insert-after calculateDiscount: src/pricing/discount.ts:16-17"), after)
	assert.Contains(t, callText(t, cs, "resolve_symbol", map[string]any{"name": "VERSION"}), "VERSION variable src/pricing/discount.ts:17-17")

	// delete recusa enquanto há uso e diz onde, sem tocar no arquivo.
	refused, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "delete_symbol",
		Arguments: map[string]any{"file": "src/pricing/discount.ts", "symbol": "applyCoupon"}})
	require.NoError(t, err)
	require.True(t, refused.IsError)
	assert.Contains(t, refused.Content[0].(*sdk.TextContent).Text, "applyCoupon is still referenced 1 time(s): src/pricing/discount.ts:13")

	preview := callText(t, cs, "rename_symbol", map[string]any{"file": "src/pricing/discount.ts", "symbol": "applyCoupon", "new_name": "withCoupon", "preview": true})
	assert.True(t, strings.HasPrefix(preview, "rename applyCoupon -> withCoupon: 2 lines in 1 files (preview: nothing written)\nsrc/pricing/discount.ts\n"), preview)
	renamed := callText(t, cs, "rename_symbol", map[string]any{"file": "src/pricing/discount.ts", "symbol": "applyCoupon", "new_name": "withCoupon"})
	assert.Equal(t, "rename applyCoupon -> withCoupon: 2 lines in 1 files; index refreshed\n"+
		"  src/pricing/discount.ts: 5, 13\n  verified: 1 of 1 references still resolve\n", renamed)
	assert.Contains(t, callText(t, cs, "resolve_symbol", map[string]any{"name": "withCoupon"}), "withCoupon function src/pricing/discount.ts:5-10")

	patched := callText(t, cs, "replace_in_symbol", map[string]any{"file": "src/pricing/discount.ts", "symbol": "calculateDiscount",
		"old_text": "return rounded;", "new_text": "return rounded + 0;"})
	assert.Equal(t, "replace-in calculateDiscount: src/pricing/discount.ts:14-14 (1 lines); index refreshed\n"+
		"  verified: 2 of 2 references still resolve\n", patched, "only the snippet travels, not the whole definition")

	// Edição externa: o watcher marca dirty e a consulta seguinte reindexa.
	path := filepath.Join(d.root, "src", "utils", "money.ts")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("// moved\n"+string(data)), 0o644))
	time.Sleep(200 * time.Millisecond)
	assert.Contains(t, callText(t, cs, "resolve_symbol", map[string]any{"name": "roundMoney"}), "src/utils/money.ts:2-4")

	single := connect(t, indexedFixture(t), Options{SingleTool: true})
	edited := callText(t, single, "explore", map[string]any{"action": "insert_before", "file": "src/utils/money.ts", "symbol": "roundMoney", "text": "// rounds to cents"})
	assert.True(t, strings.HasPrefix(edited, "insert-before roundMoney: src/utils/money.ts:1-1"), edited)
	previewed := callText(t, single, "explore", map[string]any{"action": "rename", "file": "src/utils/money.ts", "symbol": "roundMoney", "new_name": "toCents", "preview": true})
	assert.Contains(t, previewed, "rename roundMoney -> toCents: ")
	assert.Contains(t, previewed, "(preview: nothing written)")
}
