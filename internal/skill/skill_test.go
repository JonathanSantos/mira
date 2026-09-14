package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentHasFrontmatter(t *testing.T) {
	c := Content()
	assert.True(t, strings.HasPrefix(c, "---\nname: mira\ndescription: "), c[:60])
	assert.Contains(t, c, "\n---\n\n# mira")
}

func TestInstall(t *testing.T) {
	root := t.TempDir()
	path, err := Install(root, "", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".claude", "skills", "mira", "SKILL.md"), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, Content(), string(data))

	_, err = Install(root, "", false)
	require.Error(t, err, "never overwrite the user's file silently")
	assert.Contains(t, err.Error(), "already exists")

	require.NoError(t, os.WriteFile(path, []byte("edited"), 0o644))
	_, err = Install(root, "", true)
	require.NoError(t, err)
	data, _ = os.ReadFile(path)
	assert.Equal(t, Content(), string(data), "--force overwrites")

	custom, err := Install(root, "docs/agents", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "docs", "agents", "SKILL.md"), custom)
}
