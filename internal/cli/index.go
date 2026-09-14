package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/render"
)

func (a *app) indexCmd() *cobra.Command {
	var full bool
	var workers int
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Index new, changed and removed files incrementally",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			report, err := indexer.New(root, cfg, st).Run(cmd.Context(), indexer.Options{Full: full, Workers: workers})
			if err != nil {
				return err
			}
			return a.print(report, render.Report(report))
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "reindex every file, ignoring fingerprints and hashes")
	cmd.Flags().IntVar(&workers, "workers", 0, "parallel parse workers (default: number of CPUs)")
	return cmd
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show index counts per language and how many files are out of date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			status, err := indexer.New(root, cfg, st).Status()
			if err != nil {
				return err
			}
			return a.print(status, render.Status(status))
		},
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
