package sharetest

import (
	"context"
	"testing"

	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/bundle"
	"github.com/secretli/format/keys"
	"github.com/secretli/format/link"
)

// ShareStream makes a secret in bundle version 3 through the upload API of
// the server at origin, and returns its owner link. kind is the envelope's
// type, "text" or "bundle".
func ShareStream(t testing.TB, origin, kind string, reusable bool, sources ...bundle.Source) link.Link {
	t.Helper()
	base, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := bundle.NewStreamPlan(sources)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := bundle.EncryptStream(plan, sources, base)
	if err != nil {
		t.Fatal(err)
	}
	return shareBlob(t, origin, kind, reusable, base, blob)
}

// ShareVersion2 makes a secret in bundle version 2, which the client reads
// but no longer writes, like ShareStream.
func ShareVersion2(t testing.TB, origin, kind string, reusable bool, sources ...bundle.Source) link.Link {
	t.Helper()
	base, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name)
	}
	plan, err := bundle.NewPlan(sources, bundle.DefaultBundleName(names))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := bundle.Encrypt(plan, sources, base)
	if err != nil {
		t.Fatal(err)
	}
	return shareBlob(t, origin, kind, reusable, base, blob)
}

// shareBlob uploads a bundle as one part, with an envelope of kind.
func shareBlob(t testing.TB, origin, kind string, reusable bool, base *keys.KeySet, blob []byte) link.Link {
	t.Helper()
	ctx := context.Background()
	meta, err := base.EncryptMeta(keys.Meta{Type: kind})
	if err != nil {
		t.Fatal(err)
	}
	enc := base.Encoded()
	c := api.New(origin)
	session, err := c.StartUpload(ctx, api.UploadRequest{
		PublicID:      enc.PublicID,
		MetadataToken: enc.MetadataToken,
		BlobToken:     enc.BlobToken,
		DeletionToken: enc.DeletionToken,
		EncryptedMeta: meta,
		Expiration:    "1h",
		BurnAfterRead: !reusable,
		BlobSize:      int64(len(blob)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(blob)) > session.PartSize {
		t.Fatalf("a bundle of %d bytes needs more than one part", len(blob))
	}
	if err := c.UploadPart(ctx, session.ID, session.Token, 1, 0, blob, bundle.SHA256Hex(blob)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteUpload(ctx, session.ID, session.Token); err != nil {
		t.Fatal(err)
	}
	return link.Link{Origin: origin, Secret: enc.ShareSecret, DeletionToken: enc.DeletionToken}
}
