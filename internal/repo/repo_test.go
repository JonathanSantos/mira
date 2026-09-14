package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanSantos/mira/internal/lang"
)

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	got, err := FindRoot(nested)
	require.NoError(t, err)
	assert.Equal(t, nested, got, "without markers the start dir is the root")

	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	got, err = FindRoot(nested)
	require.NoError(t, err)
	assert.Equal(t, root, got, ".git marks the root")

	require.NoError(t, os.Mkdir(filepath.Join(root, "a", DirName), 0o755))
	got, err = FindRoot(nested)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "a"), got, "nearest .mira wins")
}

func TestConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, DefaultConfig(), cfg)
	assert.True(t, cfg.Enabled(lang.Java))

	require.NoError(t, WriteConfig(root, Config{Languages: []string{"java"}, Ignore: []string{"gen/"}}))
	cfg, err = LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"java"}, cfg.Languages)
	assert.Equal(t, []string{"gen/"}, cfg.Ignore)
	assert.True(t, cfg.Enabled(lang.Java))
	assert.False(t, cfg.Enabled(lang.TypeScript))

	require.NoError(t, os.WriteFile(ConfigPath(root), []byte("languages: [cobol]\n"), 0o644))
	_, err = LoadConfig(root)
	assert.Error(t, err, "unsupported language is rejected")
}

func TestGitignoreHint(t *testing.T) {
	root := t.TempDir()
	assert.Equal(t, "", GitignoreHint(root), "no git, no hint")

	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	assert.Contains(t, GitignoreHint(root), ".mira/")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules\n.mira/\n"), 0o644))
	assert.Equal(t, "", GitignoreHint(root), "already ignored")
}
