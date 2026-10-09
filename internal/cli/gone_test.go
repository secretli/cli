package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/format/keys"
)

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

// The owner link shows whether a reusable secret has been opened. A one-time
// secret that was opened is gone, and the server keeps nothing that could
// tell, so the note promises nothing about it.
func TestOwnerNoteSpeaksOfOpeningForAReusableSecretOnly(t *testing.T) {
	if got := ownerNote(&share.Result{Reusable: true}); !strings.HasSuffix(got, "it can delete the secret and shows whether it has been opened.") {
		t.Errorf("reusable: %q", got)
	}
	if got := ownerNote(&share.Result{}); strings.Contains(got, "opened") || !strings.HasSuffix(got, "it can delete the secret.") {
		t.Errorf("one-time: %q", got)
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
	meta, err := base.EncryptMeta(keys.Meta{Type: "text"})
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

// status reads whether a live secret was opened, and prints no time of the
// opening: the server keeps none.
func TestStatusSaysWhetherASecretWasOpenedWithoutATime(t *testing.T) {
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

// The server keeps nothing about a secret that is gone: an opened one-time
// secret, a deleted one and an expired one get the same plain 404 as a link
// to nothing, and the server cannot tell why. status says the secret is gone
// and nothing more, to the owner and a recipient alike, with the exit code 4,
// so that a script can always count on the state being live or gone. The
// other commands fail with an error that says the same, and the same code.
func TestALinkTheServerHasNoRecordOfIsGone(t *testing.T) {
	srv := fakeServer(t)
	// shared makes a one-time text secret and returns its two links.
	shared := func(t *testing.T) (owner, recipient string) {
		t.Helper()
		stdout, stderr, code := runCLI(t, "the launch code\n", "share", "--server="+srv.URL, "--json")
		var links struct {
			Link      string `json:"link"`
			OwnerLink string `json:"owner_link"`
		}
		if err := json.Unmarshal([]byte(stdout), &links); err != nil || code != 0 {
			t.Fatalf("share: exit %d, %v, stdout %q, stderr %q", code, err, stdout, stderr)
		}
		return links.OwnerLink, links.Link
	}
	cases := []struct {
		name  string
		links func(t *testing.T) (owner, recipient string)
	}{
		{"an opened one-time secret", func(t *testing.T) (string, string) {
			owner, recipient := shared(t)
			if stdout, stderr, code := runCLI(t, "", "open", recipient, "--yes"); code != 0 || stdout != "the launch code\n" {
				t.Fatalf("open: exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			return owner, recipient
		}},
		{"a deleted secret", func(t *testing.T) (string, string) {
			owner, recipient := shared(t)
			if _, stderr, code := runCLI(t, "", "delete", owner, "--yes"); code != 0 {
				t.Fatalf("delete: exit %d, stderr %q", code, stderr)
			}
			return owner, recipient
		}},
		{"an expired secret", func(t *testing.T) (string, string) {
			fake := srv.Fake()
			if fake == nil {
				t.Skip("a real server cannot be made to expire its secrets")
			}
			owner, recipient := shared(t)
			fake.Expire()
			return owner, recipient
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

			// open and delete are errors still: on stderr, and as JSON the error
			// and its code, with nothing more to tell.
			const notFound = "secretli: this secret is gone: it may have expired, been opened or been deleted\n"
			for _, args := range [][]string{{"open", recipient, "--yes"}, {"open", owner, "--yes"}, {"delete", owner, "--yes"}} {
				if stdout, stderr, code := runCLI(t, "", args...); code != ExitGone || stdout != "" || stderr != notFound {
					t.Errorf("%s: exit %d, stdout %q, stderr %q", args[0], code, stdout, stderr)
				}
			}
			for _, args := range [][]string{{"open", recipient, "--json"}, {"open", owner, "--json"}, {"delete", owner, "--yes", "--json"}} {
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
