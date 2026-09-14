package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/skill"
)

// cliSpans são os exemplos `mira <cmd> ...` entre crases na skill.
var cliSpans = regexp.MustCompile("`mira ([a-z]+)([^`]*)`")

// TestSkillCommandsAndFlagsExist confere que todo comando e flag citados na
// skill existem na CLI, para a skill não envelhecer quando a CLI mudar.
func TestSkillCommandsAndFlagsExist(t *testing.T) {
	root := New()
	matches := cliSpans.FindAllStringSubmatch(skill.Content(), -1)
	require.NotEmpty(t, matches)
	seen := map[string]bool{}
	for _, m := range matches {
		name, rest := m[1], m[2]
		// Subcomandos (`mira edit replace ...`): as palavras seguintes que
		// não são placeholders nem flags completam o caminho do comando.
		path := []string{name}
		for _, tok := range strings.Fields(rest) {
			if strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "[") || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "\"") || tok == "..." {
				break
			}
			path = append(path, tok)
		}
		// Find devolve o comando mais fundo que casa; o resto são argumentos
		// (`refs addVisit` casa `refs`, `edit replace` casa o subcomando).
		cmd, _, err := root.Find(path)
		require.NoError(t, err, "command %q in skill", strings.Join(path, " "))
		require.NotEqual(t, root.Name(), cmd.Name(), "command %q in skill", strings.Join(path, " "))
		seen[name] = true
		for _, tok := range strings.Fields(rest) {
			if !strings.HasPrefix(tok, "--") {
				continue
			}
			flag := strings.TrimPrefix(tok, "--")
			found := cmd.Flags().Lookup(flag) != nil || root.PersistentFlags().Lookup(flag) != nil
			assert.True(t, found, "flag --%s of %s in skill", flag, name)
		}
	}
	for _, name := range []string{"files", "resolve", "refs", "search", "symbols", "snippet", "status", "index"} {
		assert.True(t, seen[name], "skill does not show `mira %s`", name)
	}
}
