// Package skill carrega a skill que ensina um agente a usar o mira:
// um markdown embutido no binário, servido pela CLI (`mira skill`) e
// pelo MCP (resource e prompt). Uma fonte só, para não envelhecer em dois
// lugares.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var content string

// DefaultDir é onde o Claude Code procura skills do projeto.
const DefaultDir = ".claude/skills/mira"

// FileName é o nome que o loader de skills espera.
const FileName = "SKILL.md"

// Content devolve a skill como markdown com frontmatter (name, description).
func Content() string {
	return content
}

// Install grava a skill em <root>/<dir>/SKILL.md e devolve o caminho. Não
// sobrescreve um arquivo existente sem force: o arquivo é do usuário.
func Install(root, dir string, force bool) (string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	path := filepath.Join(dir, FileName)
	if _, err := os.Stat(path); err == nil && !force {
		return "", fmt.Errorf("%s already exists (use --force to overwrite)", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}
