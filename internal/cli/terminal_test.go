//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	seen   int           // how far expect has read
	more   chan struct{} // signalled when output grows
}

// startAtTerminal starts the command with these arguments in a terminal of
// its own: the test binary, which TestMain turns into the command.
func startAtTerminal(t *testing.T, args ...string) *atTerminal {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], args...)
	// The race detector waits a second before a program exits; the
	// command needn't.
	cmd.Env = []string{runAsCommand + "=1", "GORACE=" + os.Getenv("GORACE") + " atexit_sleep_ms=0"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SECRETLI_") && !strings.HasPrefix(kv, "GORACE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
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

// expect waits until text appears after what the last expect found, and
// returns what appeared up to its end.
func (c *atTerminal) expect(text string) string {
	c.t.Helper()
	timeout := time.After(terminalWait)
	for {
		c.mu.Lock()
		from := c.seen
		i := bytes.Index(c.output[from:], []byte(text))
		if i >= 0 {
			c.seen = from + i + len(text)
			got := string(c.output[from:c.seen])
			c.mu.Unlock()
			return got
		}
		c.mu.Unlock()
		select {
		case <-c.more:
		case <-c.read:
			c.t.Fatalf("the terminal closed before %q appeared; it shows:\n%s", text, c.screen())
		case <-timeout:
			c.t.Fatalf("%q did not appear; the terminal shows:\n%s", text, c.screen())
		}
	}
}

// screen is everything the terminal has shown.
func (c *atTerminal) screen() string {
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
