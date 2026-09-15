// Package e2e compila o binário uma vez e o exercita contra cópias dos
// fixtures em diretórios temporários.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mira-e2e")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "mira")
	if runtime.GOOS == "windows" {
		binary += ".exe" // o Windows só executa arquivos com extensão de executável
	}
	// O runtime tree-sitter é cgo: o binário é compilado com o toolchain
	// padrão da máquina.
	build := exec.Command("go", "build", "-o", binary, "../../cmd/mira")
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building binary: %v\n%s", err, out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

var fixtures = []string{"ts-frontend", "node-backend", "java-service", "go-service", "py-service"}

// fixture copia um fixture para um diretório temporário.
func fixture(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "fixtures", name)
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
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
	})
	require.NoError(t, err)
	return root
}

// run executa o binário e devolve stdout; falha o teste se o comando falhar.
func run(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := tryRun(root, args...)
	require.NoError(t, err, out)
	return out
}

func tryRun(root string, args ...string) (string, error) {
	cmd := exec.Command(binary, append([]string{"--repo", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), nil
}

// runJSON executa com --json e decodifica em v.
func runJSON(t *testing.T, root string, v any, args ...string) {
	t.Helper()
	out := run(t, root, append([]string{"--json"}, args...)...)
	require.NoError(t, json.Unmarshal([]byte(out), v), out)
}

// indexed prepara um fixture já inicializado e indexado.
func indexed(t *testing.T, name string) string {
	t.Helper()
	root := fixture(t, name)
	run(t, root, "init")
	run(t, root, "index")
	return root
}
