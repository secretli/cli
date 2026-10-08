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

// liveSecret is a live secret's metadata as the servers send it; extra adds
// fields to it.
func liveSecret(burnAfterRead bool, extra string) string {
	return fmt.Sprintf(`{"encrypted_meta":"v2$abc","blob_size":359,"burn_after_read":%t,"expires_at":"2026-10-08T20:14:56Z","created_at":"2026-10-08T20:09:56Z"%s}`, burnAfterRead, extra)
}

// The server used to tell when a recipient first opened a reusable secret
// (opened_at) and now only tells that one did (opened). The client reads both.
func TestMetadataReadsOpenedFromOlderAndNewerServers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"newer, opened", liveSecret(false, `,"opened":true`), true},
		{"newer, not opened", liveSecret(false, `,"opened":false`), false},
		{"newer, one-time", liveSecret(true, `,"opened":false`), false},
		{"older, opened", liveSecret(false, `,"opened_at":"2026-10-08T20:09:56Z"`), true},
		{"older, not opened", liveSecret(false, ``), false},
		{"older, one-time", liveSecret(true, ``), false},
		{"older, null", liveSecret(false, `,"opened_at":null`), false},
		// Its being there is what counts; the time is never read.
		{"older, a time nobody can read", liveSecret(false, `,"opened_at":"some day"`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := answering(t, http.StatusOK, tc.body).Metadata(context.Background(), "id", "token")
			if err != nil {
				t.Fatal(err)
			}
			if meta.Opened != tc.want {
				t.Errorf("Opened = %t, want %t", meta.Opened, tc.want)
			}
			// Nothing else about the answer depends on it.
			if meta.EncryptedMeta != "v2$abc" || meta.BlobSize != 359 || !meta.ExpiresAt.Equal(time.Date(2026, 10, 8, 20, 14, 56, 0, time.UTC)) || !meta.CreatedAt.Equal(time.Date(2026, 10, 8, 20, 9, 56, 0, time.UTC)) {
				t.Errorf("metadata = %+v", meta)
			}
		})
	}
}

// What the server tells of a secret that is gone used to hold times and who
// opened it, and an expired secret was gone too; now it is the outcome and
// whether the secret was one-time, and an expired secret is not found. Only
// the outcome is needed, and the same answer comes for deleting.
func TestAsGoneReadsOlderAndNewerServers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   *api.Gone // nil: not an answer about a secret that is gone
	}{
		{"newer, opened", 410, `{"error":"secret is gone","details":{"outcome":"opened","burn_after_read":true}}`, &api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"newer, deleted", 410, `{"error":"secret is gone","details":{"outcome":"deleted","burn_after_read":false}}`, &api.Gone{Outcome: "deleted"}},
		{"newer, one-time and deleted", 410, `{"error":"secret is gone","details":{"outcome":"deleted","burn_after_read":true}}`, &api.Gone{Outcome: "deleted", BurnAfterRead: true}},
		{"older, opened by a recipient", 410, `{"error":"secret is gone","details":{"burn_after_read":true,"ended_at":"2026-10-08T20:09:56Z","first_opened_at":"2026-10-08T20:09:56Z","opened_by_owner":false,"outcome":"opened"}}`, &api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"older, opened by its owner", 410, `{"error":"secret is gone","details":{"burn_after_read":true,"ended_at":"2026-10-08T20:09:56Z","opened_by_owner":true,"outcome":"opened"}}`, &api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"older, deleted", 410, `{"error":"secret is gone","details":{"burn_after_read":true,"ended_at":"2026-10-08T20:09:56Z","opened_by_owner":false,"outcome":"deleted"}}`, &api.Gone{Outcome: "deleted", BurnAfterRead: true}},
		{"older, expired", 410, `{"error":"secret is gone","details":{"burn_after_read":false,"ended_at":"2026-10-08T20:09:56Z","first_opened_at":"2026-10-08T20:09:56Z","opened_by_owner":false,"outcome":"expired"}}`, &api.Gone{Outcome: "expired"}},
		{"older, a time nobody can read", 410, `{"error":"secret is gone","details":{"burn_after_read":true,"ended_at":"some day","outcome":"opened"}}`, &api.Gone{Outcome: "opened", BurnAfterRead: true}},
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
