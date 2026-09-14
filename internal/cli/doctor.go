package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/lang"
	"github.com/JonathanSantos/mira/internal/parser"
	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/skill"
	"github.com/JonathanSantos/mira/internal/store"
)

// check é um item do diagnóstico. Level: ok, warn ou fail.
type check struct {
	Level string `json:"level"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"`
}

type doctorResult struct {
	Root   string  `json:"root"`
	Checks []check `json:"checks"`
	Failed int     `json:"failed"`
}

// doctorCmd diz o que está faltando para o mira funcionar bem num
// repositório: init, índice fresco, runtime do parser, git, .gitignore,
// skill instalada e a linha de configuração do MCP.
func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the setup for this repository: init, index freshness, parser runtime, git, .gitignore, skill, MCP config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			res := a.diagnose(root)
			if err := a.print(res, renderDoctor(res)); err != nil {
				return err
			}
			if res.Failed > 0 {
				return fmt.Errorf("%d check(s) failed", res.Failed)
			}
			return nil
		},
	}
}

func (a *app) diagnose(root string) doctorResult {
	res := doctorResult{Root: root}
	add := func(level, name, text, fix string) {
		res.Checks = append(res.Checks, check{Level: level, Name: name, Text: text, Fix: fix})
		if level == "fail" {
			res.Failed++
		}
	}

	if fileExists(filepath.Join(root, ".git")) {
		add("ok", "repository", "git repository at "+root, "")
	} else {
		add("warn", "repository", root+" has no .git: files are walked manually with a .gitignore subset", "")
	}
	if _, err := exec.LookPath("git"); err != nil {
		add("warn", "git", "git not found on PATH: the walker falls back to a manual walk", "")
	} else {
		add("ok", "git", "git available", "")
	}

	if err := parserSelfTest(); err != nil {
		add("fail", "parser", "tree-sitter runtime failed: "+err.Error(), "rebuild with cgo enabled and a C compiler installed")
	} else {
		add("ok", "parser", "tree-sitter (cgo) parses TypeScript, Java, Python and Go", "")
	}

	if !repo.Initialized(root) {
		add("fail", "index", ".mira/index.db not found", "run `mira init && mira index`")
		add("info", "mcp", mcpHint(root), "")
		return res
	}
	cfg, err := repo.LoadConfig(root)
	if err != nil {
		add("fail", "config", err.Error(), "fix .mira/config.yaml")
		return res
	}
	langs := strings.Join(cfg.Languages, ", ")
	if langs == "" {
		langs = "all"
	}
	text := "on"
	if cfg.SkipText {
		text = "off (skip_text: true)"
	}
	add("ok", "config", fmt.Sprintf("languages: %s; text files: %s; extra ignores: %d", langs, text, len(cfg.Ignore)), "")

	st, err := store.Open(repo.DBPath(root))
	if err != nil {
		add("fail", "index", "cannot open index: "+err.Error(), "delete .mira/index.db and run `mira init && mira index`")
		return res
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err == nil {
		add("ok", "schema", fmt.Sprintf("schema v%d (binary expects v%d; older indexes are migrated on open)", v, store.MigrationCount()), "")
	}
	status, err := indexer.New(root, cfg, st).Status()
	if err != nil {
		add("fail", "index", err.Error(), "")
		return res
	}
	if status.Files == 0 {
		add("fail", "index", "index is empty", "run `mira index`")
	} else if status.Outdated > 0 {
		add("warn", "index", fmt.Sprintf("%d files indexed, %d out of date", status.Files, status.Outdated), "run `mira index` (or the reindex tool)")
	} else {
		add("ok", "index", fmt.Sprintf("%d files, %d symbols, %d refs, %d edges, up to date", status.Files, status.Symbols, status.Refs, status.Edges), "")
	}
	if hint := repo.GitignoreHint(root); hint != "" {
		add("warn", "gitignore", ".mira/ is not ignored", "add `.mira/` to .gitignore (mira never edits it)")
	} else {
		add("ok", "gitignore", ".mira/ is ignored or there is no git", "")
	}
	if fileExists(filepath.Join(root, skill.DefaultDir, skill.FileName)) {
		add("ok", "skill", "agent skill installed at "+skill.DefaultDir, "")
	} else {
		add("warn", "skill", "agent skill not installed", "run `mira skill --install` (Claude Code loads .claude/skills/*/SKILL.md)")
	}
	if fileExists(filepath.Join(root, "tsconfig.json")) {
		add("ok", "tsconfig", "tsconfig.json found: paths/baseUrl are used to resolve imports", "")
	}
	add("info", "mcp", mcpHint(root), "")
	return res
}

// parserSelfTest garante que o runtime cgo e as gramáticas carregam.
func parserSelfTest() error {
	p := parser.New()
	for _, probe := range []struct {
		l   lang.Lang
		src string
	}{{lang.TypeScript, "const a: number = 1;"}, {lang.Java, "class A { void m() {} }"}, {lang.Python, "def f(x):\n    return x\n"}, {lang.Go, "package a\n\nfunc f() {}\n"}} {
		tree, err := p.Parse(probe.l, []byte(probe.src))
		if err != nil {
			return err
		}
		hasError := tree.HasError
		tree.Release()
		if hasError {
			return fmt.Errorf("%s grammar reported an error on a trivial snippet", probe.l)
		}
	}
	return nil
}

func mcpHint(root string) string {
	exe, err := os.Executable()
	if err != nil {
		exe = "mira"
	}
	return fmt.Sprintf("Claude Code: claude mcp add mira -- %s --repo %s mcp  |  other clients: {\"command\": %q, \"args\": [\"--repo\", %q, \"mcp\"]}", exe, root, exe, root)
}

func renderDoctor(res doctorResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mira doctor: %s\n", res.Root)
	for _, c := range res.Checks {
		fmt.Fprintf(&b, "%-4s %-10s %s\n", strings.ToUpper(c.Level), c.Name, c.Text)
		if c.Fix != "" {
			fmt.Fprintf(&b, "     %-10s fix: %s\n", "", c.Fix)
		}
	}
	if res.Failed == 0 {
		b.WriteString("all checks passed\n")
	}
	return b.String()
}
