//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// The tests in this file run the command at a terminal, as a person does:
// in a pseudo-terminal that is its controlling terminal, /dev/tty, and its
// stdin, stdout and stderr. They type answers and Ctrl-C, and read what
// appears, as the person would see it, prompts, echo and all.

// terminalWait bounds every wait for the command: for what it prints, and
// for it to end.
const terminalWait = 15 * time.Second

// atTerminal is the command running in a pseudo-terminal.
type atTerminal struct {
	t   *testing.T
	pty *os.File // the terminal's other side: what is typed goes in here
	cmd *exec.Cmd

	exited chan struct{}
	read   chan struct{} // closed once nothing more can be read

	mu     sync.Mutex
	output []byte
	more   chan struct{} // signalled when output grows
	seen   int           // how far expect has read on the screen
}

// terminalSetup changes how the command starts at the terminal: with more
// in its environment, or with stdout or stderr going elsewhere.
type terminalSetup struct {
	env            []string
	stdout, stderr *os.File
}

// startAtTerminal starts the command with these arguments in a terminal of
// its own: the test binary, which TestMain turns into the command.
func startAtTerminal(t *testing.T, args ...string) *atTerminal {
	t.Helper()
	return startAtTerminalWith(t, terminalSetup{}, args...)
}

func startAtTerminalWith(t *testing.T, setup terminalSetup, args ...string) *atTerminal {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], args...)
	// A terminal that takes colour, whatever the tests run in. The race
	// detector waits a second before a program exits; the command needn't.
	cmd.Env = []string{runAsCommand + "=1", "TERM=xterm-256color", "GORACE=" + os.Getenv("GORACE") + " atexit_sleep_ms=0"}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "SECRETLI_") && name != "GORACE" && name != "TERM" && name != "NO_COLOR" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, setup.env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if setup.stdout != nil {
		cmd.Stdout = setup.stdout
	}
	if setup.stderr != nil {
		cmd.Stderr = setup.stderr
	}
	// A session of its own, with the terminal as its controlling terminal:
	// /dev/tty is the terminal then, and Ctrl-C reaches the command.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &atTerminal{t: t, pty: ptmx, cmd: cmd, exited: make(chan struct{}), read: make(chan struct{}), more: make(chan struct{}, 1)}
	go func() {
		_ = cmd.Wait()
		close(c.exited)
	}()
	go func() {
		defer close(c.read)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			c.mu.Lock()
			c.output = append(c.output, buf[:n]...)
			c.mu.Unlock()
			select {
			case c.more <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		select {
		case <-c.exited:
		default:
			_ = cmd.Process.Kill()
			<-c.exited
		}
		// With the command gone and this side closed, the read ends.
		_ = tty.Close()
		<-c.read
		_ = ptmx.Close()
	})
	return c
}

// escapeCode is a control sequence: colour, or clearing a line.
var escapeCode = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// expect waits until text appears after what the last expect found, and
// returns what appeared up to its end. It reads the text as the person
// does, without the escape codes that colour it.
func (c *atTerminal) expect(text string) string {
	c.t.Helper()
	timeout := time.After(terminalWait)
	for {
		shown := c.screen()
		from := c.seen
		if i := strings.Index(shown[from:], text); i >= 0 {
			c.seen = from + i + len(text)
			return shown[from:c.seen]
		}
		select {
		case <-c.more:
		case <-c.read:
			c.t.Fatalf("the terminal closed before %q appeared; it shows:\n%s", text, c.screen())
		case <-timeout:
			c.t.Fatalf("%q did not appear; the terminal shows:\n%s", text, c.screen())
		}
	}
}

// screen is everything the terminal has shown, without escape codes.
func (c *atTerminal) screen() string {
	return escapeCode.ReplaceAllString(c.raw(), "")
}

// raw is everything the terminal has been sent, escape codes and all.
func (c *atTerminal) raw() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.output)
}

// typ types at the terminal; Enter is "\r", Ctrl-C "\x03".
func (c *atTerminal) typ(keys string) {
	c.t.Helper()
	if _, err := c.pty.WriteString(keys); err != nil {
		c.t.Fatal(err)
	}
}

// exitCode waits for the command to end.
func (c *atTerminal) exitCode() int {
	c.t.Helper()
	select {
	case <-c.exited:
		return c.cmd.ProcessState.ExitCode()
	case <-time.After(terminalWait):
		c.t.Fatalf("the command did not end; the terminal shows:\n%s", c.screen())
		return -1
	}
}

// echoes reports whether the terminal shows what is typed.
func (c *atTerminal) echoes() bool {
	c.t.Helper()
	settings, err := unix.IoctlGetTermios(int(c.pty.Fd()), readTermios)
	if err != nil {
		c.t.Fatal(err)
	}
	return settings.Lflag&unix.ECHO != 0
}

// typeSecret types at a password prompt. What is typed before the prompt
// turns echo off shows, as anywhere, so it waits for that first, as the
// person reading the prompt does.
func (c *atTerminal) typeSecret(keys string) {
	c.t.Helper()
	c.waitUntil("stopped showing what is typed", func() bool { return !c.echoes() })
	c.typ(keys)
}

// waitUntil waits for a condition the terminal shows no sign of.
func (c *atTerminal) waitUntil(what string, ok func() bool) {
	c.t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	timeout := time.After(terminalWait)
	for !ok() {
		select {
		case <-tick.C:
		case <-timeout:
			c.t.Fatalf("never %s; the terminal shows:\n%s", what, c.screen())
		}
	}
}

func TestTerminalOpenItNow(t *testing.T) {
	srv := fakeServer(t)
	link, _ := shareText(t, "--server="+srv.URL, "only once\n")
	question := "Open it now? It opens only once; after that the link stops working. [y/N] "

	// No keeps it.
	c := startAtTerminal(t, "open", link)
	c.expect("A one-time text secret, sent just now")
	c.expect(question)
	c.typ("n\r")
	c.expect("Not opened; the link still works.")
	if code := c.exitCode(); code != ExitError {
		t.Errorf("no: exit %d", code)
	}

	// Yes opens it, and only then is it gone.
	c = startAtTerminal(t, "open", link)
	c.expect(question)
	c.typ("y\r")
	c.expect("only once\r\n")
	if code := c.exitCode(); code != 0 {
		t.Errorf("yes: exit %d; the terminal shows:\n%s", code, c.screen())
	}
	if _, _, code := runCLI(t, "", "status", link); code != ExitGone {
		t.Errorf("status after yes: exit %d", code)
	}
}

func TestTerminalSaveWhich(t *testing.T) {
	srv := fakeServer(t)
	link, big := shareLargeFiles(t, "--server="+srv.URL)
	out := filepath.Join(t.TempDir(), "out")
	question := "Save which? [Enter = all, or e.g. 2 · 1 3 · 1-2 · *.jpg] "

	c := startAtTerminal(t, "open", link, "--out", out)
	c.expect("[y/N] ")
	from := time.Now()
	c.typ("y\r")
	shown := c.expect(question)
	// The terminal starts each line at the left again.
	list := "  1  a.txt      13 B\r\n  2  big.bin  1.2 MB\r\n"
	warnings := deadlineWarning(from, time.Now())
	if !strings.Contains(shown, list+strings.ReplaceAll(warnings[0], "\n", "\r\n")) && !strings.Contains(shown, list+strings.ReplaceAll(warnings[1], "\n", "\r\n")) {
		t.Errorf("the terminal shows %q before the question, want the list and the warning %q", shown, warnings)
	}
	c.typ("3\r")
	c.expect("there is no file 3; the files are numbered 1 to 2\r\n")
	c.expect(question)
	c.typ("*.BIN\r")
	c.expect("Saved big.bin (1.2 MB) to " + out + ".")
	if code := c.exitCode(); code != 0 {
		t.Fatalf("exit %d; the terminal shows:\n%s", code, c.screen())
	}
	entries, _ := os.ReadDir(out)
	if got, err := os.ReadFile(filepath.Join(out, "big.bin")); len(entries) != 1 || err != nil || !bytes.Equal(got, big) {
		t.Errorf("saved %d files, big.bin holds %d bytes, %v", len(entries), len(got), err)
	}
}

func TestTerminalPasswordIsNotShown(t *testing.T) {
	srv := fakeServer(t)
	const password = "correct horse"

	// Asked twice when sharing,
	c := startAtTerminal(t, "share", "--server="+srv.URL, "-p", "--reusable", "-t", "the vault code")
	c.expect("Password: ")
	c.typeSecret(password + "\r")
	c.expect("Again: ")
	c.typeSecret(password + "\r")
	shown := c.expect(srv.URL + "/s#")
	rest := c.expect("\n")
	if code := c.exitCode(); code != 0 {
		t.Fatalf("share: exit %d; the terminal shows:\n%s", code, c.screen())
	}
	if strings.Contains(shown, "correct") {
		t.Errorf("the password shows when sharing:\n%s", c.screen())
	}
	link := srv.URL + "/s#" + strings.TrimSpace(rest)

	// and once when opening.
	c = startAtTerminal(t, "open", link)
	c.expect("Password: ")
	c.typeSecret(password + "\r")
	shown = c.expect("the vault code")
	if code := c.exitCode(); code != 0 {
		t.Fatalf("open: exit %d; the terminal shows:\n%s", code, c.screen())
	}
	if strings.Contains(shown, "correct") {
		t.Errorf("the password shows when opening:\n%s", c.screen())
	}
}

// Ctrl-C at a question ends the command as interrupted, exit code 1, and
// leaves the terminal as it found it, showing what is typed.
func TestTerminalCtrlC(t *testing.T) {
	srv := fakeServer(t)

	t.Run("open it now", func(t *testing.T) {
		link, _ := shareText(t, "--server="+srv.URL, "only once\n")
		c := startAtTerminal(t, "open", link)
		c.expect("[y/N] ")
		c.typ("\x03")
		c.expect("secretli: interrupted")
		if code := c.exitCode(); code != ExitError {
			t.Errorf("exit %d", code)
		}
		if !c.echoes() {
			t.Error("the terminal no longer shows what is typed")
		}
		if _, _, code := runCLI(t, "", "status", link); code != 0 {
			t.Errorf("status afterwards: exit %d", code)
		}
	})

	// The password prompt turns echo off, so it has to turn it on again.
	t.Run("password", func(t *testing.T) {
		dir := t.TempDir()
		passwordFile := filepath.Join(dir, "password")
		if err := os.WriteFile(passwordFile, []byte("correct horse\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link, _ := shareText(t, "--server="+srv.URL, "the vault code", "--reusable", "--password-file", passwordFile)
		c := startAtTerminal(t, "open", link)
		c.expect("Password: ")
		c.typeSecret("corr")
		c.typ("\x03")
		c.expect("secretli: interrupted")
		if code := c.exitCode(); code != ExitError {
			t.Errorf("exit %d", code)
		}
		if !c.echoes() {
			t.Error("the terminal no longer shows what is typed")
		}
	})
}

// At a terminal, colour marks what matters: what cannot be undone in
// yellow, what is done in green with a tick, the error prefix in red. The
// text is the same as without it; the other terminal tests read it so.
func TestTerminalColour(t *testing.T) {
	srv := fakeServer(t)
	link, _ := shareLargeFiles(t, "--server="+srv.URL)
	out := filepath.Join(t.TempDir(), "out")

	c := startAtTerminal(t, "open", link, "--out", out)
	c.expect("[y/N] ")
	c.typ("y\r")
	c.expect("Save which?")
	c.typ("1\r")
	c.expect("Saved a.txt (13 B) to " + out + ".")
	if code := c.exitCode(); code != 0 {
		t.Fatalf("exit %d; the terminal shows:\n%s", code, c.screen())
	}
	for _, want := range []string{
		"Open it now? \x1b[33mIt opens only once; after that the link stops working.\x1b[0m [y/N] ",
		"\r\n\x1b[33mFiles you don't save by ",
		" are gone with this one-time secret.\x1b[0m\r\nSave which?",
		"\x1b[32m✓ Saved a.txt (13 B) to " + out + ".\x1b[0m\r\n",
	} {
		if !strings.Contains(c.raw(), want) {
			t.Errorf("the terminal was sent %q, without %q", c.raw(), want)
		}
	}

	c = startAtTerminal(t, "open", link)
	c.expect("secretli: this secret is gone")
	if code := c.exitCode(); code != ExitGone || !strings.Contains(c.raw(), "\x1b[31msecretli:\x1b[0m this secret is gone") {
		t.Errorf("exit %d, the terminal was sent %q", code, c.raw())
	}

	_, owner := shareText(t, "--server="+srv.URL, "to be deleted\n")
	c = startAtTerminal(t, "delete", owner)
	c.expect("[y/N] ")
	c.typ("y\r")
	c.expect("Deleted.")
	if code := c.exitCode(); code != 0 {
		t.Fatalf("delete: exit %d; the terminal shows:\n%s", code, c.screen())
	}
	for _, want := range []string{
		"Delete it for everyone? \x1b[33mThe link stops working right away.\x1b[0m [y/N] ",
		"\x1b[32m✓ Deleted. The link doesn't open anything any more.\x1b[0m\r\n",
	} {
		if !strings.Contains(c.raw(), want) {
			t.Errorf("the terminal was sent %q, without %q", c.raw(), want)
		}
	}
}

// NO_COLOR, set to anything, and TERM=dumb leave the terminal plain.
func TestTerminalWithoutColour(t *testing.T) {
	srv := fakeServer(t)
	for _, env := range []string{"NO_COLOR=1", "NO_COLOR=", "TERM=dumb"} {
		link, _ := shareText(t, "--server="+srv.URL, "plain\n")
		c := startAtTerminalWith(t, terminalSetup{env: []string{env}}, "open", link)
		c.expect("[y/N] ")
		c.typ("y\r")
		c.expect("plain\r\n")
		if code := c.exitCode(); code != 0 {
			t.Fatalf("%s: exit %d; the terminal shows:\n%s", env, code, c.screen())
		}
		if strings.Contains(c.raw(), "\x1b") {
			t.Errorf("%s: the terminal was sent %q", env, c.raw())
		}

		c = startAtTerminalWith(t, terminalSetup{env: []string{env}}, "open", link)
		c.expect("secretli: this secret is gone")
		if code := c.exitCode(); code != ExitGone || strings.Contains(c.raw(), "\x1b") {
			t.Errorf("%s: exit %d, the terminal was sent %q", env, code, c.raw())
		}
	}
}

// With stdout and stderr going to files at a terminal, as in `secretli
// open … > out 2> err`, the question is still asked at the terminal, in
// colour, and the files get plain text.
func TestTerminalKeepsFilesPlain(t *testing.T) {
	srv := fakeServer(t)
	link, _ := shareText(t, "--server="+srv.URL, "only once\n")
	dir := t.TempDir()
	file := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}

	c := startAtTerminalWith(t, terminalSetup{stdout: file("stdout"), stderr: file("stderr")}, "open", link)
	c.expect("[y/N] ")
	c.typ("y\r")
	if code := c.exitCode(); code != 0 {
		t.Fatalf("exit %d; the terminal shows:\n%s", code, c.screen())
	}
	if !strings.Contains(c.raw(), "\x1b[33mIt opens only once") {
		t.Errorf("the terminal was sent %q", c.raw())
	}
	if got := read("stdout"); got != "only once\n" {
		t.Errorf("stdout %q", got)
	}
	if got := read("stderr"); !strings.HasPrefix(got, "A one-time text secret, sent just now, expires ") || strings.Count(got, "\n") != 1 || strings.Contains(got, "\x1b") {
		t.Errorf("stderr %q", got)
	}

	c = startAtTerminalWith(t, terminalSetup{stdout: file("stdout2"), stderr: file("stderr2")}, "open", link)
	if code := c.exitCode(); code != ExitGone {
		t.Errorf("exit %d", code)
	}
	if got, want := read("stderr2"), "secretli: this secret is gone: it may have expired, been opened or been deleted\n"; got != want || read("stdout2") != "" {
		t.Errorf("stderr %q, want %q; stdout %q", got, want, read("stdout2"))
	}
}

// Ctrl-C during a download clears the progress line before it says so.
func TestTerminalCtrlCDuringADownload(t *testing.T) {
	srv := fakeServer(t)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// A server that hands out the start of a secret, then nothing more.
	proxy := httputil.NewSingleHostReverseProxy(target)
	stalling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/blob") && !strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
			<-r.Context().Done()
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(stalling.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), bytes.Repeat([]byte("this is big.bin\n"), 256*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "", "share", "--server="+stalling.URL, "--reusable", filepath.Join(dir, "big.bin"))
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}

	c := startAtTerminal(t, "open", strings.TrimSpace(stdout), "--out", filepath.Join(dir, "out"))
	c.expect("Downloading and decrypting… ")
	c.typ("\x03")
	c.expect("secretli: interrupted")
	if code := c.exitCode(); code != ExitError {
		t.Errorf("exit %d", code)
	}
	raw := c.raw()
	if !strings.Contains(raw, "\r\x1b[K\x1b[31msecretli:\x1b[0m interrupted\r\n") {
		t.Errorf("the progress line was not cleared before the error; the terminal was sent %q", raw)
	}
}
