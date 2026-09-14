package hasher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFingerprintAndHash(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mtime time.Time) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		require.NoError(t, os.Chtimes(path, mtime, mtime))
		return path
	}
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		contentA   string
		contentB   string
		mtimeA     time.Time
		mtimeB     time.Time
		sameFinger bool
		sameHash   bool
	}{
		{"same mtime and size, different content", "hello", "world", base, base, true, false},
		{"same content, different mtime", "hello", "hello", base, base.Add(time.Hour), false, true},
		{"identical", "hello", "hello", base, base, true, true},
		{"different size", "hello", "hello!", base, base, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := write("a.ts", tt.contentA, tt.mtimeA)
			b := write("b.ts", tt.contentB, tt.mtimeB)
			infoA, err := os.Stat(a)
			require.NoError(t, err)
			infoB, err := os.Stat(b)
			require.NoError(t, err)
			assert.Equal(t, tt.sameFinger, FingerprintOf(infoA) == FingerprintOf(infoB))

			_, hashA, err := ReadAndHash(a)
			require.NoError(t, err)
			_, hashB, err := ReadAndHash(b)
			require.NoError(t, err)
			assert.Equal(t, tt.sameHash, hashA == hashB)
			assert.Len(t, hashA, 64)
		})
	}
}

func TestReadAndHashMissingFile(t *testing.T) {
	_, _, err := ReadAndHash(filepath.Join(t.TempDir(), "missing.ts"))
	assert.Error(t, err)
}
