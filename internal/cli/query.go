package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/graph"
	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/render"
)

// withGraph abre o repositório, atualiza o índice (incremental, salvo
// --no-auto-index) e entrega um Graph pronto ao comando.
func (a *app) withGraph(fn func(g *graph.Graph) error) error {
	root, cfg, st, err := a.open()
	if err != nil {
		return err
	}
	defer st.Close()
	if !a.noAutoIndex {
		report, changed, err := indexer.New(root, cfg, st).Refresh(context.Background())
		if err != nil {
			return fmt.Errorf("refreshing index: %w", err)
		}
		if changed {
			fmt.Fprintf(os.Stderr, "mira: index refreshed (%d new, %d changed, %d removed)\n", report.New, report.Changed, report.Removed)
		}
	}
	return fn(graph.New(root, st))
}

func (a *app) searchCmd() *cobra.Command {
	var opts graph.SearchOptions
	cmd := &cobra.Command{
		Use:   "search <word|regex>",
		Short: "Search identifiers and string literals (grouped by file, definitions first); --regex greps line text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withGraph(func(g *graph.Graph) error {
				res, err := g.Search(args[0], opts)
				if err != nil {
					return err
				}
				return a.print(res, render.Search(res))
			})
		},
	}
	cmd.Flags().StringVar(&opts.Path, "path", "", "only files under this prefix or matching this glob (src/logic, src/**/*.ts)")
	cmd.Flags().BoolVar(&opts.ExcludeTests, "exclude-tests", false, "skip test files and directories")
	cmd.Flags().BoolVar(&opts.Regex, "regex", false, "treat the query as a Go regexp over line text (like grep)")
	cmd.Flags().IntVar(&opts.Limit, "limit", 100, "maximum number of hits in total")
	cmd.Flags().IntVar(&opts.PerFile, "per-file", 20, "maximum number of hits per file")
	cmd.Flags().IntVar(&opts.Context, "context", 0, "lines of context before and after each hit")
	return cmd
}

func (a *app) filesCmd() *cobra.Command {
	var depth int
	var noTests, dirsOnly bool
	cmd := &cobra.Command{
		Use:   "files [path]",
		Short: "Tree of indexed files with symbol counts (the ls/tree an agent should use first)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) == 1 {
				prefix = args[0]
			}
			return a.withGraph(func(g *graph.Graph) error {
				res, err := g.Files(prefix, depth, noTests, dirsOnly)
				if err != nil {
					return err
				}
				return a.print(res, render.Files(res))
			})
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 1, "how many directory levels to expand (-1 = all)")
	cmd.Flags().BoolVar(&noTests, "exclude-tests", false, "skip test files and directories")
	cmd.Flags().BoolVar(&dirsOnly, "dirs-only", false, "list only directories with their totals")
	return cmd
}

func (a *app) resolveCmd() *cobra.Command {
	var opts graph.ResolveOptions
	cmd := &cobra.Command{
		Use:   "resolve <name|Container.member|file:line>...",
		Short: "Definitions of one or more symbols with signature, numbered body or snippet, callers, callees and who exposes it (--include-skeleton: structure without the body)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withGraph(func(g *graph.Graph) error {
				results, err := g.ResolveMany(args, opts)
				if err != nil {
					return err
				}
				if len(results) == 1 {
					return a.print(results[0], render.Resolve(results[0]))
				}
				return a.print(results, render.ResolveMany(results))
			})
		},
	}
	cmd.Flags().IntVar(&opts.Depth, "depth", 1, "how many levels of callers/callees to expand")
	cmd.Flags().BoolVar(&opts.IncludeBody, "include-body", false, "include the full numbered body of each definition")
	cmd.Flags().BoolVar(&opts.IncludeSkeleton, "include-skeleton", false, "structure instead of body: returns, throws, locals, nested functions and what it uses, grouped by origin")
	cmd.Flags().IntVar(&opts.Around, "around", 0, "show only a window around this line of the definition (e.g. a return the skeleton pointed to)")
	cmd.Flags().IntVar(&opts.Context, "context", 8, "lines before and after --around")
	cmd.Flags().BoolVar(&opts.IncludeRefs, "include-refs", false, "also list the references resolved to each definition, grouped by file")
	cmd.Flags().IntVar(&opts.MaxRefs, "max-refs", 50, "maximum references listed with --include-refs")
	cmd.Flags().StringVar(&opts.Kind, "kind", "", "only definitions of this kind (class, function, method, type, interface, ...)")
	cmd.Flags().StringVar(&opts.Path, "path", "", "only definitions under this prefix or matching this glob")
	cmd.Flags().BoolVar(&opts.ExcludeTests, "exclude-tests", false, "skip definitions, callers and refs in test files")
	return cmd
}

func (a *app) refsCmd() *cobra.Command {
	var opts graph.RefsOptions
	cmd := &cobra.Command{
		Use:   "refs <name>...",
		Short: "List references to one or more names (calls, types, property accesses, imports) and how each one was resolved",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withGraph(func(g *graph.Graph) error {
				results, err := g.RefsMany(args, opts)
				if err != nil {
					return err
				}
				if len(results) == 1 {
					return a.print(results[0], render.Refs(results[0]))
				}
				// Vários nomes: JSON vira uma lista, texto vira blocos separados.
				return a.print(results, render.RefsMany(results))
			})
		},
	}
	cmd.Flags().StringVar(&opts.Path, "path", "", "only files under this prefix or matching this glob")
	cmd.Flags().BoolVar(&opts.ExcludeTests, "exclude-tests", false, "skip test files and directories")
	cmd.Flags().StringVar(&opts.Kind, "kind", "", "only references of this kind (call, method, property, type, new, jsx, annotation, import, ...)")
	cmd.Flags().IntVar(&opts.Limit, "limit", 200, "maximum number of references listed per name")
	return cmd
}

func (a *app) symbolsCmd() *cobra.Command {
	var compact bool
	cmd := &cobra.Command{
		Use:   "symbols <file>",
		Short: "List the symbols defined in a file (--compact: only name, kind and lines)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withGraph(func(g *graph.Graph) error {
				res, err := g.Symbols(args[0], compact)
				if err != nil {
					return err
				}
				return a.print(res, render.Symbols(res))
			})
		},
	}
	cmd.Flags().BoolVar(&compact, "compact", false, "omit signatures and annotations")
	return cmd
}

func (a *app) snippetCmd() *cobra.Command {
	var symbol string
	var plain bool
	cmd := &cobra.Command{
		Use:   "snippet <file> [<start> <end>]",
		Short: "Print exactly the requested line range of a file, or a symbol's full range with --symbol",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if symbol == "" && len(args) != 3 {
				return fmt.Errorf("give <start> <end> or --symbol <name>")
			}
			var start, end int
			if symbol == "" {
				var err error
				if start, err = strconv.Atoi(args[1]); err != nil {
					return fmt.Errorf("start line: %w", err)
				}
				if end, err = strconv.Atoi(args[2]); err != nil {
					return fmt.Errorf("end line: %w", err)
				}
			}
			return a.withGraph(func(g *graph.Graph) error {
				var res graph.SnippetResult
				var err error
				if symbol != "" {
					res, err = g.SnippetOfSymbol(args[0], symbol)
				} else {
					res, err = g.Snippet(args[0], start, end)
				}
				if err != nil {
					return err
				}
				res.Plain = plain
				return a.print(res, render.Snippet(res))
			})
		},
	}
	cmd.Flags().StringVar(&symbol, "symbol", "", "print the full range of this symbol instead of explicit lines")
	cmd.Flags().BoolVar(&plain, "no-numbers", false, "print the raw lines without line numbers")
	return cmd
}
