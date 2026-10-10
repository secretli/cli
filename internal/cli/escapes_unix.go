//go:build !windows

package cli

import "os"

// enableEscapes makes sure a terminal acts on escape codes, for colour and
// the progress line, and returns how to put it back. Terminals here always
// do.
func enableEscapes(*os.File) (bool, func()) {
	return true, func() {}
}
