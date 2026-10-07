package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// firstLine waits until the running command printed a whole line on stdout.
func firstLine(t *testing.T, r *running) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if out := r.stdout(); strings.Contains(out, "\n") {
			line, _, _ := strings.Cut(out, "\n")
			return line
		}
		select {
		case code := <-r.done:
			t.Fatalf("exited %d before printing a line; stderr %q", code, r.stderr())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no line on stdout; stderr %q", r.stderr())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func exitOf(t *testing.T, r *running) int {
	t.Helper()
	select {
	case code := <-r.done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("still running")
	}
	return -1
}

func TestSendAndReceiveWithACode(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	_, stderr, code := runCLI(t, "the launch code\n", "share", server)
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	ownerLink := ""
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, srv.URL) {
			ownerLink = line
		}
	}

	// Given the owner link, send hands over only the link to hand out.
	sender := startCLI(t, "", "send", ownerLink)
	transferCode := firstLine(t, sender)
	if !strings.HasPrefix(transferCode, "1-") || strings.Count(transferCode, "-") != 2 {
		t.Fatalf("code = %q", transferCode)
	}
	if !strings.Contains(sender.stderr(), "the owner link stays here") || !strings.Contains(sender.stderr(), "/c and type the code") {
		t.Errorf("send stderr = %q", sender.stderr())
	}

	// Typed loosely, in parts.
	parts := strings.Split(strings.ToUpper(transferCode), "-")
	stdout, stderr, code := runCLI(t, "", "receive", parts[0], parts[1], parts[2], server, "--yes")
	if code != 0 || stdout != "the launch code\n" {
		t.Fatalf("receive: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code := exitOf(t, sender); code != 0 || !strings.Contains(sender.stderr(), "Sent.") {
		t.Errorf("send: exit %d, stderr %q", code, sender.stderr())
	}
}

func TestAWrongCodeFailsOnBothSides(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	link, _, _ := runCLI(t, "secret\n", "share", server, "-q")

	sender := startCLI(t, "", "send", strings.TrimSpace(link))
	transferCode := firstLine(t, sender)
	nameplate, _, _ := strings.Cut(transferCode, "-")
	wrong := nameplate + "-yoyo-zucchini"
	if transferCode == wrong {
		wrong = nameplate + "-acid-rocket"
	}

	_, stderr, code := runCLI(t, "", "receive", wrong, server, "--yes")
	if code != ExitPassword || !strings.Contains(stderr, "the code didn't match; ask for a new code") {
		t.Errorf("receive: exit %d, stderr %q", code, stderr)
	}
	if code := exitOf(t, sender); code != ExitPassword || !strings.Contains(sender.stderr(), "the code didn't match; run send again") {
		t.Errorf("send: exit %d, stderr %q", code, sender.stderr())
	}
	// The secret is still there: nothing was delivered or opened.
	if stdout, _, code := runCLI(t, "", "open", strings.TrimSpace(link), "--yes"); code != 0 || stdout != "secret\n" {
		t.Errorf("open after the mismatch: exit %d, stdout %q", code, stdout)
	}
}

func TestShareWithACodeInJSON(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL

	sender := startCLI(t, "for your eyes\n", "share", "--code", "--json", server)
	var announced struct {
		Code          string    `json:"code"`
		CodeExpiresAt time.Time `json:"code_expires_at"`
		Link          string    `json:"link"`
		OwnerLink     string    `json:"owner_link"`
		Opens         string    `json:"opens"`
	}
	if err := json.Unmarshal([]byte(firstLine(t, sender)), &announced); err != nil {
		t.Fatal(err)
	}
	if announced.Code == "" || announced.OwnerLink != announced.Link+"!"+strings.SplitN(announced.OwnerLink, "!", 2)[1] || announced.Opens != "once" || announced.CodeExpiresAt.IsZero() {
		t.Errorf("announced %+v", announced)
	}

	stdout, stderr, code := runCLI(t, "", "receive", announced.Code, "--link", "--json", server)
	var received struct {
		Link string `json:"link"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &received) != nil || received.Link != announced.Link {
		t.Fatalf("receive: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code := exitOf(t, sender); code != 0 || !strings.HasSuffix(sender.stdout(), "{\"delivered\":true}\n") {
		t.Errorf("share: exit %d, stdout %q", code, sender.stdout())
	}
}

func TestReceiveRefusesBadCodes(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	for _, c := range []struct {
		args []string
		exit int
		says string
	}{
		{[]string{"receive", "7-acid-rocket", server, "--yes"}, ExitGone, "no transfer with that number"},
		{[]string{"receive", "acid-rocket", server, "--yes"}, ExitError, "codes look like 7-acid-rocket"},
		{[]string{"receive", "7-acid-rokcet", server, "--yes"}, ExitError, `"rokcet" isn't a code word`},
		{[]string{"share", "-t", "x", "--code", "--qr", server}, ExitError, "leave out --qr and --copy"},
	} {
		_, stderr, code := runCLI(t, "", c.args...)
		if code != c.exit || !strings.Contains(stderr, c.says) {
			t.Errorf("%v: exit %d, stderr %q", c.args, code, stderr)
		}
	}
	// The code can come from stdin too.
	if _, stderr, code := runCLI(t, "7 acid rocket\n", "receive", server, "--yes"); code != ExitGone {
		t.Errorf("code from stdin: exit %d, stderr %q", code, stderr)
	}
}
