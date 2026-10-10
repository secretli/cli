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

// The tests never ask on the terminal they run in: without a terminal,
// as in CI, commands that would ask fail instead, and tests that want an
// answer give one with answering. Nor do they touch the machine's
// clipboard or start anything in the background; tests that copy use
// fakeClipboard.
func TestMain(m *testing.M) {
	if os.Getenv(runAsCommand) == "1" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	openTTY = func() (*terminal, error) { return nil, errNoTerminal }
	clipboard = noClipboard{}
	startClipboardClearer = func(string, time.Duration) error { return errors.New("not in tests") }
	os.Exit(m.Run())
}
