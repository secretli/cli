package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/secretli/cli/internal/share"
)

func newDeleteCmd(e *env) *cobra.Command {
	var yes, fromClipboard bool
	cmd := &cobra.Command{
		Use:   "delete <owner-link>",
		Short: "Delete a secret for everyone",
		Long: `Deletes a secret before it is opened or expires. Only the owner link, the one
with the part after "!", can do this. The link stops working right away.`,
		Example: `  secretli delete 'https://secretli.app/s#…!…'
  secretli delete "$OWNER_LINK" --yes
  secretli delete -c                       the owner link from the clipboard`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			return e.delete(cmd.Context(), args, yes, fromClipboard)
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask; needed when not at a terminal")
	cmd.Flags().BoolVarP(&fromClipboard, "clipboard", "c", false, "take the owner link from the clipboard instead of asking for it")
	return cmd
}

func (e *env) delete(ctx context.Context, args []string, yes, fromClipboard bool) error {
	if err := checkClipboard(fromClipboard); err != nil {
		return err
	}
	link, err := e.linkArg(ctx, args, fromClipboard)
	if err != nil {
		return err
	}
	if !link.IsOwner() {
		return share.ErrNotOwner
	}
	if !yes {
		t, err := e.terminal()
		if err != nil {
			return errors.New("use --yes to delete without being asked")
		}
		ok, err := t.confirm(ctx, "Delete it for everyone? "+t.paint(yellow, "The link stops working right away."))
		t.close()
		if err != nil {
			return err
		}
		if !ok {
			return &exitWith{code: ExitError}
		}
	}
	if err := share.Delete(ctx, e.client(link.Origin), link); err != nil {
		return err
	}
	if e.json {
		return e.emitJSON(map[string]any{"deleted": true})
	}
	e.done("Deleted. The link doesn't open anything any more.\n")
	return nil
}
