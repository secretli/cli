package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share"
)

// noClipboard is a machine without a clipboard tool.
type noClipboard struct{}

var errNoClipboard = errors.New("no clipboard tool found (test)")

func (noClipboard) Available() error       { return errNoClipboard }
func (noClipboard) Copy(string) error      { return errNoClipboard }
func (noClipboard) Paste() (string, error) { return "", errNoClipboard }
func (noClipboard) Clear() error           { return errNoClipboard }

// memClipboard is a clipboard in memory.
type memClipboard struct {
	mu          sync.Mutex
	text        string
	copyErr     error
	pasteSuffix string // what the paste tool adds, as PowerShell adds a line break
	cleared     int
	clearers    []clearer
}

type clearer struct {
	hash  string
	after time.Duration
}

func (m *memClipboard) Available() error { return nil }

func (m *memClipboard) Copy(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.copyErr != nil {
		return m.copyErr
	}
	m.text = text
	return nil
}

func (m *memClipboard) Paste() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.text + m.pasteSuffix, nil
}

func (m *memClipboard) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text = ""
	m.cleared++
	return nil
}

func (m *memClipboard) state() (string, []clearer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.text, append([]clearer(nil), m.clearers...)
}

// fakeClipboard gives the commands a clipboard in memory, and records the
// clearers they would start, until the test ends.
func fakeClipboard(t *testing.T) *memClipboard {
	t.Helper()
	m := &memClipboard{}
	clipboard = m
	startClipboardClearer = func(hash string, after time.Duration) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.clearers = append(m.clearers, clearer{hash: hash, after: after})
		return nil
	}
	t.Cleanup(func() {
		clipboard = noClipboard{}
		startClipboardClearer = func(string, time.Duration) error { return errors.New("not in tests") }
	})
	return m
}

func TestOpenCopiesTheSecretInsteadOfPrintingIt(t *testing.T) {
	srv := fakeServer(t)
	cb := fakeClipboard(t)
	link, _ := shareText(t, "--server="+srv.URL, "hunter2\n")

	stdout, stderr, code := runCLI(t, "", "open", link, "--copy", "--yes")
	if code != 0 || stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Without the final line break, so pasting a password does not press Enter.
	text, clearers := cb.state()
	if text != "hunter2" {
		t.Errorf("clipboard = %q", text)
	}
	// The clearer gets the hash, never the secret.
	if len(clearers) != 1 || clearers[0].hash != clipHash("hunter2") || clearers[0].after != 45*time.Second {
		t.Errorf("clearers = %+v", clearers)
	}
	if !strings.Contains(stderr, "Copied the secret to the clipboard; it is cleared in 45 seconds.") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestCopyIsRefusedBeforeAnythingIsUsedUp(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	cb := fakeClipboard(t)
	link, _ := shareText(t, server, "only once\n")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--stdout"}, "--stdout writes a file and --copy copies text"},
		{[]string{"--json"}, "--json prints the secret; leave out --copy"},
	} {
		// With --json, the error is JSON on stdout.
		stdout, stderr, code := runCLI(t, "", append([]string{"open", link, "--copy", "--yes"}, tc.args...)...)
		if code != ExitError || !strings.Contains(stdout+stderr, tc.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", tc.args, code, stdout, stderr)
		}
	}

	// No clipboard tool: refused before the link is even read.
	clipboard = noClipboard{}
	if _, stderr, code := runCLI(t, "", "open", link, "--copy", "--yes"); code != ExitError || !strings.Contains(stderr, "--copy: no clipboard tool found") {
		t.Errorf("no clipboard: exit %d, stderr %q", code, stderr)
	}
	clipboard = cb

	// Files: refused once the link says what it holds, before it is opened.
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink, _ := shareText(t, server, "", notes)
	if _, stderr, code := runCLI(t, "", "open", fileLink, "--copy", "--yes"); code != ExitError || !strings.Contains(stderr, "--copy is for text, and this secret holds files") {
		t.Errorf("files: exit %d, stderr %q", code, stderr)
	}

	// Both still open.
	if stdout, _, code := runCLI(t, "", "open", link, "--yes"); code != 0 || stdout != "only once\n" {
		t.Errorf("text afterwards: exit %d, stdout %q", code, stdout)
	}
	if _, stderr, code := runCLI(t, "", "open", fileLink, "--yes", "--out", filepath.Join(dir, "received")); code != 0 {
		t.Errorf("files afterwards: exit %d, stderr %q", code, stderr)
	}
}

func TestACopyThatFailsPrintsTheSecretAfterAll(t *testing.T) {
	srv := fakeServer(t)
	cb := fakeClipboard(t)
	cb.copyErr = errors.New("xclip: exit status 1")
	link, _ := shareText(t, "--server="+srv.URL, "hunter2\n")

	// The one-time secret is used up by then, so this is the only copy.
	stdout, stderr, code := runCLI(t, "", "open", link, "--copy", "--yes")
	if code != 0 || stdout != "hunter2\n" || !strings.Contains(stderr, "Couldn't copy to the clipboard (xclip: exit status 1), so here it is:") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestReceiveCopies(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	cb := fakeClipboard(t)

	receive := func(link string, args ...string) (string, string, int) {
		t.Helper()
		sender := startCLI(t, "", "send", link)
		transferCode := firstLine(t, sender)
		stdout, stderr, code := runCLI(t, "", append([]string{"receive", transferCode, server}, args...)...)
		if c := exitOf(t, sender); c != 0 {
			t.Errorf("send: exit %d, stderr %q", c, sender.stderr())
		}
		return stdout, stderr, code
	}

	link, _ := shareText(t, server, "handed over\n")
	if stdout, stderr, code := receive(link, "--copy", "--yes"); code != 0 || stdout != "" {
		t.Errorf("text: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if text, _ := cb.state(); text != "handed over" {
		t.Errorf("clipboard after text = %q", text)
	}

	// With --link, the link is what goes on the clipboard.
	link, _ = shareText(t, server, "a link\n")
	if stdout, stderr, code := receive(link, "--copy", "--link"); code != 0 || stdout != "" || !strings.Contains(stderr, "Copied the link") {
		t.Errorf("link: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if text, _ := cb.state(); text != link {
		t.Errorf("clipboard after --link = %q, want %q", text, link)
	}

	// Files: the code is used up by the time that is known, so the link is
	// printed rather than lost, and it still opens.
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink, _ := shareText(t, server, "", notes)
	stdout, stderr, code := receive(fileLink, "--copy", "--yes")
	if code != ExitError || strings.TrimSpace(stdout) != fileLink || !strings.Contains(stderr, "Not opened: --copy is for text") || !strings.Contains(stderr, "Here is the link, which still works") {
		t.Errorf("files: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, stderr, code := runCLI(t, "", "open", fileLink, "--yes", "--out", filepath.Join(dir, "received")); code != 0 {
		t.Errorf("files afterwards: exit %d, stderr %q", code, stderr)
	}
}

func TestTheClipboardIsClearedOnlyIfItStillHoldsTheSecret(t *testing.T) {
	cb := fakeClipboard(t)
	hash := clipHash("hunter2")

	cb.text = "hunter2"
	if err := clearClipboardAfter(context.Background(), hash, 0); err != nil || cb.text != "" || cb.cleared != 1 {
		t.Errorf("still the secret: err %v, clipboard %q, cleared %d", err, cb.text, cb.cleared)
	}

	// Something else was copied in the meantime: left alone.
	cb.text = "something else"
	if err := clearClipboardAfter(context.Background(), hash, 0); err != nil || cb.text != "something else" || cb.cleared != 1 {
		t.Errorf("something else: err %v, clipboard %q, cleared %d", err, cb.text, cb.cleared)
	}

	// A paste tool that adds a line break still finds the secret.
	cb.text, cb.pasteSuffix = "hunter2", "\r\n"
	if err := clearClipboardAfter(context.Background(), hash, 0); err != nil || cb.text != "" || cb.cleared != 2 {
		t.Errorf("with a line break: err %v, clipboard %q, cleared %d", err, cb.text, cb.cleared)
	}

	// Asked to stop early, it clears right away instead of waiting.
	cb.text, cb.pasteSuffix = "hunter2", ""
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := clearClipboardAfter(ctx, hash, time.Hour); err != nil || cb.text != "" || time.Since(start) > time.Second {
		t.Errorf("stopped early: err %v, clipboard %q, took %s", err, cb.text, time.Since(start))
	}
}

func TestWithoutFinalLineBreak(t *testing.T) {
	for in, want := range map[string]string{
		"hunter2":         "hunter2",
		"hunter2\n":       "hunter2",
		"hunter2\r\n":     "hunter2",
		"two\nlines\n":    "two\nlines",
		"blank line\n\n":  "blank line\n",
		"":                "",
		"\n":              "",
		"no break at end": "no break at end",
	} {
		if got := withoutFinalLineBreak(in); got != want {
			t.Errorf("withoutFinalLineBreak(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTabCompletion(t *testing.T) {
	// --expires completes to the lifetimes the server takes, and not to files.
	stdout, _, code := runCLI(t, "", "__complete", "share", "--expires", "")
	if code != 0 || !strings.HasSuffix(strings.TrimSpace(stdout), ":4") {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	for _, e := range share.Expirations {
		if !strings.Contains(stdout, e+"\n") {
			t.Errorf("%s missing from %q", e, stdout)
		}
	}

	// Links and codes are not files.
	for _, cmd := range []string{"open", "status", "delete", "send", "receive"} {
		if stdout, _, _ := runCLI(t, "", "__complete", cmd, ""); !strings.HasSuffix(strings.TrimSpace(stdout), ":4") {
			t.Errorf("%s: %q", cmd, stdout)
		}
	}

	// The command behind --copy's clearing stays out of sight.
	if stdout, _, _ := runCLI(t, "", "--help"); strings.Contains(stdout, "clear-clipboard") {
		t.Errorf("help lists clear-clipboard: %q", stdout)
	}
}
