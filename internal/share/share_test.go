package share_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/cli/internal/share/sharetest"
	"github.com/secretli/format/bundle"
)

// memorySink keeps what Open hands over.
type memorySink struct {
	text  []byte
	files map[string]*bytes.Buffer
}

func (m *memorySink) Text(text []byte) error { m.text = text; return nil }

func (m *memorySink) File(file bundle.Entry) (io.WriteCloser, error) {
	if m.files == nil {
		m.files = map[string]*bytes.Buffer{}
	}
	buf := &bytes.Buffer{}
	m.files[file.Name] = buf
	return nopCloser{buf}, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// newClient talks to the fake server, or to the real one named by
// SECRETLI_TEST_SERVER.
func newClient(t *testing.T, partSize int64) (*api.Client, *sharetest.Target) {
	t.Helper()
	srv := sharetest.Start(t, partSize)
	return api.New(srv.URL), srv
}

func TestShareAndOpenText(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t, 32*1024*1024)

	result, err := share.Share(ctx, c, share.Params{Text: []byte("the launch code is 0000")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != share.KindText || result.Reusable || result.PasswordProtected || result.Size != 23 {
		t.Errorf("result = %+v", result)
	}
	if !result.Link.IsOwner() || !strings.HasPrefix(result.Link.String(), c.BaseURL+"/s#") {
		t.Errorf("link = %s", result.Link)
	}
	recipient := result.Link.Recipient()
	if recipient.IsOwner() || !strings.Contains(result.Link.String(), recipient.String()+"!") {
		t.Errorf("recipient link = %s, owner link = %s", recipient, result.Link)
	}

	info, err := share.Inspect(ctx, c, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != share.KindText || info.PasswordProtected || info.Reusable {
		t.Errorf("info = %+v", info)
	}

	sink := &memorySink{}
	opened, err := share.Open(ctx, c, recipient, "", sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(sink.text) != "the launch code is 0000" || opened.Info.Kind != share.KindText || opened.Version != 3 {
		t.Errorf("text = %q, opened = %+v", sink.text, opened)
	}

	// A one-time secret is gone once opened, and its link finds nothing.
	_, err = share.Inspect(ctx, c, recipient)
	var notFound *share.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("after opening: err = %v, want a NotFoundError", err)
	}
	if _, err := share.Open(ctx, c, recipient, "", &memorySink{}, nil); !errors.As(err, &notFound) {
		t.Errorf("second open: err = %v, want a NotFoundError", err)
	}
}

func TestShareFilesWithPasswordInSeveralParts(t *testing.T) {
	ctx := context.Background()
	// Parts are exactly the 9 MiB the server asks for, whatever chunks they
	// cut through, so a 20 MiB file goes up as three parts, two of them at
	// once.
	c, srv := newClient(t, 9*1024*1024)
	big := make([]byte, 20*1024*1024)
	for i := range big {
		big[i] = byte(i * 7)
	}
	files := []bundle.Source{
		{Name: "big.bin", Size: int64(len(big)), Reader: bytes.NewReader(big)},
		{Name: "notes.txt", Type: "text/plain", Size: 5, Reader: strings.NewReader("notes")},
	}
	var progressCalls int
	var lastUploaded, lastTotal int64
	result, err := share.Share(ctx, c, share.Params{
		Files: files, Password: "hunter2", Reusable: true, Expiration: "1h",
		Progress: func(uploaded, total int64) { progressCalls++; lastUploaded, lastTotal = uploaded, total },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != share.KindFiles || !result.Reusable || !result.PasswordProtected || len(result.Names) != 2 {
		t.Errorf("result = %+v", result)
	}
	if progressCalls < 2 || lastUploaded != lastTotal {
		t.Errorf("progress: %d calls, ended at %d of %d", progressCalls, lastUploaded, lastTotal)
	}
	if fake := srv.Fake(); fake != nil {
		if sizes := fake.PartSizes(); len(sizes) != 3 || sizes[0] != 9*1024*1024 || sizes[1] != 9*1024*1024 || sizes[2]+18*1024*1024 != lastTotal {
			t.Errorf("part sizes = %v of %d bytes, want two of exactly 9 MiB and the rest", sizes, lastTotal)
		}
	}

	recipient := result.Link.Recipient()
	info, err := share.Inspect(ctx, c, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if !info.PasswordProtected || !info.Reusable || info.Kind != share.KindFiles {
		t.Errorf("info = %+v", info)
	}
	if _, err := share.Open(ctx, c, recipient, "", &memorySink{}, nil); !errors.Is(err, share.ErrPasswordRequired) {
		t.Errorf("no password: err = %v", err)
	}
	if _, err := share.Open(ctx, c, recipient, "wrong", &memorySink{}, nil); !errors.Is(err, share.ErrWrongPassword) {
		t.Errorf("wrong password: err = %v", err)
	}

	sink := &memorySink{}
	var last int64
	opened, err := share.Open(ctx, c, recipient, "hunter2", sink, func(done, _ int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if opened.Version != 3 {
		t.Errorf("bundle version %d, want 3", opened.Version)
	}
	if !bytes.Equal(sink.files["big.bin"].Bytes(), big) || sink.files["notes.txt"].String() != "notes" {
		t.Error("decrypted files differ from the originals")
	}
	if last != int64(len(big))+5 {
		t.Errorf("progress ended at %d", last)
	}

	// Reusable: it stays, and says that a recipient opened it.
	if _, err := share.Open(ctx, c, result.Link, "hunter2", &memorySink{}, nil); err != nil {
		t.Fatal(err)
	}
	info, err = share.Inspect(ctx, c, result.Link)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Opened {
		t.Error("a recipient opened it, so Opened must be set")
	}
}

// choosingSink keeps only the files it chooses, and what it was offered.
type choosingSink struct {
	memorySink
	choose  []int
	offered []bundle.Entry
}

func (c *choosingSink) Choose(_ context.Context, files []bundle.Entry) ([]int, error) {
	c.offered = files
	return c.choose, nil
}

func TestOpenReadsBundleVersion3AndOnlyTheChosenFiles(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, 32*1024*1024)
	// Above 1 MiB, so that the files are read by range and not from the
	// bundle fetched whole.
	big := bytes.Repeat([]byte("0123456789abcdef"), 96*1024)
	files := []bundle.Source{
		{Name: "big.bin", Size: int64(len(big)), Reader: bytes.NewReader(big)},
		{Name: "skipped.bin", Size: 600 * 1024, Reader: bytes.NewReader(make([]byte, 600*1024))},
		{Name: "notes.txt", Type: "text/plain", Size: 5, Reader: strings.NewReader("notes")},
	}
	l := sharetest.ShareStream(t, srv.URL, "bundle", false, files...)

	info, err := share.Inspect(ctx, c, l)
	if err != nil || info.Kind != share.KindFiles {
		t.Fatalf("info = %+v, %v", info, err)
	}
	sink := &choosingSink{choose: []int{0, 2}}
	var last, total int64
	opened, err := share.Open(ctx, c, l.Recipient(), "", sink, func(done, all int64) { last, total = done, all })
	if err != nil {
		t.Fatal(err)
	}
	if opened.Version != 3 || len(opened.Files) != 3 || len(sink.offered) != 3 || sink.offered[1].Name != "skipped.bin" {
		t.Errorf("opened = %+v, offered %+v", opened, sink.offered)
	}
	if len(sink.files) != 2 || !bytes.Equal(sink.files["big.bin"].Bytes(), big) || sink.files["notes.txt"].String() != "notes" {
		t.Errorf("saved %d files, or they differ from the originals", len(sink.files))
	}
	if want := int64(len(big)) + 5; last != want || total != want {
		t.Errorf("progress ended at %d of %d, want %d of %d", last, total, want, want)
	}

	// A note in version 3 is text, and nothing is offered to choose from.
	note := []bundle.Source{{Name: "secret.txt", Type: "text/plain", Size: 4, Reader: strings.NewReader("hush")}}
	l = sharetest.ShareStream(t, srv.URL, "text", true, note...)
	sink = &choosingSink{}
	if opened, err = share.Open(ctx, c, l.Recipient(), "", sink, nil); err != nil {
		t.Fatal(err)
	}
	if string(sink.text) != "hush" || sink.offered != nil || opened.Version != 3 || opened.Info.Kind != share.KindText {
		t.Errorf("text = %q, offered %+v, opened %+v", sink.text, sink.offered, opened)
	}
}

// Bundles from before version 3 still open, until no such secret can exist.
func TestOpenReadsBundleVersion2(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, 32*1024*1024)
	files := []bundle.Source{
		{Name: "a.txt", Type: "text/plain", Size: 1, Reader: strings.NewReader("a")},
		{Name: "b.txt", Type: "text/plain", Size: 2, Reader: strings.NewReader("bb")},
	}
	l := sharetest.ShareVersion2(t, srv.URL, "bundle", true, files...)
	sink := &choosingSink{choose: []int{1}}
	opened, err := share.Open(ctx, c, l.Recipient(), "", sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Version != 2 || len(sink.offered) != 2 || len(sink.files) != 1 || sink.files["b.txt"].String() != "bb" {
		t.Errorf("opened %+v, offered %+v, saved %d files", opened, sink.offered, len(sink.files))
	}

	note := []bundle.Source{{Name: "secret.txt", Type: "text/plain", Size: 4, Reader: strings.NewReader("hush")}}
	l = sharetest.ShareVersion2(t, srv.URL, "text", true, note...)
	sink = &choosingSink{}
	if opened, err = share.Open(ctx, c, l.Recipient(), "", sink, nil); err != nil {
		t.Fatal(err)
	}
	if string(sink.text) != "hush" || opened.Version != 2 {
		t.Errorf("text = %q, opened %+v", sink.text, opened)
	}
}

func TestOpenedMeansARecipientOpenedIt(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t, 32*1024*1024)
	result, err := share.Share(ctx, c, share.Params{Text: []byte("again and again"), Reusable: true})
	if err != nil {
		t.Fatal(err)
	}
	opened := func() bool {
		t.Helper()
		info, err := share.Inspect(ctx, c, result.Link)
		if err != nil {
			t.Fatal(err)
		}
		return info.Opened
	}

	if opened() {
		t.Error("nobody has opened it yet")
	}
	// The owner's own look is not an opening, a recipient's is.
	if _, err := share.Open(ctx, c, result.Link, "", &memorySink{}, nil); err != nil {
		t.Fatal(err)
	}
	if opened() {
		t.Error("only the owner has looked, so it is not opened")
	}
	for range 2 {
		if _, err := share.Open(ctx, c, result.Link.Recipient(), "", &memorySink{}, nil); err != nil {
			t.Fatal(err)
		}
		if !opened() {
			t.Error("a recipient opened it, so it is opened")
		}
	}
}

func TestOneTimeSecretOpenedByItsOwnerIsGoneForBothLinks(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t, 32*1024*1024)
	result, err := share.Share(ctx, c, share.Params{Text: []byte("mine")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := share.Open(ctx, c, result.Link, "", &memorySink{}, nil); err != nil {
		t.Fatal(err)
	}

	// The owner link finds nothing, the same as the recipient link.
	for name, l := range map[string]share.Link{"owner link": result.Link, "recipient link": result.Link.Recipient()} {
		_, err = share.Inspect(ctx, c, l)
		var notFound *share.NotFoundError
		if !errors.As(err, &notFound) {
			t.Errorf("%s after the owner opened it: err = %v, want a NotFoundError", name, err)
		}
	}
}

func TestDeleteNeedsTheOwnerLinkAndLeavesNothing(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, 32*1024*1024)
	result, err := share.Share(ctx, c, share.Params{Text: []byte("bye")})
	if err != nil {
		t.Fatal(err)
	}
	if err := share.Delete(ctx, c, result.Link.Recipient()); !errors.Is(err, share.ErrNotOwner) {
		t.Errorf("recipient link: err = %v, want ErrNotOwner", err)
	}
	if err := share.Delete(ctx, c, result.Link); err != nil {
		t.Fatal(err)
	}
	if fake := srv.Fake(); fake != nil && fake.Secrets() != 0 {
		t.Error("the secret is still on the server")
	}
	// Both links find nothing, and deleting again finds nothing either.
	var notFound *share.NotFoundError
	for name, l := range map[string]share.Link{"owner link": result.Link, "recipient link": result.Link.Recipient()} {
		if _, err := share.Inspect(ctx, c, l); !errors.As(err, &notFound) {
			t.Errorf("%s after delete: err = %v, want a NotFoundError", name, err)
		}
	}
	if err := share.Delete(ctx, c, result.Link); !errors.As(err, &notFound) {
		t.Errorf("second delete: err = %v, want a NotFoundError", err)
	}
}

// An expired secret is not found, the same as one that was opened or deleted:
// the server keeps nothing of any of them. A real server cannot be made to
// wait, so this one is for the fake.
func TestAnExpiredSecretIsNotFoundLikeAGoneOne(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, 32*1024*1024)
	fake := srv.Fake()
	if fake == nil {
		t.Skip("a real server cannot be made to expire its secrets")
	}
	newSecret := func() share.Link {
		t.Helper()
		result, err := share.Share(ctx, c, share.Params{Text: []byte("x")})
		if err != nil {
			t.Fatal(err)
		}
		return result.Link
	}
	opened, deleted, expired := newSecret(), newSecret(), newSecret()
	if _, err := share.Open(ctx, c, opened.Recipient(), "", &memorySink{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := share.Delete(ctx, c, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := share.Inspect(ctx, c, expired); err != nil {
		t.Fatalf("before it expires: err = %v", err)
	}

	fake.Expire()
	var notFound *share.NotFoundError
	for name, l := range map[string]share.Link{"opened": opened, "deleted": deleted, "expired": expired} {
		if _, err := share.Inspect(ctx, c, l); !errors.As(err, &notFound) {
			t.Errorf("%s: err = %v, want a NotFoundError", name, err)
		}
		if _, err := share.Open(ctx, c, l.Recipient(), "", &memorySink{}, nil); !errors.As(err, &notFound) {
			t.Errorf("%s, opened: err = %v, want a NotFoundError", name, err)
		}
		if err := share.Delete(ctx, c, l); !errors.As(err, &notFound) {
			t.Errorf("%s, deleted: err = %v, want a NotFoundError", name, err)
		}
	}
}

func TestLinks(t *testing.T) {
	secret := strings.Repeat("a", 43)
	token := strings.Repeat("b", 43)
	link, err := share.ParseLink(" https://secretli.app/s#" + secret + "!" + token + " ")
	if err != nil {
		t.Fatal(err)
	}
	if link.Origin != "https://secretli.app" || link.Secret != secret || link.DeletionToken != token || !link.IsOwner() {
		t.Errorf("link = %+v", link)
	}
	if link.Recipient().String() != "https://secretli.app/s#"+secret {
		t.Errorf("recipient = %s", link.Recipient())
	}
	for _, bad := range []string{"", "secretli.app/s#" + secret, "https://secretli.app/share#" + secret, "https://secretli.app/s#" + secret[:42], "https://secretli.app/s#" + secret + "!short", "ftp://x/s#" + secret} {
		if _, err := share.ParseLink(bad); !errors.Is(err, share.ErrNotALink) {
			t.Errorf("%q: err = %v, want ErrNotALink", bad, err)
		}
	}
	if _, err := share.Share(context.Background(), api.New("http://127.0.0.1:1"), share.Params{Text: []byte("x"), Expiration: "2h"}); err == nil || !strings.Contains(err.Error(), "5m, 10m") {
		t.Errorf("bad expiry: err = %v", err)
	}
	if _, err := share.Share(context.Background(), api.New("http://127.0.0.1:1"), share.Params{}); !errors.Is(err, share.ErrNothingToShare) {
		t.Errorf("nothing: err = %v", err)
	}
}
