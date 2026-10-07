//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// detachedProcess is DETACHED_PROCESS: no console, so closing the window
// the command was started from leaves cmd running.
const detachedProcess = 0x00000008

// detach starts cmd without a console and in a process group of its own,
// so that it outlives the window it was started from and never receives
// its Ctrl-C.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}
