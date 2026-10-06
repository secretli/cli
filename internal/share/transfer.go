package share

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/transfer"
)

// closeTimeout bounds the request that releases the other side after an
// interruption, when the command's own context is already done.
const closeTimeout = 5 * time.Second

var (
	// ErrNoSuchTransfer is a nameplate the relay has no open transfer under.
	ErrNoSuchTransfer = errors.New("no transfer with that number")
	// ErrTransferClaimed is a nameplate someone else claimed already.
	ErrTransferClaimed = errors.New("that code was already used")
	// ErrBadTransfer is a transfer whose id the relay handed back mangled.
	ErrBadTransfer = errors.New("the relay answered with a broken transfer")
)

// Origin is a server's URL the way a browser writes its origin: scheme and
// host in lower case, the default port left out. It is part of what both
// sides of a transfer must agree on, so the CLI and the web app have to
// spell it the same.
func Origin(server string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(server))
	// url.Parse lower-cases the scheme already.
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("not a server address: %q", server)
	}
	scheme := u.Scheme
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && (scheme != "https" || port != "443") && (scheme != "http" || port != "80") {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

// SendWithCode opens a transfer for the link at the client's server and
// tells ready the code to show, then waits until the receiver typed the
// same code and hands the link over. A wrong code returns
// transfer.ErrCodeMismatch; a transfer that expired or that the other side
// stopped returns a *transfer.EndedError. Any other failure, an
// interruption included, ends the transfer so the other side stops waiting.
func SendWithCode(ctx context.Context, c *api.Client, link string, ready func(code transfer.Code, expiresAt time.Time)) error {
	if len(link) > transfer.MaxLinkSize {
		return transfer.ErrLinkTooLong
	}
	origin, err := Origin(c.BaseURL)
	if err != nil {
		return err
	}
	sid, err := transfer.NewSID()
	if err != nil {
		return err
	}
	words, err := transfer.RandomWords()
	if err != nil {
		return err
	}
	party := transfer.Party{Words: words, SID: sid, Origin: origin}
	offer, err := transfer.NewOffer(party)
	if err != nil {
		return err
	}
	transferID := base64.RawURLEncoding.EncodeToString(sid)
	opened, err := c.OpenTransfer(ctx, transferID, offer.Share)
	if err != nil {
		return err
	}
	ready(transfer.Code{Nameplate: opened.Nameplate, Words: words}, opened.ExpiresAt)

	relay := senderRelay{relay{c: c, id: transferID, token: opened.SenderToken}}
	err = transfer.Send(ctx, relay, party, offer, link)
	if err != nil && !transferOver(err) {
		relay.release(ctx)
	}
	return err
}

// ReceiveWithCode claims the transfer behind a code and returns the link it
// carries. The errors are those of SendWithCode, and ErrNoSuchTransfer or
// ErrTransferClaimed for a nameplate that cannot be claimed.
func ReceiveWithCode(ctx context.Context, c *api.Client, code transfer.Code) (string, error) {
	origin, err := Origin(c.BaseURL)
	if err != nil {
		return "", err
	}
	claimed, err := c.ClaimTransfer(ctx, code.Nameplate)
	switch {
	case api.IsStatus(err, http.StatusNotFound):
		return "", ErrNoSuchTransfer
	case api.IsStatus(err, http.StatusConflict):
		return "", ErrTransferClaimed
	case err != nil:
		return "", err
	}
	relay := receiverRelay{relay{c: c, id: claimed.TransferID, token: claimed.ReceiverToken}}
	sid, err := base64.RawURLEncoding.DecodeString(claimed.TransferID)
	if err != nil || len(sid) != transfer.SIDSize {
		relay.release(ctx)
		return "", ErrBadTransfer
	}
	party := transfer.Party{Words: code.Words, SID: sid, Origin: origin}
	link, err := transfer.Receive(ctx, relay, party, claimed.Offer)
	if err != nil && !transferOver(err) {
		relay.release(ctx)
	}
	return link, err
}

// transferOver reports whether the transfer has ended on the relay already:
// closed as a mismatch, or ended by the other side or by its expiry.
// Closing it again would only send a misleading "cancelled".
func transferOver(err error) bool {
	_, ended := errors.AsType[*transfer.EndedError](err)
	return ended || errors.Is(err, transfer.ErrCodeMismatch)
}

// relay is one side's view of a transfer at the server.
type relay struct {
	c     *api.Client
	id    string
	token string
}

func (r relay) Close(ctx context.Context, reason transfer.CloseReason) error {
	return ended(r.c.CloseTransfer(ctx, r.id, r.token, string(reason)))
}

// release closes the transfer as cancelled, even after an interruption,
// so that the other side stops waiting. A failure is ignored: the transfer
// then expires on its own.
func (r relay) release(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	_ = r.Close(ctx, transfer.Cancelled)
}

type senderRelay struct{ relay }

func (r senderRelay) AwaitAnswer(ctx context.Context) (transfer.Answer, error) {
	for {
		answer, err := r.c.AwaitTransferAnswer(ctx, r.id, r.token)
		if err != nil {
			return transfer.Answer{}, ended(err)
		}
		if answer != nil {
			return transfer.Answer{Share: answer.Share, Confirmation: answer.Confirmation}, nil
		}
	}
}

func (r senderRelay) Deliver(ctx context.Context, sealed []byte) error {
	return ended(r.c.PostTransferDelivery(ctx, r.id, r.token, sealed))
}

type receiverRelay struct{ relay }

func (r receiverRelay) Answer(ctx context.Context, answer transfer.Answer) error {
	return ended(r.c.PostTransferAnswer(ctx, r.id, r.token, api.TransferAnswer{Share: answer.Share, Confirmation: answer.Confirmation}))
}

func (r receiverRelay) AwaitDelivery(ctx context.Context) ([]byte, error) {
	for {
		sealed, err := r.c.AwaitTransferDelivery(ctx, r.id, r.token)
		if err != nil {
			return nil, ended(err)
		}
		if sealed != nil {
			return sealed, nil
		}
	}
}

// ended turns the relay's answers for a finished transfer into a
// *transfer.EndedError: 410 with its reason, and 404, since the server
// deletes a transfer shortly after it ends.
func ended(err error) error {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case http.StatusGone:
		reason, _ := apiErr.Details["reason"].(string)
		if reason == "" {
			reason = "expired"
		}
		return &transfer.EndedError{Reason: reason}
	case http.StatusNotFound:
		return &transfer.EndedError{Reason: "expired"}
	}
	return err
}
