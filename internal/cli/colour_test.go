package cli

import (
	"strings"
	"testing"
)

func TestColours(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		json     bool
		env      map[string]string
		want     bool
	}{
		{"a terminal", true, false, map[string]string{"TERM": "xterm-256color"}, true},
		{"a terminal without TERM", true, false, nil, true},
		{"not a terminal", false, false, map[string]string{"TERM": "xterm-256color"}, false},
		{"--json", true, true, map[string]string{"TERM": "xterm-256color"}, false},
		{"NO_COLOR", true, false, map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"}, false},
		{"NO_COLOR, empty", true, false, map[string]string{"TERM": "xterm-256color", "NO_COLOR": ""}, false},
		{"TERM=dumb", true, false, map[string]string{"TERM": "dumb"}, false},
	}
	for _, tc := range cases {
		lookup := func(name string) (string, bool) {
			v, ok := tc.env[name]
			return v, ok
		}
		if got := colours(tc.terminal, tc.json, lookup); got != tc.want {
			t.Errorf("%s: colours = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// Off a terminal, as in every other test, nothing is coloured: stderr is
// the same text as ever, and done lines come without a tick.
func TestNoColourOffATerminal(t *testing.T) {
	srv := fakeServer(t)
	link := shareFiles(t, "--server="+srv.URL, []string{"a.txt"}, "--reusable")
	out := t.TempDir()
	_, stderr, code := runCLI(t, "", "open", link, "--out", out)
	if code != 0 || strings.Contains(stderr, "\x1b") || !strings.HasSuffix(stderr, "\nSaved a.txt (13 B) to "+out+".\n") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	_, stderr, code = runCLI(t, "", "open", link+"x")
	if code == 0 || !strings.HasPrefix(stderr, "secretli: ") || strings.Contains(stderr, "\x1b") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}
