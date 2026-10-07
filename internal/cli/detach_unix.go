//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, so that it outlives the
// terminal it was started from and never receives its Ctrl-C.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
