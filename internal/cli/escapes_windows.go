package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableEscapes makes sure a terminal acts on escape codes, for colour and
// the progress line, and returns how to put it back. A Windows console
// does once virtual terminal processing is on; where that can't be turned
// on, there is neither colour nor a progress line.
func enableEscapes(f *os.File) (bool, func()) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false, func() {}
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true, func() {}
	}
	if err := windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return false, func() {}
	}
	return true, func() { _ = windows.SetConsoleMode(h, mode) }
}
