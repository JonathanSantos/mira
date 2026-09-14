package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/skill"
)

type skillResult struct {
	Path string `json:"path"`
}

// skillCmd imprime a skill ou a instala no repositório alvo. Instalar é
// opt-in e nunca sobrescreve sem --force, pela mesma regra do .gitignore:
// arquivos do usuário são do usuário.
func (a *app) skillCmd() *cobra.Command {
	var install, force bool
	var dir string
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Print the agent skill (how to use mira well); --install writes it to .claude/skills/mira/SKILL.md",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !install {
				_, err := io.WriteString(a.out, skill.Content())
				return err
			}
			root, err := a.root()
			if err != nil {
				return err
			}
			path, err := skill.Install(root, dir, force)
			if err != nil {
				return err
			}
			return a.print(skillResult{Path: path}, fmt.Sprintf("installed %s\n", path))
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "write the skill into the repository instead of printing it")
	cmd.Flags().StringVar(&dir, "dir", skill.DefaultDir, "target directory, relative to the repository root")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing SKILL.md")
	return cmd
}
