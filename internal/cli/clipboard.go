package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// clipboardClearAfter is how long an opened secret stays on the clipboard.
const clipboardClearAfter = 45 * time.Second

// clipboardTool is the desktop's clipboard.
type clipboardTool interface {
	// Available fails when there is no clipboard to copy to, so that
	// --copy is refused before anything is opened.
	Available() error
	Copy(text string) error
	Paste() (string, error)
	Clear() error
}

// clipboard is the clipboard commands use. The tests replace it, so that
// they never touch the clipboard of the machine they run on.
var clipboard clipboardTool = systemClipboard{}

// clipTool is one command line tool for the clipboard. clear is empty for
// tools that clear by copying nothing.
type clipTool struct{ copy, paste, clear []string }

func clipTools() []clipTool {
	windows := clipTool{copy: []string{"clip.exe"}, paste: []string{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard -Raw"}}
	switch runtime.GOOS {
	case "darwin":
		return []clipTool{{copy: []string{"pbcopy"}, paste: []string{"pbpaste"}}}
	case "windows":
		return []clipTool{windows}
	default:
		return []clipTool{
			{copy: []string{"wl-copy"}, paste: []string{"wl-paste", "--no-newline"}, clear: []string{"wl-copy", "--clear"}},
			{copy: []string{"xclip", "-selection", "clipboard"}, paste: []string{"xclip", "-selection", "clipboard", "-o"}},
			{copy: []string{"xsel", "--clipboard", "--input"}, paste: []string{"xsel", "--clipboard", "--output"}, clear: []string{"xsel", "--clipboard", "--clear"}},
			windows, // WSL
		}
	}
}

// systemClipboard uses whichever of the known tools the desktop has.
type systemClipboard struct{}

func (systemClipboard) tool() (clipTool, error) {
	for _, t := range clipTools() {
		if _, err := exec.LookPath(t.copy[0]); err == nil {
			return t, nil
		}
	}
	return clipTool{}, errors.New("no clipboard tool found (pbcopy, wl-copy, xclip, xsel or clip.exe)")
}

func (c systemClipboard) Available() error {
	_, err := c.tool()
	return err
}

func (c systemClipboard) Copy(text string) error {
	t, err := c.tool()
	if err != nil {
		return err
	}
	return runClipTool(t.copy, text)
}

func (c systemClipboard) Paste() (string, error) {
	t, err := c.tool()
	if err != nil {
		return "", err
	}
	return readClipTool(t.paste)
}

func (c systemClipboard) Clear() error {
	t, err := c.tool()
	if err != nil {
		return err
	}
	if len(t.clear) == 0 {
		return runClipTool(t.copy, "")
	}
	return runClipTool(t.clear, "")
}

// runClipTool runs a tool that copies or clears. Its output is not read:
// xclip and wl-copy stay behind to serve the clipboard, and waiting for
// their output to end would wait for them.
func runClipTool(args []string, input string) error {
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) //nolint:gosec // fixed list of known clipboard tools
	cmd.Stdin = strings.NewReader(input)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}

// readClipTool runs a tool that pastes, and returns what it printed.
func readClipTool(args []string) (string, error) {
	out, err := exec.CommandContext(context.Background(), args[0], args[1:]...).Output() //nolint:gosec // fixed list of known clipboard tools
	if err != nil {
		return "", fmt.Errorf("%s: %w", args[0], err)
	}
	return string(out), nil
}

// clipHash identifies what was put on the clipboard without carrying it.
func clipHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// stillHolds reports whether the clipboard's content is the text with this
// hash. Some paste tools add a line break of their own, so one is allowed.
func stillHolds(content, hash string) bool {
	for _, c := range []string{content, strings.TrimSuffix(content, "\n"), strings.TrimSuffix(content, "\r\n")} {
		if clipHash(c) == hash {
			return true
		}
	}
	return false
}

// withoutFinalLineBreak drops the line break text from a pipe ends with, so
// that pasting a password does not also press Enter.
func withoutFinalLineBreak(text string) string {
	if t, ok := strings.CutSuffix(text, "\r\n"); ok {
		return t
	}
	return strings.TrimSuffix(text, "\n")
}

// copySecret puts a secret, or a link, on the clipboard instead of the
// terminal, where it would stay in the scrollback, and has the clipboard
// cleared again later. If copying fails, the text is printed after all: a
// one-time secret is used up by now, and this is the only copy.
func (e *env) copySecret(text, what string) error {
	copied := withoutFinalLineBreak(text)
	if err := clipboard.Copy(copied); err != nil {
		e.note("Couldn't copy to the clipboard (%v), so here it is:\n", err)
		_, err := io.WriteString(e.stdout, text)
		if err == nil && e.stdoutTTY && !strings.HasSuffix(text, "\n") {
			_, err = fmt.Fprintln(e.stdout)
		}
		return err
	}
	if err := startClipboardClearer(clipHash(copied), clipboardClearAfter); err != nil {
		e.note("Copied %s to the clipboard, but couldn't arrange for it to be cleared (%v); clear it yourself.\n", what, err)
		return nil
	}
	e.done("Copied %s to the clipboard; it is cleared in %d seconds.\n", what, int(clipboardClearAfter.Seconds()))
	return nil
}

// startClipboardClearer runs this command again, in the background and on
// its own, to clear the clipboard after a while. It passes only the hash:
// arguments are visible to everyone on the machine. The tests replace it.
var startClipboardClearer = func(hash string, after time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), exe, "clear-clipboard", hash, strconv.Itoa(int(after.Seconds()))) //nolint:gosec // this very program
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// newClearClipboardCmd is what startClipboardClearer runs. It waits, then
// clears the clipboard unless something else was copied in the meantime.
func newClearClipboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "clear-clipboard <sha256> <seconds>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			seconds, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			return clearClipboardAfter(cmd.Context(), args[0], time.Duration(seconds)*time.Second)
		},
	}
}

// clearClipboardAfter clears the clipboard after the given time, or sooner
// when the process is asked to stop, if it still holds the text with this
// hash.
func clearClipboardAfter(ctx context.Context, hash string, after time.Duration) error {
	timer := time.NewTimer(after)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	content, err := clipboard.Paste()
	if err != nil {
		return err
	}
	if !stillHolds(content, hash) {
		return nil
	}
	return clipboard.Clear()
}
