package cli

import (
	"errors"
	"os"
	"testing"
	"time"
)

// The tests never ask on the terminal they run in: without a terminal,
// as in CI, commands that would ask fail instead, and tests that want an
// answer give one with answering. Nor do they touch the machine's
// clipboard or start anything in the background; tests that copy use
// fakeClipboard.
func TestMain(m *testing.M) {
	openTTY = func() (*terminal, error) { return nil, errNoTerminal }
	clipboard = noClipboard{}
	startClipboardClearer = func(string, time.Duration) error { return errors.New("not in tests") }
	os.Exit(m.Run())
}
