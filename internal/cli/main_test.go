package cli

import (
	"errors"
	"os"
	"testing"
	"time"
)

// runAsCommand, set to 1 in the environment, makes the test binary the
// command itself, with nothing replaced. The terminal tests start it that
// way, in a pseudo-terminal of its own.
const runAsCommand = "SECRETLI_TEST_RUN_AS_COMMAND"

// testClipboard names the file that is the clipboard of a command the
// terminal tests start.
const testClipboard = "SECRETLI_TEST_CLIPBOARD"

// fileClipboard is a clipboard in a file, for commands in a process of
// their own.
type fileClipboard string

func (f fileClipboard) Available() error { return nil }

func (f fileClipboard) Copy(text string) error { return os.WriteFile(string(f), []byte(text), 0o600) }

func (f fileClipboard) Paste() (string, error) {
	b, err := os.ReadFile(string(f))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func (f fileClipboard) Clear() error { return f.Copy("") }

// The tests never ask on the terminal they run in: without a terminal,
// as in CI, commands that would ask fail instead, and tests that want an
// answer give one with answering. Nor do they touch the machine's
// clipboard or start anything in the background; tests that copy use
// fakeClipboard.
func TestMain(m *testing.M) {
	if os.Getenv(runAsCommand) == "1" {
		// At a terminal too, the tests never touch the machine's clipboard:
		// one in a file stands in for it, and nothing is started to clear it.
		if path := os.Getenv(testClipboard); path != "" {
			clipboard = fileClipboard(path)
			startClipboardClearer = func(string, time.Duration) error { return nil }
		} else {
			clipboard = noClipboard{}
		}
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	openTTY = func() (*terminal, error) { return nil, errNoTerminal }
	clipboard = noClipboard{}
	startClipboardClearer = func(string, time.Duration) error { return errors.New("not in tests") }
	os.Exit(m.Run())
}
