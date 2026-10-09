package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// answering stands in a terminal that answers every question with answer,
// and returns what was asked on it.
func answering(t *testing.T, answer string) *bytes.Buffer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(path, []byte(answer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := &bytes.Buffer{}
	openTTY = func() (*terminal, error) {
		in, err := os.Open(path) //nolint:gosec // the test's own file
		if err != nil {
			return nil, err
		}
		return &terminal{in: in, out: asked, fd: int(in.Fd()), own: true}, nil
	}
	t.Cleanup(func() { openTTY = func() (*terminal, error) { return nil, errNoTerminal } })
	return asked
}

// answeringInTurn stands in a terminal that answers successive questions
// with successive lines, and returns what was asked on it. Every terminal
// it opens reads from the same file, so each question takes the line after
// the one the last question took.
func answeringInTurn(t *testing.T, answers ...string) *bytes.Buffer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(path, []byte(strings.Join(answers, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(path) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() })
	asked := &bytes.Buffer{}
	openTTY = func() (*terminal, error) {
		return &terminal{in: in, out: asked, fd: int(in.Fd())}, nil
	}
	t.Cleanup(func() { openTTY = func() (*terminal, error) { return nil, errNoTerminal } })
	return asked
}

// shareText shares text and returns the link and the owner link.
func shareText(t *testing.T, server, text string, extra ...string) (string, string) {
	t.Helper()
	stdout, stderr, code := runCLI(t, text, append([]string{"share", server}, extra...)...)
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	owner := ""
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "/s#") && strings.Contains(line, "!") {
			owner = line
		}
	}
	return strings.TrimSpace(stdout), owner
}

func TestAOneTimeSecretIsOpenedOnlyWhenAskedFor(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	link, owner := shareText(t, server, "only once\n")

	// No terminal to ask on: --yes has to say so, and nothing is used up.
	_, stderr, code := runCLI(t, "", "open", link)
	if code != ExitError || !strings.Contains(stderr, "opens only once; use --yes") {
		t.Errorf("without a terminal: exit %d, stderr %q", code, stderr)
	}

	// Asked and declined: still nothing used up.
	asked := answering(t, "n")
	_, stderr, code = runCLI(t, "", "open", link)
	if code != ExitError || !strings.Contains(stderr, "Not opened; the link still works.") {
		t.Errorf("declined: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(asked.String(), "Open it now? It opens only once; after that the link stops working. [y/N]") {
		t.Errorf("asked %q", asked.String())
	}

	// The owner is told what opening means for the person it was sent to.
	asked = answering(t, "")
	if _, _, code = runCLI(t, "", "open", owner); code != ExitError || !strings.Contains(asked.String(), "the person you sent it to won't be able to") {
		t.Errorf("owner, Enter: exit %d, asked %q", code, asked.String())
	}

	// Yes opens it.
	answering(t, "y")
	if stdout, stderr, code := runCLI(t, "", "open", link); code != 0 || stdout != "only once\n" {
		t.Errorf("yes: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestAReusableSecretOpensWithoutAQuestion(t *testing.T) {
	srv := fakeServer(t)
	link, _ := shareText(t, "--server="+srv.URL, "again and again\n", "--reusable")
	for range 2 {
		if stdout, stderr, code := runCLI(t, "", "open", link); code != 0 || stdout != "again and again\n" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	}
}

func TestReceiveNeverLosesTheLink(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	link, _ := shareText(t, server, "handed over\n")

	// Without a terminal and without --yes or --link, receive refuses before
	// it takes the code, which stays usable.
	sender := startCLI(t, "", "send", link)
	transferCode := firstLine(t, sender)
	_, stderr, code := runCLI(t, "", "receive", transferCode, server)
	if code != ExitError || !strings.Contains(stderr, "use --yes to open it anyway, or --link") {
		t.Errorf("without a terminal: exit %d, stderr %q", code, stderr)
	}
	if fake := srv.Fake(); fake != nil {
		if states := fake.Transfers(); len(states) != 1 || states[0].Claimed {
			t.Errorf("the code was taken: %+v", states)
		}
	}

	// Asked and declined: the link is printed, since the code is used up now.
	answering(t, "n")
	stdout, stderr, code := runCLI(t, "", "receive", transferCode, server)
	if code != ExitError || strings.TrimSpace(stdout) != link || !strings.Contains(stderr, "Here is the link, which still works") {
		t.Errorf("declined: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code := exitOf(t, sender); code != 0 {
		t.Errorf("send: exit %d", code)
	}

	// And the link still opens.
	if stdout, _, code := runCLI(t, "", "open", link, "--yes"); code != 0 || stdout != "handed over\n" {
		t.Errorf("open afterwards: exit %d, stdout %q", code, stdout)
	}
}
