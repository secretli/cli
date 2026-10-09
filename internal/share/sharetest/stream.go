package sharetest

import (
	"context"
	"testing"

	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/bundle"
	"github.com/secretli/format/keys"
	"github.com/secretli/format/link"
)

// ShareStream makes a secret in bundle version 3, which the client reads but
// does not write yet, through the upload API of the server at origin, and
// returns its owner link. kind is the envelope's type, "text" or "bundle".
func ShareStream(t testing.TB, origin, kind string, reusable bool, sources ...bundle.Source) link.Link {
	t.Helper()
	ctx := context.Background()
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
		BlobSize:      plan.TotalSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalSize > session.PartSize {
		t.Fatalf("a bundle of %d bytes needs more than one part", plan.TotalSize)
	}
	if err := c.UploadPart(ctx, session.ID, session.Token, 1, 0, blob, bundle.SHA256Hex(blob)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteUpload(ctx, session.ID, session.Token); err != nil {
		t.Fatal(err)
	}
	return link.Link{Origin: origin, Secret: enc.ShareSecret, DeletionToken: enc.DeletionToken}
}
