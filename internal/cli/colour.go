package cli

import (
	"fmt"
	"os"
	"strings"
)

// The few colours the command uses, sparingly: red for the error prefix,
// yellow for what cannot be undone, green for what is done.
const (
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	reset  = "\033[0m"
)

// colours reports whether text for a stream may be coloured: only for a
// terminal, never with --json, and not when NO_COLOR is set, to anything,
// or TERM is dumb. stdout is never coloured, whatever this says: it is the
// result.
func colours(terminal, json bool, lookup func(string) (string, bool)) bool {
	if !terminal || json {
		return false
	}
	if _, set := lookup("NO_COLOR"); set {
		return false
	}
	name, _ := lookup("TERM")
	return name != "dumb"
}

// paint wraps s in a colour when on is set.
func paint(on bool, colour, s string) string {
	if !on {
		return s
	}
	return colour + s + reset
}

// stderrColours reports whether what goes to stderr may be coloured.
func (e *env) stderrColours() bool {
	return colours(e.stderrEscapes, e.json, os.LookupEnv)
}

// done tells the person that something is finished, as say does; in
// colour, green and ticked.
func (e *env) done(format string, args ...any) {
	if e.quiet || e.json {
		return
	}
	line := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")
	if e.stderrColours() {
		line = green + "✓ " + line + reset
	}
	_, _ = fmt.Fprintln(e.stderr, line)
}
