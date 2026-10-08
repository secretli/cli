package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/cli/internal/share/api"
	"github.com/secretli/format/keys"
)

func TestGoneSentences(t *testing.T) {
	opened := api.Gone{Outcome: "opened", BurnAfterRead: true}
	deleted := api.Gone{Outcome: "deleted"}
	expired := api.Gone{Outcome: "expired"} // only older servers say so
	cases := []struct {
		gone  api.Gone
		owner bool
		want  string
	}{
		{opened, true, "Your secret was opened. It was a one-time secret, so nothing is left on the server."},
		{opened, false, "This secret was already opened. If that wasn't you, tell the sender: the link may have reached someone else."},
		{deleted, true, "You deleted this secret."},
		{deleted, false, "The sender deleted this secret. Ask them for a new link if you still need it."},
		{expired, true, "Your secret expired. Nothing is left on the server."},
		{expired, false, "This secret expired. Nothing is left on the server, so ask the sender for a new link if you still need it."},
		{api.Gone{Outcome: "vanished"}, false, "This secret is gone (vanished)."},
	}
	for _, tc := range cases {
		if got := goneSentence(tc.gone, tc.owner); got != tc.want {
			t.Errorf("goneSentence(%+v, owner=%t) = %q, want %q", tc.gone, tc.owner, got, tc.want)
		}
	}
}

func TestDescribeInfoSaysWhetherAReusableSecretWasOpenedToItsOwnerOnly(t *testing.T) {
	now := time.Now()
	info := func(reusable, opened bool) *share.Info {
		return &share.Info{Kind: share.KindText, Reusable: reusable, Opened: opened, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	cases := []struct {
		name  string
		info  *share.Info
		owner bool
		want  string // empty: nothing about being opened
	}{
		{"owner, opened", info(true, true), true, "It has been opened."},
		{"owner, not opened", info(true, false), true, "Nobody has opened it yet."},
		{"recipient, opened", info(true, true), false, ""},
		{"owner of a one-time secret", info(false, false), true, ""},
	}
	for _, tc := range cases {
		got := describeInfo(tc.info, tc.owner, now)
		if tc.want != "" && !strings.HasSuffix(got, " "+tc.want) {
			t.Errorf("%s: %q does not end with %q", tc.name, got, tc.want)
		}
		if tc.want == "" && (strings.Contains(got, "opened") || strings.Contains(got, "Opened")) {
			t.Errorf("%s: %q says something about being opened", tc.name, got)
		}
	}
}

// stubbed starts a server that answers every request with this status and
// JSON body, where META stands for the encrypted metadata of the text secret
// the two links returned point to: the owner link and the recipient link.
func stubbed(t *testing.T, status int, body string) (owner, recipient string) {
	t.Helper()
	base, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := base.EncryptMeta(keys.Meta{Type: "text", BundleName: "secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	answer := strings.ReplaceAll(body, "META", meta)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	enc := base.Encoded()
	l := share.Link{Origin: srv.URL, Secret: enc.ShareSecret, DeletionToken: enc.DeletionToken}
	return l.String(), l.Recipient().String()
}

func liveAnswer(burnAfterRead bool, extra string) string {
	return fmt.Sprintf(`{"encrypted_meta":"META","blob_size":359,"burn_after_read":%t,"expires_at":"2026-10-09T20:14:56Z","created_at":"2026-10-08T20:09:56Z"%s}`, burnAfterRead, extra)
}

// The command reads the answers of the server as it was, with times and
// names, and as it is now, whichever server the other tests run against: the
// live ones first, then the 410 answers. Either way it prints no time.
func TestStatusReadsTheAnswersOfOlderAndNewerServers(t *testing.T) {
	live := []struct {
		name   string
		body   string
		opened bool
	}{
		{"older, reusable and opened", liveAnswer(false, `,"opened_at":"2026-10-08T20:11:00Z"`), true},
		{"older, reusable and not opened", liveAnswer(false, ``), false},
		{"older, one-time", liveAnswer(true, ``), false},
		{"newer, reusable and opened", liveAnswer(false, `,"opened":true`), true},
		{"newer, reusable and not opened", liveAnswer(false, `,"opened":false`), false},
		{"newer, one-time", liveAnswer(true, `,"opened":false`), false},
	}
	for _, tc := range live {
		t.Run(tc.name, func(t *testing.T) {
			owner, _ := stubbed(t, http.StatusOK, tc.body)

			stdout, stderr, code := runCLI(t, "", "status", owner, "--json")
			var status map[string]any
			if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != 0 || status["state"] != "live" || status["opened"] != tc.opened {
				t.Fatalf("status --json: exit %d, %v, stdout %q, stderr %q", code, err, stdout, stderr)
			}
			if _, ok := status["opened_at"]; ok {
				t.Errorf("status --json tells when it was opened: %q", stdout)
			}

			want := "Not opened yet.\n"
			if tc.opened {
				want = "It has been opened.\n"
			}
			stdout, stderr, code = runCLI(t, "", "status", owner)
			if code != 0 || !strings.HasSuffix(stdout, "\n"+want) || strings.Contains(stdout, "First opened") {
				t.Errorf("status: exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
		})
	}

	// The 410 answers as the real server gave them, and as it gives them
	// now: only the outcome and whether the secret was one-time are read.
	gone := []struct {
		name    string
		details string
		gone    api.Gone
	}{
		{"older, opened by a recipient", `{"burn_after_read":true,"ended_at":"2026-10-08T20:09:56Z","first_opened_at":"2026-10-08T20:09:56Z","opened_by_owner":false,"outcome":"opened"}`, api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"older, opened by its owner", `{"burn_after_read":true,"ended_at":"2026-10-08T20:09:56Z","opened_by_owner":true,"outcome":"opened"}`, api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"older, deleted after being opened", `{"burn_after_read":false,"ended_at":"2026-10-08T20:09:56Z","first_opened_at":"2026-10-08T20:09:56Z","opened_by_owner":false,"outcome":"deleted"}`, api.Gone{Outcome: "deleted"}},
		{"older, expired", `{"burn_after_read":false,"ended_at":"2026-10-08T20:09:56Z","first_opened_at":"2026-10-08T20:09:56Z","opened_by_owner":false,"outcome":"expired"}`, api.Gone{Outcome: "expired"}},
		{"newer, opened", `{"outcome":"opened","burn_after_read":true}`, api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"newer, deleted", `{"outcome":"deleted","burn_after_read":false}`, api.Gone{Outcome: "deleted"}},
	}
	digit := regexp.MustCompile(`\d`)
	for _, tc := range gone {
		t.Run(tc.name, func(t *testing.T) {
			owner, recipient := stubbed(t, http.StatusGone, `{"error":"secret is gone","details":`+tc.details+`}`)

			for who, link := range map[string]string{"owner": owner, "recipient": recipient} {
				stdout, stderr, code := runCLI(t, "", "status", link, "--json")
				want := map[string]any{"state": "gone", "outcome": tc.gone.Outcome, "one_time": tc.gone.BurnAfterRead}
				var status map[string]any
				if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != ExitGone || !reflect.DeepEqual(status, want) {
					t.Errorf("%s: status --json: exit %d, %v, stdout %q, stderr %q", who, code, err, stdout, stderr)
				}

				stdout, stderr, code = runCLI(t, "", "status", link)
				sentence := goneSentence(tc.gone, who == "owner")
				if code != ExitGone || stdout != sentence+"\n" || digit.MatchString(stdout) {
					t.Errorf("%s: status: exit %d, stdout %q, stderr %q, want %q without a time", who, code, stdout, stderr, sentence)
				}
			}

			// Any other command fails the same way, and as JSON says the same.
			stdout, _, code := runCLI(t, "", "open", recipient, "--json")
			var failure map[string]any
			if err := json.Unmarshal([]byte(stdout), &failure); err != nil || code != ExitGone || failure["outcome"] != tc.gone.Outcome || len(failure) != 3 {
				t.Errorf("open --json: exit %d, %v, stdout %q, want the error, the code and the outcome", code, err, stdout)
			}
		})
	}
}

func TestOwnerCommandsOnAGoneSecretSpeakToTheOwner(t *testing.T) {
	owner, recipient := stubbed(t, http.StatusGone, `{"error":"secret is gone","details":{"outcome":"deleted","burn_after_read":false}}`)

	for _, args := range [][]string{{"open", owner}, {"delete", owner, "--yes"}} {
		_, stderr, code := runCLI(t, "", args...)
		if code != ExitGone || !strings.Contains(stderr, "You deleted this secret.") {
			t.Errorf("%s with the owner link: exit %d, stderr %q, want the owner's sentence", args[0], code, stderr)
		}
	}
	_, stderr, code := runCLI(t, "", "open", recipient)
	if code != ExitGone || !strings.Contains(stderr, "The sender deleted this secret.") {
		t.Errorf("open with the recipient's link: exit %d, stderr %q, want the recipient's sentence", code, stderr)
	}
}

func TestAnExpiredSecretIsJustNotFound(t *testing.T) {
	srv := fakeServer(t)
	fake := srv.Fake()
	if fake == nil {
		t.Skip("a real server cannot be made to expire its secrets")
	}
	server := "--server=" + srv.URL

	stdout, stderr, code := runCLI(t, "the launch code\n", "share", server, "--json")
	var shared struct {
		Link      string `json:"link"`
		OwnerLink string `json:"owner_link"`
	}
	if err := json.Unmarshal([]byte(stdout), &shared); err != nil || code != 0 {
		t.Fatalf("share: exit %d, %v, stdout %q, stderr %q", code, err, stdout, stderr)
	}
	fake.Expire()

	// Not found, like a link to nothing, and the same for every command.
	const notFound = "secretli: this secret is gone: it may have expired, been opened or been deleted\n"
	for _, args := range [][]string{{"status", shared.OwnerLink}, {"open", shared.Link, "--yes"}, {"delete", shared.OwnerLink, "--yes"}} {
		if stdout, stderr, code := runCLI(t, "", args...); code != ExitGone || stdout != "" || stderr != notFound {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", args[0], code, stdout, stderr)
		}
	}
}
