package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/secretli/cli/internal/share"
)

func newStatusCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [link]",
		Short: "Say what a link points to, without opening it",
		Long: `Describes a secret from its link alone: text or files, one-time or reusable,
password or not, when it was sent and when it expires. Nothing is opened, so
a one-time secret stays unopened.

For a secret that is gone it tells what happened: opened or deleted. An
expired secret is simply not found, like a link to nothing, so it is gone with
no word on why. The exit code is 4 for all of them, so scripts can tell, and
with --json the state is always live or gone.`,
		Example: `  secretli status 'https://secretli.app/s#…!…'
  secretli status "$LINK" --json | jq -r .state`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			return e.status(cmd.Context(), args)
		}),
	}
	return cmd
}

// exitWith ends a command with a code and no message; the result was printed.
type exitWith struct{ code int }

func (x *exitWith) Error() string { return "" }

func (e *env) status(ctx context.Context, args []string) error {
	link, err := e.linkArg(ctx, args)
	if err != nil {
		return err
	}
	now := time.Now()
	info, err := share.Inspect(ctx, e.client(link.Origin), link)
	var gone *share.GoneError
	if errors.As(err, &gone) {
		if e.json {
			out := map[string]any{
				"state":    "gone",
				"outcome":  gone.Gone.Outcome,
				"one_time": gone.Gone.BurnAfterRead,
			}
			if err := e.emitJSON(out); err != nil {
				return err
			}
		} else {
			_, _ = fmt.Fprintln(e.stdout, goneSentence(gone.Gone, link.IsOwner()))
		}
		return &exitWith{code: ExitGone}
	}
	// An expired secret and a link to nothing both get a 404, so there is no
	// outcome to tell. Scripts still get the state they branch on.
	var notFound *share.NotFoundError
	if errors.As(err, &notFound) {
		if e.json {
			if err := e.emitJSON(map[string]any{"state": "gone"}); err != nil {
				return err
			}
		} else {
			_, _ = fmt.Fprintln(e.stdout, notFoundSentence)
		}
		return &exitWith{code: ExitGone}
	}
	if err != nil {
		return err
	}

	if e.json {
		out := map[string]any{
			"state":          "live",
			"kind":           info.Kind,
			"password":       info.PasswordProtected,
			"reusable":       info.Reusable,
			"encrypted_size": info.EncryptedSize,
			"expires_at":     info.ExpiresAt.UTC(),
			"created_at":     info.CreatedAt.UTC(),
			"opened":         info.Opened,
		}
		return e.emitJSON(out)
	}

	what := "A one-time "
	if info.Reusable {
		what = "A reusable "
	}
	if info.Kind == share.KindText {
		what += "text secret"
	} else {
		what += "set of files"
	}
	if info.PasswordProtected {
		what += " with a password"
	}
	_, _ = fmt.Fprintf(e.stdout, "%s.\nSent %s, expires %s.\n", what, formatMoment(info.CreatedAt, now), formatMoment(info.ExpiresAt, now))
	if info.Opened {
		_, _ = fmt.Fprintln(e.stdout, "It has been opened.")
	} else {
		_, _ = fmt.Fprintln(e.stdout, "Not opened yet.")
	}
	return nil
}
