package share_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/cli/internal/share/sharetest"
	"github.com/secretli/format/transfer"
)

const transferLink = "https://secretli.example/s#dHMtdmVjdG9ycy1zaGFyZS1zZWNyZXQtMDAwMDAwMDA"

func TestOriginIsSpelledLikeABrowser(t *testing.T) {
	for in, want := range map[string]string{
		"https://secretli.app":           "https://secretli.app",
		"HTTPS://SecretLI.app:443/":      "https://secretli.app",
		"https://secretli.app:8443/path": "https://secretli.app:8443",
		"http://localhost:8080":          "http://localhost:8080",
		"http://LOCALHOST:80":            "http://localhost",
		"http://[::1]:8080":              "http://[::1]:8080",
	} {
		if got, err := share.Origin(in); err != nil || got != want {
			t.Errorf("Origin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "secretli.app", "ftp://secretli.app"} {
		if got, err := share.Origin(in); err == nil {
			t.Errorf("Origin(%q) = %q, want an error", in, got)
		}
	}
}

// sendInBackground starts SendWithCode and hands back the code once the
// transfer is open, and the send's result when it ends.
func sendInBackground(ctx context.Context, t *testing.T, srv *sharetest.Server, link string) (transfer.Code, <-chan error) {
	t.Helper()
	c := clientFor(srv)
	codes := make(chan transfer.Code, 1)
	done := make(chan error, 1)
	go func() {
		done <- share.SendWithCode(ctx, c, link, func(code transfer.Code, expiresAt time.Time) {
			if time.Until(expiresAt) < 5*time.Minute {
				t.Errorf("code expires at %v", expiresAt)
			}
			codes <- code
		})
	}()
	select {
	case code := <-codes:
		return code, done
	case err := <-done:
		t.Fatalf("send ended before the code was ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no code")
	}
	return transfer.Code{}, nil
}

func newRelayServer(t *testing.T) *sharetest.Server {
	t.Helper()
	_, srv := newClient(t, 32*1024*1024)
	return srv
}

func TestHandsALinkOverWithACode(t *testing.T) {
	srv := newRelayServer(t)
	code, sent := sendInBackground(context.Background(), t, srv, transferLink)
	if code.Nameplate != 1 {
		t.Errorf("nameplate = %d", code.Nameplate)
	}

	c := clientFor(srv)
	link, err := share.ReceiveWithCode(context.Background(), c, code)
	if err != nil {
		t.Fatal(err)
	}
	if link != transferLink {
		t.Errorf("received %q", link)
	}
	if err := <-sent; err != nil {
		t.Errorf("send: %v", err)
	}
	if states := srv.Transfers(); len(states) != 1 || states[0].Closed != "done" || !states[0].Delivered {
		t.Errorf("transfers = %+v", states)
	}
}

func TestAWrongCodeDeliversNothing(t *testing.T) {
	srv := newRelayServer(t)
	code, sent := sendInBackground(context.Background(), t, srv, transferLink)

	wrong := code
	wrong.Words[1] = "yoyo"
	if code.Words[1] == "yoyo" {
		wrong.Words[1] = "zucchini"
	}
	c := clientFor(srv)
	_, err := share.ReceiveWithCode(context.Background(), c, wrong)

	if !errors.Is(err, transfer.ErrCodeMismatch) {
		t.Errorf("receive: %v", err)
	}
	if err := <-sent; !errors.Is(err, transfer.ErrCodeMismatch) {
		t.Errorf("send: %v", err)
	}
	if states := srv.Transfers(); len(states) != 1 || states[0].Closed != "mismatch" || states[0].Delivered {
		t.Errorf("transfers = %+v", states)
	}
}

func TestANameplateIsClaimedOnce(t *testing.T) {
	srv := newRelayServer(t)
	c := clientFor(srv)

	if _, err := share.ReceiveWithCode(context.Background(), c, transfer.Code{Nameplate: 42, Words: [2]string{"acid", "rocket"}}); !errors.Is(err, share.ErrNoSuchTransfer) {
		t.Errorf("unknown nameplate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code, _ := sendInBackground(ctx, t, srv, transferLink)
	if _, err := c.ClaimTransfer(context.Background(), code.Nameplate); err != nil {
		t.Fatal(err)
	}
	if _, err := share.ReceiveWithCode(context.Background(), c, code); !errors.Is(err, share.ErrTransferClaimed) {
		t.Errorf("second claim: %v", err)
	}
}

func TestAnInterruptedSenderReleasesTheCode(t *testing.T) {
	srv := newRelayServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	code, sent := sendInBackground(ctx, t, srv, transferLink)

	cancel()

	if err := <-sent; !errors.Is(err, context.Canceled) {
		t.Errorf("send: %v", err)
	}
	if states := srv.Transfers(); len(states) != 1 || states[0].Closed != "cancelled" {
		t.Errorf("transfers = %+v", states)
	}
	c := clientFor(srv)
	if _, err := share.ReceiveWithCode(context.Background(), c, code); !errors.Is(err, share.ErrNoSuchTransfer) {
		t.Errorf("receive after the sender stopped: %v", err)
	}
}

func TestTheReceiverHearsThatTheSenderStopped(t *testing.T) {
	srv := newRelayServer(t)
	c := clientFor(srv)
	// A sender that opens the transfer and stops once it was answered.
	party := transfer.Party{Words: [2]string{"acid", "rocket"}, Origin: mustOrigin(t, srv.URL)}
	party.SID, _ = transfer.NewSID()
	offer, err := transfer.NewOffer(party)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(party.SID)
	opened, err := c.OpenTransfer(context.Background(), id, offer.Share)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan error, 1)
	go func() {
		_, err := share.ReceiveWithCode(context.Background(), c, transfer.Code{Nameplate: opened.Nameplate, Words: party.Words})
		received <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if states := srv.Transfers(); len(states) == 1 && states[0].Answered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.CloseTransfer(context.Background(), id, opened.SenderToken, "cancelled"); err != nil {
		t.Fatal(err)
	}

	ended, ok := errors.AsType[*transfer.EndedError](<-received)
	if !ok || ended.Reason != "cancelled" {
		t.Errorf("receive: %v", ended)
	}
}

func clientFor(srv *sharetest.Server) *api.Client { return api.New(srv.URL) }

func mustOrigin(t *testing.T, server string) string {
	t.Helper()
	origin, err := share.Origin(server)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}
