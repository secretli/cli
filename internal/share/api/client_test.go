package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share/api"
)

// answering is a server that gives every request this status and JSON body.
func answering(t *testing.T, status int, body string) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return api.New(srv.URL)
}

// liveSecret is a live secret's metadata as the server sends it.
func liveSecret(burnAfterRead, opened bool) string {
	return fmt.Sprintf(`{"encrypted_meta":"v2$abc","blob_size":359,"burn_after_read":%t,"expires_at":"2026-10-08T20:14:56Z","created_at":"2026-10-08T20:09:56Z","opened":%t}`, burnAfterRead, opened)
}

// The server tells that a recipient opened a reusable secret, not when.
func TestMetadataReadsOpened(t *testing.T) {
	cases := []struct {
		name          string
		burnAfterRead bool
		opened        bool
	}{
		{"reusable and opened", false, true},
		{"reusable and not opened", false, false},
		{"one-time", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := answering(t, http.StatusOK, liveSecret(tc.burnAfterRead, tc.opened)).Metadata(context.Background(), "id", "token")
			if err != nil {
				t.Fatal(err)
			}
			if meta.Opened != tc.opened || meta.BurnAfterRead != tc.burnAfterRead {
				t.Errorf("Opened = %t, BurnAfterRead = %t, want %t, %t", meta.Opened, meta.BurnAfterRead, tc.opened, tc.burnAfterRead)
			}
			// Nothing else about the answer depends on it.
			if meta.EncryptedMeta != "v2$abc" || meta.BlobSize != 359 || !meta.ExpiresAt.Equal(time.Date(2026, 10, 8, 20, 14, 56, 0, time.UTC)) || !meta.CreatedAt.Equal(time.Date(2026, 10, 8, 20, 9, 56, 0, time.UTC)) {
				t.Errorf("metadata = %+v", meta)
			}
		})
	}
}
