package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// interruptible runs a blocking read and gives up as soon as ctx is done.
// A read from a terminal or a pipe doesn't notice Ctrl-C by itself, because
// the command catches the signal to end uploads and transfers cleanly. The
// abandoned read goes away with the process, which is about to exit.
func interruptible[T any](ctx context.Context, read func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := read()
		done <- result{value, err}
	}()
	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// terminal is where questions are asked: the controlling terminal when there
// is one, so a pipe on stdin does not get in the way.
type terminal struct {
	in  *os.File
	out io.Writer
	fd  int
	own bool
}

var errNoTerminal = errors.New("not at a terminal")

func (e *env) terminal() (*terminal, error) {
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		return &terminal{in: tty, out: tty, fd: int(tty.Fd()), own: true}, nil
	}
	if e.stdinTTY {
		return &terminal{in: e.stdin, out: e.stderr, fd: int(e.stdin.Fd())}, nil
	}
	return nil, errNoTerminal
}

func (t *terminal) close() {
	if t.own {
		_ = t.in.Close()
	}
}

// askLine prints the prompt and reads one line.
func (t *terminal) askLine(ctx context.Context, prompt string) (string, error) {
	_, _ = fmt.Fprint(t.out, prompt)
	line, err := interruptible(ctx, func() (string, error) { return bufio.NewReader(t.in).ReadString('\n') })
	if ctx.Err() != nil {
		// Ctrl-C leaves the cursor after the prompt; what follows goes below it.
		_, _ = fmt.Fprintln(t.out)
		return "", ctx.Err()
	}
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// askSecret prints the prompt and reads a line without echoing it.
func (t *terminal) askSecret(ctx context.Context, prompt string) (string, error) {
	_, _ = fmt.Fprint(t.out, prompt)
	// ReadPassword turns echo back on when it returns. An interrupted read
	// never returns, so the terminal is put back here, or the shell would
	// stay without echo.
	state, stateErr := term.GetState(t.fd)
	b, err := interruptible(ctx, func() ([]byte, error) { return term.ReadPassword(t.fd) })
	if ctx.Err() != nil {
		if stateErr == nil {
			_ = term.Restore(t.fd, state)
		}
		// The abandoned read still holds the descriptor, which ReadPassword
		// reads directly rather than through the file. Closing a terminal
		// that another thread reads blocks on macOS, so it is left to the
		// exiting process.
		t.own = false
	}
	_, _ = fmt.Fprintln(t.out)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// confirm asks a yes/no question; anything but y or yes is no.
func (t *terminal) confirm(ctx context.Context, prompt string) (bool, error) {
	answer, err := t.askLine(ctx, prompt+" [y/N] ")
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// passwordSource is how a command gets a password: a file, the environment,
// or a prompt.
type passwordSource struct {
	flag bool
	file string
}

// resolve returns the password, or "" when none was asked for. required
// makes it ask at a terminal even without the flag, since the secret needs one.
func (p passwordSource) resolve(ctx context.Context, e *env, required, confirm bool) (string, error) {
	switch {
	case p.file != "":
		b, err := os.ReadFile(p.file)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		line, _, _ := strings.Cut(string(b), "\n")
		return strings.TrimRight(line, "\r"), nil
	case !p.flag && !required:
		return "", nil
	}
	if pw := os.Getenv("SECRETLI_PASSWORD"); pw != "" {
		return pw, nil
	}
	t, err := e.terminal()
	if err != nil {
		return "", errors.New("a password is needed: use --password-file or SECRETLI_PASSWORD when not at a terminal")
	}
	defer t.close()
	pw, err := t.askSecret(ctx, "Password: ")
	if err != nil {
		return "", err
	}
	if pw == "" {
		return "", errors.New("the password is empty")
	}
	if confirm {
		again, err := t.askSecret(ctx, "Again: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("the passwords differ")
		}
	}
	return pw, nil
}
