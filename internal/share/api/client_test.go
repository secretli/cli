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

// What the server tells of a secret that is gone is its outcome and whether it
// was one-time, and the same answer comes for deleting. An expired secret is
// not found, which says nothing about it.
func TestAsGoneReadsTheStoryOfASecretThatIsGone(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   *api.Gone // nil: not an answer about a secret that is gone
	}{
		{"opened", 410, `{"error":"secret is gone","details":{"outcome":"opened","burn_after_read":true}}`, &api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"deleted", 410, `{"error":"secret is gone","details":{"outcome":"deleted","burn_after_read":false}}`, &api.Gone{Outcome: "deleted"}},
		{"one-time and deleted", 410, `{"error":"secret is gone","details":{"outcome":"deleted","burn_after_read":true}}`, &api.Gone{Outcome: "deleted", BurnAfterRead: true}},
		{"an outcome it has no word for", 410, `{"error":"secret is gone","details":{"outcome":"vanished","burn_after_read":false}}`, &api.Gone{Outcome: "vanished"}},
		{"no word on whether it was one-time", 410, `{"error":"secret is gone","details":{"outcome":"opened"}}`, &api.Gone{Outcome: "opened"}},
		{"no outcome", 410, `{"error":"secret is gone","details":{"burn_after_read":true}}`, nil},
		{"no details", 410, `{"error":"secret is gone"}`, nil},
		{"not found", 404, `{"error":"secret not found"}`, nil},
	}
	ctx := context.Background()
	calls := map[string]func(c *api.Client) error{
		"metadata": func(c *api.Client) error { _, err := c.Metadata(ctx, "id", "token"); return err },
		"delete":   func(c *api.Client) error { return c.Delete(ctx, "id", "token", "deletion-token") },
	}
	for _, tc := range cases {
		for name, call := range calls {
			t.Run(tc.name+", "+name, func(t *testing.T) {
				err := call(answering(t, tc.status, tc.body))
				gone, ok := api.AsGone(err)
				switch {
				case tc.want == nil && ok:
					t.Errorf("AsGone = %+v, want no answer about a secret that is gone", gone)
				case tc.want != nil && (!ok || *gone != *tc.want):
					t.Errorf("AsGone = %+v, %t, want %+v", gone, ok, tc.want)
				}
			})
		}
	}
}
