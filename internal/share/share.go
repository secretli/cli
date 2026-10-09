// Package share does what the web app does, without the browser: it makes a
// secret from text or files and uploads it, and it opens, inspects and
// deletes secrets by their links. The server never sees anything but
// ciphertext and token hashes, exactly as with the browser.
package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/bundle"
	"github.com/secretli/format/keys"
	"github.com/secretli/format/link"
)

const (
	// MaxEncryptedUploadBytes is the server's default upload limit.
	MaxEncryptedUploadBytes = 1024 * 1024 * 1024
	// UploadConcurrency is how many parts are in flight at once.
	UploadConcurrency = 3

	textFileName = "secret.txt"
)

// Expirations are the lifetimes the server accepts, shortest first.
var Expirations = []string{"5m", "10m", "15m", "1h", "4h", "12h", "1d", "3d", "7d"}

// DefaultExpiration is what the web app picks.
const DefaultExpiration = "1d"

// ValidExpiration reports whether the server accepts this lifetime.
func ValidExpiration(s string) bool {
	return slices.Contains(Expirations, s)
}

var (
	// ErrNotOwner is an operation that needs the owner link's deletion token.
	ErrNotOwner = errors.New("this needs the owner link, the one with the part after \"!\"")
	// ErrPasswordRequired is a secret with a password when none was given.
	ErrPasswordRequired = errors.New("this secret needs a password")
	// ErrWrongPassword is a password the server's blob token did not match.
	ErrWrongPassword = errors.New("wrong password")
	// ErrLinkMismatch is a link whose tokens do not fit the secret they name.
	ErrLinkMismatch = errors.New("this link doesn't fit the secret it points to")
	// ErrTooLarge is a share beyond the upload limit.
	ErrTooLarge = errors.New("too large to share: the limit is 1 GiB")
	// ErrTooManyFiles is a share whose file list passes the format's 4 MiB.
	ErrTooManyFiles = errors.New("too many files to share at once; pack them into an archive first")
	// ErrNothingToShare is a share with neither text nor files.
	ErrNothingToShare = errors.New("nothing to share")
)

// NotFoundError is a secret the server has no record of. Once a secret is
// gone, opened, deleted or expired, the server keeps nothing about it, so it
// answers the same as for a link to nothing, to the owner and a recipient
// alike.
type NotFoundError struct{}

func (*NotFoundError) Error() string {
	return "this secret is gone: it may have expired, been opened or been deleted"
}

// Link is a share link; ParseLink reads one as the web app prints it. Both
// come from the format library.
type Link = link.Link

// ErrNotALink is text that is not a Secretli link.
var ErrNotALink = link.ErrNotALink

// ParseLink reads a link as the web app prints it: https://host/s#secret or
// https://host/s#secret!deletionToken.
func ParseLink(raw string) (Link, error) { return link.Parse(raw) }

// Kind is what a secret holds.
type Kind string

const (
	// KindText is a note, shared as a single secret.txt inside the bundle.
	KindText Kind = "text"
	// KindFiles is one or more files.
	KindFiles Kind = "files"
)

// Params describes a secret to make.
type Params struct {
	// Files are shared as a bundle; when empty, Text is shared instead.
	Files []bundle.Source
	Text  []byte
	// Expiration is one of Expirations; empty means DefaultExpiration.
	Expiration string
	// Reusable keeps the link working until it expires; the default is once.
	Reusable bool
	Password string
	// Progress, if set, is told about uploaded bytes. It is never called
	// from two goroutines at once.
	Progress func(uploaded, total int64)
}

// Result is a shared secret: its links and what the server confirmed.
type Result struct {
	Link              Link
	ExpiresAt         time.Time
	Kind              Kind
	Size              int64
	Names             []string
	Reusable          bool
	PasswordProtected bool
}

// Share encrypts and uploads a secret and returns its links.
func Share(ctx context.Context, c *api.Client, p Params) (*Result, error) {
	expiration := p.Expiration
	if expiration == "" {
		expiration = DefaultExpiration
	}
	if !ValidExpiration(expiration) {
		return nil, fmt.Errorf("expiry %q is not one of %s", expiration, strings.Join(Expirations, ", "))
	}

	kind := KindFiles
	sources := p.Files
	if len(sources) == 0 {
		if len(p.Text) == 0 {
			return nil, ErrNothingToShare
		}
		kind = KindText
		sources = []bundle.Source{{Name: textFileName, Type: "text/plain", Size: int64(len(p.Text)), Reader: bytes.NewReader(p.Text)}}
	}
	names := make([]string, 0, len(sources))
	var total int64
	for _, s := range sources {
		names = append(names, s.Name)
		total += s.Size
	}
	// The plan needs only names, types and sizes, so the bundle's exact
	// size, padding included, is known before anything is read.
	plan, err := bundle.NewStreamPlan(sources)
	if errors.Is(err, bundle.ErrListTooLarge) {
		return nil, ErrTooManyFiles
	}
	if err != nil {
		return nil, err
	}
	if plan.TotalSize > MaxEncryptedUploadBytes {
		return nil, ErrTooLarge
	}

	base, err := keys.Generate()
	if err != nil {
		return nil, err
	}
	blobKeys, err := base.WithPassword(p.Password)
	if err != nil {
		return nil, err
	}
	metaType := "bundle"
	if kind == KindText {
		metaType = "text"
	}
	encryptedMeta, err := base.EncryptMeta(keys.Meta{Type: metaType, PasswordProtected: p.Password != ""})
	if err != nil {
		return nil, err
	}

	enc := base.Encoded()
	session, err := c.StartUpload(ctx, api.UploadRequest{
		PublicID:      enc.PublicID,
		MetadataToken: enc.MetadataToken,
		BlobToken:     blobKeys.Encoded().BlobToken,
		DeletionToken: enc.DeletionToken,
		EncryptedMeta: encryptedMeta,
		Expiration:    expiration,
		BurnAfterRead: !p.Reusable,
		BlobSize:      plan.TotalSize,
	})
	if err != nil {
		return nil, describeUploadError(err)
	}

	expiresAt, err := upload(ctx, c, session, plan, sources, blobKeys, p.Progress)
	if err != nil {
		// Whatever went wrong, the partial upload is useless: release it.
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = c.AbortUpload(abortCtx, session.ID, session.Token)
		return nil, err
	}

	return &Result{
		Link:              Link{Origin: c.BaseURL, Secret: enc.ShareSecret, DeletionToken: enc.DeletionToken},
		ExpiresAt:         expiresAt,
		Kind:              kind,
		Size:              total,
		Names:             names,
		Reusable:          p.Reusable,
		PasswordProtected: p.Password != "",
	}, nil
}

// upload encrypts the bundle as one stream and sends it in parts of exactly
// the server's part size, the last one shorter, a few at a time. Parts are
// cut wherever chunks begin and end, so their sizes say nothing about the
// files (FORMAT.md section 9). Only a handful of parts are ever in memory.
func upload(ctx context.Context, c *api.Client, session *api.UploadSession, plan *bundle.StreamPlan, sources []bundle.Source, blobKeys *keys.KeySet, progress func(uploaded, total int64)) (time.Time, error) {
	stream, err := bundle.NewEncrypter(plan, sources, blobKeys)
	if err != nil {
		return time.Time{}, err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(UploadConcurrency)
	var (
		mu       sync.Mutex
		uploaded int64
	)
	// Parts finish on their own goroutines; the lock is held through the
	// callback, so a caller's progress code never runs twice at once.
	report := func(n int64) {
		if progress == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		uploaded += n
		progress(uploaded, plan.TotalSize)
	}
	report(0)

	var offset int64
	for number := 1; offset < plan.TotalSize; number++ {
		if err := gctx.Err(); err != nil {
			// A part has failed for good, or the caller gave up: stop
			// encrypting and surface that failure.
			if werr := g.Wait(); werr != nil {
				return time.Time{}, werr
			}
			return time.Time{}, err
		}
		data := make([]byte, min(session.PartSize, plan.TotalSize-offset))
		if _, err := io.ReadFull(stream, data); err != nil {
			_ = g.Wait()
			return time.Time{}, describeSourceError(err)
		}
		partOffset, sum := offset, bundle.SHA256Hex(data)
		g.Go(func() error {
			if err := c.UploadPart(gctx, session.ID, session.Token, number, partOffset, data, sum); err != nil {
				return describeUploadError(err)
			}
			report(int64(len(data)))
			return nil
		})
		offset += int64(len(data))
	}
	if err := g.Wait(); err != nil {
		return time.Time{}, err
	}
	expiresAt, err := c.CompleteUpload(ctx, session.ID, session.Token)
	if err != nil {
		return time.Time{}, describeUploadError(err)
	}
	return expiresAt, nil
}

// Info is what a link reveals without opening the secret: the metadata
// envelope decrypts with the link alone.
type Info struct {
	Kind              Kind
	PasswordProtected bool
	Reusable          bool
	EncryptedSize     int64
	ExpiresAt         time.Time
	CreatedAt         time.Time
	// Opened is whether a recipient has opened a reusable secret. For a
	// one-time secret it is false: opening it ends it.
	Opened bool
}

// Inspect describes a live secret, or returns a *NotFoundError.
func Inspect(ctx context.Context, c *api.Client, link Link) (*Info, error) {
	base, err := keys.FromShareSecret(link.Secret, "")
	if err != nil {
		return nil, err
	}
	enc := base.Encoded()
	meta, err := c.Metadata(ctx, enc.PublicID, enc.MetadataToken)
	if err != nil {
		return nil, describeLookupError(err)
	}
	clientMeta, err := base.DecryptMeta(meta.EncryptedMeta)
	if err != nil {
		return nil, ErrLinkMismatch
	}
	kind := KindFiles
	if clientMeta.Type == "text" {
		kind = KindText
	}
	return &Info{
		Kind:              kind,
		PasswordProtected: clientMeta.PasswordProtected,
		Reusable:          !meta.BurnAfterRead,
		EncryptedSize:     meta.BlobSize,
		ExpiresAt:         meta.ExpiresAt,
		CreatedAt:         meta.CreatedAt,
		Opened:            meta.Opened,
	}, nil
}

// Sink receives what Open decrypts.
type Sink interface {
	// Text receives a text secret.
	Text(text []byte) error
	// File is asked where a file's plaintext should go. The writer is
	// closed when the file is complete.
	File(file bundle.Entry) (io.WriteCloser, error)
}

// ChoosingSink is a Sink that sees the files before anything is decrypted,
// to pick the ones it wants and to check names and sizes while nothing has
// been written yet. Choose returns the indexes of the files to decrypt, nil
// meaning all of them; only those reach File.
type ChoosingSink interface {
	Sink
	Choose(ctx context.Context, files []bundle.Entry) ([]int, error)
}

// Opened is what Open found.
type Opened struct {
	Info Info
	// Version is the bundle's: 3, or 2 for one written before the stream.
	Version int
	// Files lists every file of the bundle, chosen or not.
	Files []bundle.Entry
}

// Open reads a secret, handing the content to sink. Opening a one-time
// secret is what ends it. A password is needed when Info says so; a wrong
// one is ErrWrongPassword. progress, if set, is told the plaintext bytes
// written so far.
func Open(ctx context.Context, c *api.Client, link Link, password string, sink Sink, progress func(done, total int64)) (*Opened, error) {
	info, err := Inspect(ctx, c, link)
	if err != nil {
		return nil, err
	}
	if info.PasswordProtected && password == "" {
		return nil, ErrPasswordRequired
	}
	if !info.PasswordProtected {
		password = ""
	}
	blobKeys, err := keys.FromShareSecret(link.Secret, password)
	if err != nil {
		return nil, err
	}
	enc := blobKeys.Encoded()
	session, err := c.StartRetrievalSession(ctx, enc.PublicID, enc.BlobToken, link.DeletionToken)
	if err != nil {
		switch {
		case api.IsStatus(err, http.StatusForbidden) && info.PasswordProtected:
			return nil, ErrWrongPassword
		case api.IsStatus(err, http.StatusForbidden):
			return nil, ErrLinkMismatch
		case api.IsStatus(err, http.StatusNotFound):
			// It went away between the look and the opening.
			return nil, &NotFoundError{}
		}
		return nil, err
	}

	// Open fetches a small bundle whole and reads everything after from
	// that, so the fetcher needs no cache in front of it.
	fetch := func(ctx context.Context, start, end int64) ([]byte, error) {
		return c.ReadRange(ctx, enc.PublicID, session.Token, start, end)
	}
	b, err := bundle.Open(ctx, fetch, blobKeys, session.BlobSize)
	if err != nil {
		return nil, describeReadError(err)
	}
	opened := &Opened{Info: *info, Version: b.Version, Files: b.Files}

	// A note is the bundle's single file; the envelope says it is one.
	if info.Kind == KindText && len(b.Files) == 1 {
		var buf bytes.Buffer
		track := func(written int64) {
			if progress != nil {
				progress(written, b.Files[0].Size)
			}
		}
		if err := b.DecryptFile(ctx, 0, &buf, track); err != nil {
			return nil, describeReadError(err)
		}
		if err := sink.Text(buf.Bytes()); err != nil {
			return nil, err
		}
		return opened, nil
	}

	var chosen []int
	if cs, ok := sink.(ChoosingSink); ok {
		if chosen, err = cs.Choose(ctx, b.Files); err != nil {
			return nil, err
		}
	}
	total := b.TotalSize()
	if chosen != nil {
		total = 0
		for _, i := range chosen {
			if i >= 0 && i < len(b.Files) {
				total += b.Files[i].Size
			}
		}
	}
	track := func(written int64) {
		if progress != nil {
			progress(written, total)
		}
	}
	if err := b.Decrypt(ctx, chosen, sink.File, track); err != nil {
		return nil, describeReadError(err)
	}
	return opened, nil
}

// Delete removes the secret for everyone. It needs the owner link.
func Delete(ctx context.Context, c *api.Client, link Link) error {
	if !link.IsOwner() {
		return ErrNotOwner
	}
	base, err := keys.FromShareSecret(link.Secret, "")
	if err != nil {
		return err
	}
	enc := base.Encoded()
	if err := c.Delete(ctx, enc.PublicID, enc.MetadataToken, link.DeletionToken); err != nil {
		if api.IsStatus(err, http.StatusForbidden) {
			return ErrLinkMismatch
		}
		return describeLookupError(err)
	}
	return nil
}

func describeLookupError(err error) error {
	if api.IsStatus(err, http.StatusNotFound) {
		return &NotFoundError{}
	}
	if api.IsStatus(err, http.StatusForbidden) {
		return ErrLinkMismatch
	}
	return err
}

func describeReadError(err error) error {
	if api.IsStatus(err, http.StatusForbidden) {
		return errors.New("the download window has closed; open the link again")
	}
	if errors.Is(err, keys.ErrDecrypt) {
		return errors.New("the data does not decrypt with this link: it was damaged in storage or in transit")
	}
	return err
}

// describeSourceError says what went wrong reading the files being shared.
func describeSourceError(err error) error {
	if errors.Is(err, bundle.ErrSourceChanged) {
		return fmt.Errorf("%w; share it again once it stays the same", err)
	}
	return err
}

func describeUploadError(err error) error {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusBadRequest && strings.Contains(apiErr.Message, "maximum size"):
			return ErrTooLarge
		case apiErr.Status == http.StatusTooManyRequests:
			return errors.New("the server is rate limiting this address; wait a minute and try again")
		}
	}
	return err
}
