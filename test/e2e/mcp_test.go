package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/cli"
)

// mcpClient fala JSON-RPC por linhas com o subprocesso `mira mcp`.
type mcpClient struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     *bufio.Writer
	out    *bufio.Scanner
	nextID int
}

func startMCP(t *testing.T, root string) *mcpClient {
	t.Helper()
	cmd := exec.Command(binary, "--repo", root, "mcp")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	c := &mcpClient{t: t, cmd: cmd, in: bufio.NewWriter(stdin), out: scanner}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	return c
}

func (c *mcpClient) send(msg map[string]any) {
	data, err := json.Marshal(msg)
	require.NoError(c.t, err)
	_, err = c.in.Write(append(data, '\n'))
	require.NoError(c.t, err)
	require.NoError(c.t, c.in.Flush())
}

// call envia uma requisição e devolve o "result" da resposta com o mesmo id.
func (c *mcpClient) call(method string, params any) map[string]any {
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			c.t.Fatalf("timeout waiting for response to %s", method)
		default:
		}
		require.True(c.t, c.out.Scan(), "server closed stdout: %v", c.out.Err())
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result map[string]any  `json:"result"`
			Error  map[string]any  `json:"error"`
		}
		require.NoError(c.t, json.Unmarshal(c.out.Bytes(), &msg), c.out.Text())
		if string(msg.ID) != fmt.Sprint(id) {
			continue // notificação ou resposta de outro id
		}
		require.Nil(c.t, msg.Error, "rpc error for %s: %v", method, msg.Error)
		return msg.Result
	}
}

func TestMCPServer(t *testing.T) {
	root := indexed(t, "node-backend")
	c := startMCP(t, root)

	init := c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "0"},
	})
	assert.Equal(t, "mira", init["serverInfo"].(map[string]any)["name"])
	assert.Contains(t, init["instructions"], "resolve_symbol")
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	list := c.call("tools/list", map[string]any{})
	var names []string
	for _, tool := range list["tools"].([]any) {
		tl := tool.(map[string]any)
		names = append(names, tl["name"].(string))
		if tl["name"] == "resolve_symbol" {
			assert.Contains(t, tl["description"], "before reading files")
		}
	}
	assert.ElementsMatch(t, []string{
		"resolve_symbol", "find_references", "search_text", "list_files", "list_symbols", "get_snippet", "index_status", "reindex",
		"replace_symbol_body", "replace_in_symbol", "insert_before_symbol", "insert_after_symbol", "delete_symbol", "rename_symbol",
	}, names)

	result := c.call("tools/call", map[string]any{
		"name":      "resolve_symbol",
		"arguments": map[string]any{"name": "calculateDiscount", "depth": 1},
	})
	assert.NotEqual(t, true, result["isError"])
	content := result["content"].([]any)
	require.Len(t, content, 1)
	text := content[0].(map[string]any)["text"].(string)
	assert.True(t, strings.HasPrefix(text, "calculateDiscount function src/pricing/discount.ts:12-14 [exported]\n"), text)
	assert.Contains(t, text, "callers (1):\n    OrderService.total method src/orders/order.service.ts:24-27\n")
	_, hasStructured := result["structuredContent"]
	assert.False(t, hasStructured, "text only: structuredContent would duplicate every response")

	snippet := c.call("tools/call", map[string]any{
		"name":      "get_snippet",
		"arguments": map[string]any{"file": "src/utils/money.ts", "start_line": 1, "end_line": 1},
	})
	assert.Equal(t, "src/utils/money.ts:1-1 (in roundMoney:1-3)\n1| export function roundMoney(value: number): number {\n", snippet["content"].([]any)[0].(map[string]any)["text"])

	budget := c.call("tools/call", map[string]any{
		"name":      "list_symbols",
		"arguments": map[string]any{"file": "src/orders/order.service.ts", "max_tokens": 20},
	})
	assert.Contains(t, budget["content"].([]any)[0].(map[string]any)["text"], "truncated at 20 tokens")

	bad := c.call("tools/call", map[string]any{
		"name":      "list_symbols",
		"arguments": map[string]any{"file": "src/nope.ts"},
	})
	assert.Equal(t, true, bad["isError"], "tool errors are reported to the model, not as protocol errors")

	statusResult := c.call("tools/call", map[string]any{"name": "index_status", "arguments": map[string]any{}})
	assert.Contains(t, statusResult["content"].([]any)[0].(map[string]any)["text"], "files=11 symbols=")

	files := c.call("tools/call", map[string]any{"name": "list_files", "arguments": map[string]any{"path": "src/orders", "depth": 1}})
	assert.Contains(t, files["content"].([]any)[0].(map[string]any)["text"], "src/orders files=3")

	skeleton := c.call("tools/call", map[string]any{
		"name":      "resolve_symbol",
		"arguments": map[string]any{"name": "OrderController.total", "include_skeleton": true},
	})
	skText := skeleton["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, skText, "  skeleton: 7 lines, 1 branches, 0 loops\n  returns (2):\n    12 return 0;\n")
	assert.Contains(t, skText, "  uses (other files):\n    OrderService.findOne src/orders/order.service.ts:20 (10)\n")

	several := c.call("tools/call", map[string]any{
		"name":      "find_references",
		"arguments": map[string]any{"name": "roundMoney, calculateDiscount"},
	})
	severalText := several["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, severalText, "refs roundMoney: ")
	assert.Contains(t, severalText, "\n\nrefs calculateDiscount: ")

	resources := c.call("resources/list", map[string]any{})
	require.Len(t, resources["resources"].([]any), 1)
	assert.Equal(t, "mira://skill", resources["resources"].([]any)[0].(map[string]any)["uri"])
	resource := c.call("resources/read", map[string]any{"uri": "mira://skill"})
	skill := resource["contents"].([]any)[0].(map[string]any)["text"].(string)
	assert.True(t, strings.HasPrefix(skill, "---\nname: mira\n"), skill[:40])
	prompt := c.call("prompts/get", map[string]any{"name": "guide"})
	message := prompt["messages"].([]any)[0].(map[string]any)
	assert.Equal(t, skill, message["content"].(map[string]any)["text"])

	bySymbol := c.call("tools/call", map[string]any{
		"name":      "get_snippet",
		"arguments": map[string]any{"file": "src/utils/money.ts", "symbol": "roundMoney"},
	})
	assert.True(t, strings.HasPrefix(bySymbol["content"].([]any)[0].(map[string]any)["text"].(string), "src/utils/money.ts:1-3 (in roundMoney:1-3)\n"))
}

// TestMCPParamsMatchCLIFlags: todo parâmetro de tool MCP é uma flag com o
// mesmo nome em kebab-case no comando equivalente, ou um argumento
// posicional dele; a skill vale para os dois lados sem tradução.
func TestMCPParamsMatchCLIFlags(t *testing.T) {
	c := startMCP(t, indexed(t, "node-backend"))
	c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "0"},
	})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	commands := map[string]struct {
		path       []string
		positional []string
	}{
		"list_files":           {[]string{"files"}, []string{"path"}},
		"resolve_symbol":       {[]string{"resolve"}, []string{"name"}},
		"find_references":      {[]string{"refs"}, []string{"name"}},
		"search_text":          {[]string{"search"}, []string{"word"}},
		"list_symbols":         {[]string{"symbols"}, []string{"file"}},
		"get_snippet":          {[]string{"snippet"}, []string{"file", "start_line", "end_line"}},
		"index_status":         {[]string{"status"}, nil},
		"reindex":              {[]string{"index"}, nil},
		"replace_symbol_body":  {[]string{"edit", "replace"}, []string{"file", "symbol"}},
		"replace_in_symbol":    {[]string{"edit", "replace-in"}, []string{"file", "symbol"}},
		"insert_before_symbol": {[]string{"edit", "insert-before"}, []string{"file", "symbol"}},
		"insert_after_symbol":  {[]string{"edit", "insert-after"}, []string{"file", "symbol"}},
		"delete_symbol":        {[]string{"edit", "delete"}, []string{"file", "symbol"}},
		"rename_symbol":        {[]string{"edit", "rename"}, []string{"file", "symbol", "new_name"}},
	}
	app := cli.New()
	list := c.call("tools/list", map[string]any{})
	for _, tool := range list["tools"].([]any) {
		spec := tool.(map[string]any)
		name := spec["name"].(string)
		command, ok := commands[name]
		require.True(t, ok, "tool %s has no CLI command in this test", name)
		cmd, _, err := app.Find(command.path)
		require.NoError(t, err, name)
		props, _ := spec["inputSchema"].(map[string]any)["properties"].(map[string]any)
		for param := range props {
			if param == "fresh" || slices.Contains(command.positional, param) {
				continue // fresh é da memória de sessão do servidor, sem equivalente na CLI
			}
			want := strings.ReplaceAll(param, "_", "-")
			flag := cmd.Flags().Lookup(want)
			if flag == nil {
				flag = app.PersistentFlags().Lookup(want)
			}
			if assert.NotNil(t, flag, "%s.%s has no --%s in mira %s", name, param, want, strings.Join(command.path, " ")) {
				assert.Equal(t, want, flag.Name, "%s.%s must be the flag's own name, not an old alias", name, param)
			}
		}
	}
}
