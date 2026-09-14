package mcp

import (
	"context"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/skill"
)

// session conecta um cliente em memória ao servidor: sem índice, porque
// listar tools, resources e prompts não toca o banco.
func session(t *testing.T) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	_, err := NewServer(newDeps("", repo.Config{}, nil), Options{}).Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestSkillResourceAndPrompt(t *testing.T) {
	cs := session(t)
	ctx := context.Background()

	resources, err := cs.ListResources(ctx, nil)
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1)
	assert.Equal(t, SkillURI, resources.Resources[0].URI)

	read, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: SkillURI})
	require.NoError(t, err)
	require.Len(t, read.Contents, 1)
	assert.Equal(t, skill.Content(), read.Contents[0].Text)

	prompts, err := cs.ListPrompts(ctx, nil)
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 1)
	assert.Equal(t, "guide", prompts.Prompts[0].Name)

	prompt, err := cs.GetPrompt(ctx, &sdk.GetPromptParams{Name: "guide"})
	require.NoError(t, err)
	require.Len(t, prompt.Messages, 1)
	assert.Equal(t, skill.Content(), prompt.Messages[0].Content.(*sdk.TextContent).Text)
}

// snakeTokens são os nomes com underscore entre crases na skill: tools e
// parâmetros MCP. Cada um tem de existir no servidor, senão a skill
// envelheceu.
var snakeTokens = regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")

func TestSkillMentionsOnlyRealToolsAndParams(t *testing.T) {
	cs := session(t)
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	known := map[string]bool{}
	for _, tool := range tools.Tools {
		known[tool.Name] = true
		schema, ok := tool.InputSchema.(map[string]any)
		require.True(t, ok, "%s schema: %T", tool.Name, tool.InputSchema)
		props, _ := schema["properties"].(map[string]any)
		for name := range props {
			known[name] = true
		}
	}
	for _, name := range []string{"resolve_symbol", "include_skeleton", "exclude_tests", "max_tokens"} {
		require.True(t, known[name], "sanity: %s should be known", name)
	}
	for _, m := range snakeTokens.FindAllStringSubmatch(skill.Content(), -1) {
		assert.True(t, known[m[1]], "skill mentions %q, which is neither a tool nor a parameter", m[1])
	}
	for _, tool := range tools.Tools {
		assert.True(t, strings.Contains(skill.Content(), "`"+tool.Name+"`"), "skill does not mention tool %s", tool.Name)
	}
}
