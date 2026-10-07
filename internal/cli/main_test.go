package cli

import (
	"os"
	"testing"
)

// The tests never ask on the terminal they run in: without a terminal,
// as in CI, commands that would ask fail instead, and tests that want an
// answer give one with answering.
func TestMain(m *testing.M) {
	openTTY = func() (*terminal, error) { return nil, errNoTerminal }
	os.Exit(m.Run())
}
