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

	stdout, stderr, code := runCLI(t, "", "open", link, "--clipboard", "--yes")
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

// share --clipboard puts the link on the clipboard instead of printing it,
// and has it cleared like the secret: the link carries the key. A pipe is
// still what is shared; the clipboard stands in only for the prompt.
func TestShareCopiesTheLinkInsteadOfPrintingIt(t *testing.T) {
	srv := fakeServer(t)
	cb := fakeClipboard(t)
	cb.text = "not this"

	stdout, stderr, code := runCLI(t, "the launch code", "share", "--server="+srv.URL, "-c")
	if code != 0 || stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	link, clearers := cb.state()
	if !strings.Contains(link, "/s#") || strings.Contains(link, "!") {
		t.Fatalf("clipboard = %q, want the link to hand out", link)
	}
	if len(clearers) != 1 || clearers[0].hash != clipHash(link) || clearers[0].after != 45*time.Second {
		t.Errorf("clearers = %+v", clearers)
	}
	// The owner link exists nowhere else, so it is still printed.
	if !strings.Contains(stderr, "Copied the link to the clipboard; it is cleared in 45 seconds.") || !strings.Contains(stderr, "Owner link") || strings.Contains(stderr, link+"\n") {
		t.Errorf("stderr = %q", stderr)
	}
	if stdout, _, code := runCLI(t, "", "open", link, "--yes"); code != 0 || stdout != "the launch code" {
		t.Errorf("open: exit %d, stdout %q", code, stdout)
	}

	// Files are shared as files; only the link goes on the clipboard.
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, code := runCLI(t, "", "share", "--server="+srv.URL, file, "-c"); code != 0 || stdout != "" {
		t.Errorf("file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if text, _ := cb.state(); !strings.Contains(text, "/s#") || text == link {
		t.Errorf("clipboard after a file = %q", text)
	}
}

// An empty pipe, as from a command that printed nothing, is an error, never
// a reason to share whatever is on the clipboard.
func TestShareNeverFallsBackToTheClipboard(t *testing.T) {
	srv := fakeServer(t)
	cb := fakeClipboard(t)
	cb.text = "the clipboard"

	if _, stderr, code := runCLI(t, "", "share", "--server="+srv.URL, "-c"); code == 0 || !strings.Contains(stderr, share.ErrNothingToShare.Error()) {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if text, clearers := cb.state(); text != "the clipboard" || len(clearers) != 0 {
		t.Errorf("clipboard = %q, clearers %+v", text, clearers)
	}
}

func TestClipboardIsRefusedBeforeAnythingIsUsedUp(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	fakeClipboard(t)
	link, owner := shareText(t, server, "only once\n")

	// No clipboard tool: refused before anything is read or uploaded.
	clipboard = noClipboard{}
	for _, args := range [][]string{
		{"share", server, "-t", "x", "-c"},
		{"open", link, "-c", "--yes"},
		{"status", link, "-c"},
		{"send", link, "-c"},
		{"delete", owner, "-c", "--yes"},
		{"receive", "7-acid-rocket", server, "-c", "--yes"},
	} {
		if _, stderr, code := runCLI(t, "", args...); code != ExitError || !strings.Contains(stderr, "--clipboard: no clipboard tool found") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}

	// Nothing was deleted or used up: it still opens.
	if _, _, code := runCLI(t, "", "status", owner); code != 0 {
		t.Errorf("owner link afterwards: exit %d", code)
	}
	if stdout, _, code := runCLI(t, "", "open", link, "--yes"); code != 0 || stdout != "only once\n" {
		t.Errorf("afterwards: exit %d, stdout %q", code, stdout)
	}
}

// Files are saved as usual with --clipboard, and --json still prints text:
// what is given explicitly wins.
func TestOpenWithTheClipboardSavesFilesAndKeepsJSON(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	cb := fakeClipboard(t)
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink, _ := shareText(t, server, "", notes)
	out := filepath.Join(dir, "received")
	if _, stderr, code := runCLI(t, "", "open", fileLink, "-c", "--yes", "--out", out); code != 0 {
		t.Fatalf("files: exit %d, stderr %q", code, stderr)
	}
	if data, err := os.ReadFile(filepath.Join(out, "notes.txt")); err != nil || string(data) != "notes" {
		t.Errorf("notes.txt = %q, %v", data, err)
	}

	link, _ := shareText(t, server, "in json\n")
	stdout, stderr, code := runCLI(t, "", "--json", "open", link, "-c", "--yes")
	if code != 0 || !strings.Contains(stdout, `"text":"in json\n"`) {
		t.Errorf("json: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if text, _ := cb.state(); text != "" {
		t.Errorf("clipboard after json = %q", text)
	}
}

func TestACopyThatFailsPrintsTheSecretAfterAll(t *testing.T) {
	srv := fakeServer(t)
	cb := fakeClipboard(t)
	cb.copyErr = errors.New("xclip: exit status 1")
	link, _ := shareText(t, "--server="+srv.URL, "hunter2\n")

	// The one-time secret is used up by then, so this is the only copy.
	stdout, stderr, code := runCLI(t, "", "open", link, "--clipboard", "--yes")
	if code != 0 || stdout != "hunter2\n" || !strings.Contains(stderr, "Couldn't copy to the clipboard (xclip: exit status 1), so here it is:") {
		t.Errorf("open: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	// The new link is printed after all, too.
	stdout, stderr, code = runCLI(t, "a secret", "share", "--server="+srv.URL, "-c")
	if code != 0 || !strings.Contains(stdout, "/s#") || !strings.Contains(stderr, "Couldn't copy to the clipboard (xclip: exit status 1), so here it is.") {
		t.Errorf("share: exit %d, stdout %q, stderr %q", code, stdout, stderr)
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
	if stdout, stderr, code := receive(link, "-c", "--yes"); code != 0 || stdout != "" {
		t.Errorf("text: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if text, _ := cb.state(); text != "handed over" {
		t.Errorf("clipboard after text = %q", text)
	}

	// With --link, the link is what goes on the clipboard.
	link, _ = shareText(t, server, "a link\n")
	if stdout, stderr, code := receive(link, "-c", "--link"); code != 0 || stdout != "" || !strings.Contains(stderr, "Copied the link") {
		t.Errorf("link: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if text, _ := cb.state(); text != link {
		t.Errorf("clipboard after --link = %q, want %q", text, link)
	}

	// Files are saved as usual.
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink, _ := shareText(t, server, "", notes)
	out := filepath.Join(dir, "received")
	if _, stderr, code := receive(fileLink, "-c", "--yes", "--out", out); code != 0 {
		t.Errorf("files: exit %d, stderr %q", code, stderr)
	}
	if data, err := os.ReadFile(filepath.Join(out, "notes.txt")); err != nil || string(data) != "notes" {
		t.Errorf("notes.txt = %q, %v", data, err)
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

	// The command behind --clipboard's clearing stays out of sight.
	if stdout, _, _ := runCLI(t, "", "--help"); strings.Contains(stdout, "clear-clipboard") {
		t.Errorf("help lists clear-clipboard: %q", stdout)
	}
}
