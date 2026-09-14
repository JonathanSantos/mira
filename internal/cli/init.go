package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/repo"
	"github.com/JonathanSantos/mira/internal/store"
)

type initResult struct {
	Root    string `json:"root"`
	Created bool   `json:"created"`
	Hint    string `json:"hint,omitempty"`
}

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create .mira/ with config.yaml and an empty index (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			created := !repo.Initialized(root)
			if _, err := repo.LoadConfig(root); err != nil {
				return err
			}
			if !fileExists(repo.ConfigPath(root)) {
				if err := repo.WriteConfig(root, repo.DefaultConfig()); err != nil {
					return err
				}
			}
			st, err := store.Open(repo.DBPath(root))
			if err != nil {
				return err
			}
			if err := st.Close(); err != nil {
				return fmt.Errorf("closing index: %w", err)
			}
			res := initResult{Root: root, Created: created, Hint: repo.GitignoreHint(root)}
			text := fmt.Sprintf("initialized %s\n", repo.DataDir(root))
			if !res.Created {
				text = fmt.Sprintf("already initialized: %s\n", repo.DataDir(root))
			}
			if res.Hint != "" {
				text += res.Hint + "\n"
			}
			return a.print(res, text)
		},
	}
}
