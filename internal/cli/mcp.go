package cli

import (
	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/mcp"
)

func (a *app) mcpCmd() *cobra.Command {
	var single bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the graph as an MCP server over stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			return mcp.Serve(cmd.Context(), root, cfg, st, mcp.Options{SingleTool: single, NoAutoIndex: a.noAutoIndex})
		},
	}
	cmd.Flags().BoolVar(&single, "single-tool", false, "expose one `explore` tool with an action parameter instead of eight tools (smaller schema per turn)")
	return cmd
}
