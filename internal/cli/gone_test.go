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
	cases := []struct {
		gone  api.Gone
		owner bool
		want  string
	}{
		{opened, true, "Your secret was opened. It was a one-time secret, so nothing is left on the server."},
		{opened, false, "This secret was already opened. If that wasn't you, tell the sender: the link may have reached someone else."},
		{deleted, true, "You deleted this secret."},
		{deleted, false, "The sender deleted this secret. Ask them for a new link if you still need it."},
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

func liveAnswer(burnAfterRead, opened bool) string {
	return fmt.Sprintf(`{"encrypted_meta":"META","blob_size":359,"burn_after_read":%t,"expires_at":"2026-10-09T20:14:56Z","created_at":"2026-10-08T20:09:56Z","opened":%t}`, burnAfterRead, opened)
}

// The command reads the answers of the server, the live ones first, then the
// 410 answers. It prints no time of an opening or a deletion: there is none.
func TestStatusTellsWhatBecameOfASecretWithoutTimes(t *testing.T) {
	live := []struct {
		name          string
		burnAfterRead bool
		opened        bool
	}{
		{"reusable and opened", false, true},
		{"reusable and not opened", false, false},
		{"one-time", true, false},
	}
	for _, tc := range live {
		t.Run(tc.name, func(t *testing.T) {
			owner, _ := stubbed(t, http.StatusOK, liveAnswer(tc.burnAfterRead, tc.opened))

			stdout, stderr, code := runCLI(t, "", "status", owner, "--json")
			var status map[string]any
			if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != 0 || status["state"] != "live" || status["opened"] != tc.opened || status["reusable"] != !tc.burnAfterRead {
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

	// The 410 answers: the outcome and whether the secret was one-time. An
	// outcome the command has no sentence for is still told as gone.
	gone := []struct {
		name    string
		details string
		gone    api.Gone
	}{
		{"opened", `{"outcome":"opened","burn_after_read":true}`, api.Gone{Outcome: "opened", BurnAfterRead: true}},
		{"deleted", `{"outcome":"deleted","burn_after_read":false}`, api.Gone{Outcome: "deleted"}},
		{"deleted, one-time", `{"outcome":"deleted","burn_after_read":true}`, api.Gone{Outcome: "deleted", BurnAfterRead: true}},
		{"an outcome it has no word for", `{"outcome":"vanished","burn_after_read":false}`, api.Gone{Outcome: "vanished"}},
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

	// Not found, like a link to nothing, and the same for the commands that
	// open or delete it. status tells it as a state, which has its own test.
	const notFound = "secretli: this secret is gone: it may have expired, been opened or been deleted\n"
	for _, args := range [][]string{{"open", shared.Link, "--yes"}, {"delete", shared.OwnerLink, "--yes"}} {
		if stdout, stderr, code := runCLI(t, "", args...); code != ExitGone || stdout != "" || stderr != notFound {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", args[0], code, stdout, stderr)
		}
	}
}

// unknownLink is a link to a secret that was never uploaded to this server.
func unknownLink(t *testing.T, origin string) (owner, recipient string) {
	t.Helper()
	base, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	enc := base.Encoded()
	l := share.Link{Origin: origin, Secret: enc.ShareSecret, DeletionToken: enc.DeletionToken}
	return l.String(), l.Recipient().String()
}

// An expired secret and a link to nothing get the same plain 404, and the
// server cannot tell why. status says the secret is gone and nothing more,
// with the exit code 4, so that a script can always count on the state being
// live or gone. The other commands keep failing with an error.
func TestStatusOfALinkTheServerHasNoRecordOfIsGone(t *testing.T) {
	srv := fakeServer(t)
	cases := []struct {
		name  string
		links func(t *testing.T) (owner, recipient string)
	}{
		{"an expired secret", func(t *testing.T) (string, string) {
			fake := srv.Fake()
			if fake == nil {
				t.Skip("a real server cannot be made to expire its secrets")
			}
			stdout, stderr, code := runCLI(t, "the launch code\n", "share", "--server="+srv.URL, "--json")
			var shared struct {
				Link      string `json:"link"`
				OwnerLink string `json:"owner_link"`
			}
			if err := json.Unmarshal([]byte(stdout), &shared); err != nil || code != 0 {
				t.Fatalf("share: exit %d, %v, stdout %q, stderr %q", code, err, stdout, stderr)
			}
			fake.Expire()
			return shared.OwnerLink, shared.Link
		}},
		{"a link to nothing", func(t *testing.T) (string, string) { return unknownLink(t, srv.URL) }},
		{"a plain 404", func(t *testing.T) (string, string) {
			return stubbed(t, http.StatusNotFound, `{"error":"secret not found"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, recipient := tc.links(t)

			for who, link := range map[string]string{"owner": owner, "recipient": recipient} {
				stdout, stderr, code := runCLI(t, "", "status", link, "--json")
				if code != ExitGone || stdout != "{\"state\":\"gone\"}\n" || stderr != "" {
					t.Errorf("%s: status --json: exit %d, stdout %q, stderr %q, want the state alone", who, code, stdout, stderr)
				}

				stdout, stderr, code = runCLI(t, "", "status", link)
				if code != ExitGone || stdout != "This secret is gone: it may have expired, been opened or been deleted.\n" || stderr != "" {
					t.Errorf("%s: status: exit %d, stdout %q, stderr %q", who, code, stdout, stderr)
				}
			}

			// open and delete are errors still, as JSON too: the error and its
			// code, and no state, since there is no outcome either.
			for _, args := range [][]string{{"open", recipient, "--json"}, {"delete", owner, "--yes", "--json"}} {
				stdout, _, code := runCLI(t, "", args...)
				var failure map[string]any
				if err := json.Unmarshal([]byte(stdout), &failure); err != nil || code != ExitGone || len(failure) != 2 ||
					failure["code"] != float64(ExitGone) || failure["error"] != "this secret is gone: it may have expired, been opened or been deleted" {
					t.Errorf("%s --json: exit %d, %v, stdout %q, want the error and the code alone", args[0], code, err, stdout)
				}
			}
		})
	}
}
