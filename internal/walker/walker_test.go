package walker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitignoreMatcher(t *testing.T) {
	tests := []struct {
		name    string
		rules   string
		path    string
		isDir   bool
		ignored bool
	}{
		{"simple name matches file anywhere", "secret.ts", "a/b/secret.ts", false, true},
		{"simple name matches dir anywhere", "generated", "src/generated/x.ts", false, true},
		{"dir-only pattern does not match file", "generated/", "src/generated", false, false},
		{"dir-only pattern matches dir", "generated/", "src/generated", true, true},
		{"anchored pattern only at root", "/build", "build/x.ts", false, true},
		{"anchored pattern not nested", "/build", "src/build/x.ts", false, false},
		{"star glob", "*.min.js", "lib/app.min.js", false, true},
		{"double star", "docs/**/draft.ts", "docs/a/b/draft.ts", false, true},
		{"negation wins when later", "*.log\n!keep.log", "logs/keep.log", false, false},
		{"comment and blank lines ignored", "# comment\n\nfoo.ts", "foo.ts", false, true},
		{"no match", "foo.ts", "bar.ts", false, false},
		{"question mark", "file?.ts", "file1.ts", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := parseGitignore("", []byte(tt.rules))
			ignored, _ := m.match(tt.path, tt.isDir)
			assert.Equal(t, tt.ignored, ignored)
		})
	}
}

func TestNestedGitignoreIsRelativeToItsDir(t *testing.T) {
	m := parseGitignore("src", []byte("/local.ts"))
	ignored, matched := m.match("src/local.ts", false)
	assert.True(t, ignored)
	assert.True(t, matched)
	_, matched = m.match("other/local.ts", false)
	assert.False(t, matched)
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

var sampleTree = map[string]string{
	"src/a.ts":                "export const a = 1;",
	"src/b.tsx":               "export const b = 1;",
	"src/c.java":              "class C {}",
	"src/d.md":                "# not code",
	"src/generated/g.ts":      "export const g = 1;",
	"node_modules/x/index.js": "module.exports = 1;",
	"dist/bundle.js":          "var x;",
	"build/out.js":            "var x;",
	".mira/index.db":          "",
	"lib/keep.ts":             "export const k = 1;",
	"lib/skip.ts":             "export const s = 1;",
	".gitignore":              "src/generated/\nlib/skip.ts\n",
}

func paths(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestWalkWithoutGit(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, sampleTree)
	files, err := Walk(root, Options{Extensions: map[string]bool{".ts": true, ".tsx": true, ".java": true, ".js": true}})
	require.NoError(t, err)
	assert.Equal(t, []string{"lib/keep.ts", "src/a.ts", "src/b.tsx", "src/c.java"}, paths(files))
	assert.Equal(t, int64(len("export const a = 1;")), files[1].Size)
}

func TestWalkExtraIgnoresFromConfig(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, sampleTree)
	files, err := Walk(root, Options{
		Extensions: map[string]bool{".ts": true, ".tsx": true, ".java": true},
		Ignore:     []string{"*.tsx", "lib/"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"src/a.ts", "src/c.java"}, paths(files))
}

func TestWalkWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	writeFiles(t, root, sampleTree)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	run("add", "src/a.ts")
	// b.tsx and lib/keep.ts stay untracked: they must still be listed.
	files, err := Walk(root, Options{Extensions: map[string]bool{".ts": true, ".tsx": true, ".java": true, ".js": true}})
	require.NoError(t, err)
	assert.Equal(t, []string{"lib/keep.ts", "src/a.ts", "src/b.tsx", "src/c.java"}, paths(files))

	// A tracked file deleted from disk is not returned.
	require.NoError(t, os.Remove(filepath.Join(root, "src/a.ts")))
	files, err = Walk(root, Options{Extensions: map[string]bool{".ts": true}})
	require.NoError(t, err)
	assert.Equal(t, []string{"lib/keep.ts"}, paths(files))
}
