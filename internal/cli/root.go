// Package cli monta os comandos cobra. Cada comando é um adaptador fino
// sobre indexer (escrita) e graph (leitura); a saída --json é a mesma que
// o servidor MCP devolve.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/JonathanSantos/mira/internal/render"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

// app guarda as flags globais e a saída, para os comandos não dependerem de
// estado global.
type app struct {
	repoFlag    string
	jsonOut     bool
	maxTokens   int
	noAutoIndex bool
	out         io.Writer
}

// New monta o comando raiz com todos os subcomandos.
func New() *cobra.Command {
	a := &app{out: os.Stdout}
	root := &cobra.Command{
		Use:           "mira",
		Short:         "Code graph for AI agents: navigate code by symbol, get snippets by line range",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.repoFlag, "repo", "", "repository root (default: cwd, walking up to .mira/ or .git/)")
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "print structured JSON output")
	root.PersistentFlags().IntVar(&a.maxTokens, "max-tokens", 0, "truncate text output to roughly this many tokens (0 = unlimited)")
	root.PersistentFlags().BoolVar(&a.noAutoIndex, "no-auto-index", false, "do not refresh the index (incremental) before answering a query")
	root.AddCommand(
		a.initCmd(), a.indexCmd(), a.statusCmd(), a.filesCmd(), a.searchCmd(), a.resolveCmd(),
		a.refsCmd(), a.symbolsCmd(), a.snippetCmd(), a.editCmd(), a.skillCmd(), a.mcpCmd(), a.doctorCmd(), a.versionCmd(),
	)
	root.SetGlobalNormalizationFunc(oldFlagNames)
	return root
}

// oldFlagNames aceita os nomes antigos das flags. A CLI usa os nomes dos
// parâmetros MCP em kebab-case (`exclude_tests` é `--exclude-tests`), para
// um agente não errar ao passar de um para o outro; scripts com `--no-tests`
// ou `--with-refs` continuam funcionando, sem os nomes antigos na ajuda.
func oldFlagNames(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	switch name {
	case "no-tests":
		name = "exclude-tests"
	case "skeleton":
		name = "include-skeleton"
	case "with-refs":
		name = "include-refs"
	case "old":
		name = "old-text"
	case "new":
		name = "new-text"
	}
	return pflag.NormalizedName(name)
}

// Execute roda a CLI e devolve o código de saída.
func Execute() int {
	if err := New().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "mira: %v\n", err)
		return 1
	}
	return 0
}

// root localiza a raiz do repositório alvo.
func (a *app) root() (string, error) {
	start := a.repoFlag
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting cwd: %w", err)
		}
		start = cwd
	}
	return repo.FindRoot(start)
}

// open abre um repositório já inicializado.
func (a *app) open() (string, repo.Config, *store.Store, error) {
	root, err := a.root()
	if err != nil {
		return "", repo.Config{}, nil, err
	}
	if !repo.Initialized(root) {
		return "", repo.Config{}, nil, fmt.Errorf("%s is not initialized: run `mira init` first", root)
	}
	cfg, err := repo.LoadConfig(root)
	if err != nil {
		return "", repo.Config{}, nil, err
	}
	st, err := store.Open(repo.DBPath(root))
	if err != nil {
		return "", repo.Config{}, nil, err
	}
	return root, cfg, st, nil
}

// print emite JSON compacto ou o texto compartilhado com o MCP, cortado no
// orçamento de tokens quando --max-tokens é dado.
func (a *app) print(v any, text string) error {
	if a.jsonOut {
		return json.NewEncoder(a.out).Encode(v)
	}
	_, err := io.WriteString(a.out, render.Budget(text, a.maxTokens))
	return err
}
