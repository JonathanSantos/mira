package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/JonathanSantos/mira/internal/editor"
	"github.com/JonathanSantos/mira/internal/indexer"
	"github.com/JonathanSantos/mira/internal/render"
)

// editCmd edita por símbolo. replace, insert-before e insert-after recebem
// o texto por --text ou stdin; replace-in troca um trecho exato dentro do
// símbolo (--old-text/--new-text); delete apaga a definição (recusa se ainda há
// referências, a menos que --force); rename troca o nome na declaração e em
// todas as referências resolvidas. --preview mostra o diff sem gravar. O
// índice é atualizado antes (para o range estar certo) e depois (para
// verificar o resultado e a próxima consulta já ver).
func (a *app) editCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit by symbol: replace, insert before/after, delete or rename a definition, with preview and verification",
		Long: "Edit by symbol: replace, replace-in, insert before/after, delete or rename a definition, with preview and verification.\n\n" +
			"<symbol> is a name, Container.member or a qualified name; overloads share a name, so name:line picks the one\n" +
			"that contains that line (Owner.getPet:126). The index refreshes before and after, and the answer says\n" +
			"which references still resolve, what was left untouched and why.",
	}
	for _, action := range editor.Actions {
		cmd.AddCommand(a.editAction(action))
	}
	return cmd
}

func (a *app) editAction(action string) *cobra.Command {
	var text, oldText, newText string
	var preview, force, all bool
	short := map[string]string{
		editor.Replace:      "Replace the whole definition of <symbol> in <file> (signature, body, annotations); callers are re-checked after",
		editor.ReplaceIn:    "Replace an exact piece of text inside <symbol> (--old-text, --new-text); it must appear once unless --all",
		editor.InsertBefore: "Insert text immediately before the definition of <symbol> in <file>",
		editor.InsertAfter:  "Insert text immediately after the definition of <symbol> in <file>",
		editor.Delete:       "Delete <symbol> and its doc comment; refuses while other code still references it (--force to override)",
		editor.Rename:       "Rename <symbol> at its declaration and every reference resolved to it, across files",
	}[action]
	use, nargs := action+" <file> <symbol>", 2
	if action == editor.Rename {
		use, nargs = use+" <new-name>", 3
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := editor.Request{Action: action, File: args[0], Symbol: args[1], Preview: preview, Force: force}
			switch action {
			case editor.Rename:
				req.Text = args[2]
			case editor.ReplaceIn:
				if !cmd.Flags().Changed("new-text") {
					return fmt.Errorf("pass --new-text with the replacement (--new-text '' deletes the text)")
				}
				req.Old, req.Text, req.All = oldText, newText, all
			case editor.Replace, editor.InsertBefore, editor.InsertAfter:
				t, err := editText(text)
				if err != nil {
					return err
				}
				req.Text = t
			}
			root, cfg, st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := context.Background()
			ix := indexer.New(root, cfg, st)
			if _, _, err := ix.Refresh(ctx); err != nil {
				return fmt.Errorf("refreshing index: %w", err)
			}
			res, err := editor.Apply(root, st, req)
			if err != nil {
				return err
			}
			if !preview {
				if _, _, err := ix.Refresh(ctx); err != nil {
					return fmt.Errorf("refreshing index after edit: %w", err)
				}
				if err := editor.Verify(st, &res); err != nil {
					return fmt.Errorf("verifying edit: %w", err)
				}
			}
			return a.print(res, render.Edit(res))
		},
	}
	if action == editor.Replace || action == editor.InsertBefore || action == editor.InsertAfter {
		cmd.Flags().StringVar(&text, "text", "", "the new text (default: read from stdin)")
	}
	if action == editor.ReplaceIn {
		cmd.Flags().StringVar(&oldText, "old-text", "", "exact text to find inside the symbol, indentation included")
		cmd.Flags().StringVar(&newText, "new-text", "", "text that replaces it ('' deletes it)")
		cmd.Flags().BoolVar(&all, "all", false, "replace every occurrence inside the symbol instead of requiring exactly one")
	}
	if action == editor.Delete {
		cmd.Flags().BoolVar(&force, "force", false, "delete even if other code still references the symbol (those sites are listed)")
	}
	cmd.Flags().BoolVar(&preview, "preview", false, "show the diff and what would be skipped, without writing anything")
	return cmd
}

// editText devolve o texto de --text ou, sem ele, o que vier no stdin.
func editText(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading text from stdin: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("no text: pass --text or pipe it on stdin")
	}
	return string(data), nil
}
