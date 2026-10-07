package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/transfer"
)

type receiveOptions struct {
	openOptions
	linkOnly bool
}

func newReceiveCmd(e *env) *cobra.Command {
	var o receiveOptions
	cmd := &cobra.Command{
		Use:   "receive [code]",
		Short: "Receive a link sent with a code, and open it",
		Long: `Receives a link that another device hands over with a short code, from
secretli send or from "Send with a code" in the web app, and opens it like
secretli open: text to stdout, files to the current directory or --out.
--link prints the link instead of opening it. --copy puts the text, or with
--link the link, on the clipboard instead, as with open.

A one-time secret is described and you are asked before it is opened, as
with open; --yes opens it without asking. Answering no prints the link,
which still works. Where there is no terminal to ask on, receive needs
--yes or --link, and checks that before it takes the code.

Typing is forgiving: "7 acid rocket", "7-ACID-ROCKET" and "7-aci-roc" all
work. A wrong code ends the transfer on both sides; ask for a new one.
Without an argument the code is read from stdin, or asked for at a terminal.`,
		Example: `  secretli receive 7-acid-rocket
  secretli receive 7 acid rocket --out ./received
  secretli receive 7-acid-rocket --link`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			return e.receive(cmd.Context(), o, args)
		}),
	}
	addOpenFlags(cmd, &o.openOptions)
	cmd.Flags().BoolVar(&o.linkOnly, "link", false, "print the link instead of opening it")
	return cmd
}

func (e *env) receive(ctx context.Context, o receiveOptions, args []string) error {
	// A code works once: find out now whether what it brings could be
	// opened, rather than lose the link after the transfer.
	if !o.yes && !o.linkOnly && !e.canAsk() {
		return errors.New("receive asks before it opens a one-time secret, and there is no terminal to ask on; use --yes to open it anyway, or --link to print the link")
	}
	if err := e.checkCopy(o.openOptions); err != nil {
		return err
	}
	code, err := e.codeArg(ctx, args)
	if err != nil {
		return err
	}
	raw, err := share.ReceiveWithCode(ctx, e.client(e.server), code)
	if err != nil {
		return describeReceiveError(err)
	}
	link, err := share.ParseLink(raw)
	if err != nil {
		return errors.New("the code delivered something that isn't a Secretli link")
	}
	// The web app opens only links for its own site; so does this.
	server, err := share.Origin(e.server)
	if err != nil {
		return err
	}
	if got, err := share.Origin(link.Origin); err != nil || got != server {
		return fmt.Errorf("the code delivered a link for another server, %s; it was not opened", link.Origin)
	}
	if o.linkOnly {
		if o.copy {
			return e.copySecret(link.String(), "the link")
		}
		return e.printLink(link)
	}
	// The code is used up; if the secret is not opened, the link is the
	// only way back to it.
	err = e.openLink(ctx, o.openOptions, link)
	switch {
	case errors.Is(err, errNotOpened):
		e.note("Not opened. Here is the link, which still works:\n")
	case errors.Is(err, errCopyIsForText):
		e.note("Not opened: %v. Here is the link, which still works:\n", err)
	default:
		return err
	}
	if err := e.printLink(link); err != nil {
		return err
	}
	return &exitWith{code: ExitError}
}

func (e *env) printLink(link share.Link) error {
	if e.json {
		return e.emitJSON(map[string]any{"link": link.String()})
	}
	_, _ = fmt.Fprintln(e.stdout, link.String())
	return nil
}

// codeArg takes the code from the arguments, which may be its parts, from
// stdin, or from a prompt.
func (e *env) codeArg(ctx context.Context, args []string) (transfer.Code, error) {
	raw := strings.Join(args, " ")
	switch {
	case raw != "":
	case !e.stdinTTY:
		line, err := interruptible(ctx, func() (string, error) { return bufio.NewReader(e.stdin).ReadString('\n') })
		if ctx.Err() != nil {
			return transfer.Code{}, ctx.Err()
		}
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return transfer.Code{}, errors.New("no code given, and nothing on stdin")
		}
		raw = line
	default:
		t, err := e.terminal()
		if err != nil {
			return transfer.Code{}, errors.New("no code given")
		}
		defer t.close()
		if raw, err = t.askLine(ctx, "Code: "); err != nil {
			return transfer.Code{}, err
		}
	}
	code, err := transfer.ParseCode(raw)
	if unknown, ok := errors.AsType[*transfer.UnknownWordError](err); ok {
		return transfer.Code{}, explain(err, fmt.Sprintf("%q isn't a code word; check the spelling", unknown.Word))
	}
	if err != nil {
		return transfer.Code{}, explain(err, "codes look like 7-acid-rocket: a number and two words")
	}
	return code, nil
}

// describeReceiveError says what went wrong on the receiving side.
func describeReceiveError(err error) error {
	ended, isEnded := errors.AsType[*transfer.EndedError](err)
	switch {
	case errors.Is(err, share.ErrNoSuchTransfer):
		return explain(err, "no transfer with that number; check the code, or ask for a new one")
	case errors.Is(err, share.ErrTransferClaimed):
		return explain(err, "that code was already used; ask for a new one")
	case errors.Is(err, transfer.ErrCodeMismatch):
		return explain(err, "the code didn't match; ask for a new code")
	case isEnded && ended.Reason == "cancelled":
		return explain(err, "the sender stopped the transfer")
	case isEnded:
		return explain(err, "the code expired; ask for a new one")
	case api.IsStatus(err, http.StatusTooManyRequests):
		return explain(err, "too many attempts; wait a minute and try again")
	}
	return err
}
