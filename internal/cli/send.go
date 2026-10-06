package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/transfer"
)

func newSendCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "send [link]",
		Short: "Hand a link to another device with a short code",
		Long: `Hands a link to another device with a short code like 7-acid-rocket, so
nobody has to copy the link itself. On the other device, open the server's
/c page and type the code, or run secretli receive.

The link travels encrypted, and only after the other device has proved it
typed the same code; the code's words never leave the two devices. A code
works once and for ten minutes, and a wrong one ends the transfer. Given an
owner link, only the link to hand out is sent; the owner link stays here.

Prints the code and waits until the link was handed over. Without an
argument the link is read from stdin, or asked for at a terminal.`,
		Example: `  secretli send 'https://secretli.app/s#…'
  secretli share notes.txt -q | secretli send`,
		Args: cobra.MaximumNArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			link, err := e.linkArg(args)
			if err != nil {
				return err
			}
			if link.IsOwner() {
				e.note("Sending the link to hand out; the owner link stays here.\n")
			}
			return e.handOver(cmd.Context(), link.Recipient(), nil)
		}),
	}
}

// handOver sends a link with a code and waits until it was handed over.
// extra goes into the JSON that announces the code.
func (e *env) handOver(ctx context.Context, link share.Link, extra map[string]any) error {
	origin, err := share.Origin(link.Origin)
	if err != nil {
		return err
	}
	err = share.SendWithCode(ctx, e.client(origin), link.String(), func(code transfer.Code, expiresAt time.Time) {
		e.printCode(code, expiresAt, origin, extra)
	})
	if err != nil {
		return describeSendError(err)
	}
	if e.json {
		return e.emitJSON(map[string]any{"delivered": true})
	}
	e.say("Sent. The other device is opening the secret.\n")
	return nil
}

// printCode shows the code as soon as the transfer is open: on stdout, as
// the result, and how to use it on stderr when stdout is a pipe.
func (e *env) printCode(code transfer.Code, expiresAt time.Time, origin string, extra map[string]any) {
	if e.json {
		out := map[string]any{"code": code.String(), "code_expires_at": expiresAt.UTC()}
		for k, v := range extra {
			out[k] = v
		}
		_ = e.emitJSON(out)
		return
	}
	receive := "secretli receive " + code.String()
	if origin != DefaultServer {
		receive += " --server " + origin
	}
	page := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://") + "/c"
	waiting := fmt.Sprintf("Waiting for the other device. The code expires %s; Ctrl-C stops.", formatMoment(expiresAt, time.Now()))
	if e.quiet || !e.stdoutTTY {
		_, _ = fmt.Fprintln(e.stdout, code.String())
		e.note("On the other device, open %s and type the code, or run %s there.\n%s\n", page, receive, waiting)
		return
	}
	_, _ = fmt.Fprintf(e.stdout, "On the other device, open %s and type\n\n  %s\n\nor run %s there.\n%s\n", page, code, receive, waiting)
}

// explained is an error told in the CLI's words, which keeps the original
// for the exit code.
type explained struct {
	msg string
	err error
}

func (x *explained) Error() string { return x.msg }
func (x *explained) Unwrap() error { return x.err }

func explain(err error, msg string) error { return &explained{msg: msg, err: err} }

// describeSendError says what went wrong on the sending side.
func describeSendError(err error) error {
	ended, isEnded := errors.AsType[*transfer.EndedError](err)
	switch {
	case errors.Is(err, transfer.ErrCodeMismatch):
		return explain(err, "the code didn't match; run send again for a new code")
	case isEnded && ended.Reason == "cancelled":
		return explain(err, "the other device stopped the transfer")
	case isEnded:
		return explain(err, "nobody entered the code in time; run send again for a new code")
	case api.IsStatus(err, http.StatusServiceUnavailable):
		return explain(err, "too many transfers right now; try again in a minute")
	case api.IsStatus(err, http.StatusTooManyRequests):
		return explain(err, "too many attempts; wait a minute and try again")
	}
	return err
}
