package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/mcp"
)

// Version é injetada no build por
// `-ldflags "-X github.com/JonathanSantos/mira/internal/cli.Version=v0.2.0"`.
var Version = "dev"

type versionResult struct {
	Version string `json:"version"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the mira version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mcp.Version = Version
			res := versionResult{Version: Version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
			return a.print(res, fmt.Sprintf("mira %s (%s %s/%s, tree-sitter via cgo)\n", res.Version, res.Go, res.OS, res.Arch))
		},
	}
}
