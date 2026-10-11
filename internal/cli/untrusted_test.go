package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrintableNames(t *testing.T) {
	for in, want := range map[string]string{
		"notes.txt":                     "notes.txt",
		"invoice\u202Etxt.exe":          "invoice?txt.exe",
		"a\u2066b\u2069c\u200Fd\u200Be": "a?b?c?d?e",
		"title\x1b]0;x\x07.txt":         "title?]0;x?.txt",
		"line\nbreak\ttab":              "line?break?tab",
		"c1\u009b31m":                   "c1?31m",
		"bad\xffbyte":                   "bad?byte",
		"naïve résumé.pdf":              "naïve résumé.pdf",
		"👨\u200D👩\u200D👧.png":           "👨\u200D👩\u200D👧.png",
		"می\u200Cخواهم.txt":             "می\u200Cخواهم.txt",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintableMessageKeepsLines(t *testing.T) {
	in := "first line\n\tsecond\x1b[2Jcleared\u202Eflipped\rover"
	want := "first line\n\tsecond?[2Jcleared?flipped?over"
	if got := printableMessage(in); got != want {
		t.Errorf("printableMessage(%q) = %q, want %q", in, got, want)
	}
}

func TestPrintableText(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		changed  bool
	}{
		{"hunter2", "hunter2", false},
		{"lines\r\nwith CR LF\nand\ttabs\n", "lines\r\nwith CR LF\nand\ttabs\n", false},
		{"עברית \u202Bembedded\u202C and U+FFFD \uFFFD stay", "עברית \u202Bembedded\u202C and U+FFFD \uFFFD stay", false},
		{"title\x1b]0;x\x07", "title\uFFFD]0;x\uFFFD", true},
		{"hidden\rover", "hidden\uFFFDover", true},
		{"back\bspace\x7f", "back\uFFFDspace\uFFFD", true},
		{"c1 \u009b31m", "c1 \uFFFD31m", true},
		{"bad\xffbyte", "bad\uFFFDbyte", true},
	} {
		got, changed := printableText([]byte(tc.in))
		if got != tc.want || changed != tc.changed {
			t.Errorf("printableText(%q) = %q, %t; want %q, %t", tc.in, got, changed, tc.want, tc.changed)
		}
	}
}

// The text a server sends with an error reaches the terminal without its
// control characters.
func TestServerErrorsAreShownPlain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad\u001b]8;;https://example.com\u0007click\u001b]8;;\u0007 request"}`))
	}))
	t.Cleanup(srv.Close)
	link := srv.URL + "/s#" + strings.Repeat("A", 43)

	_, stderr, code := runCLI(t, "", "status", link)
	if code == 0 || strings.ContainsAny(stderr, "\x1b\x07") || !strings.Contains(stderr, "bad?]8;;https://example.com?click?]8;;? request") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// Into a pipe, and as JSON, the text stays exactly as it was shared.
func TestTextWithControlCharactersStaysExactInAPipe(t *testing.T) {
	srv := fakeServer(t)
	const text = "title\x1b]0;x\x07 and\r a CR"
	link, _ := shareText(t, "--server="+srv.URL, text, "--reusable")

	stdout, stderr, code := runCLI(t, "", "open", link)
	if code != 0 || stdout != text || strings.Contains(stderr, "control characters") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	stdout, _, code = runCLI(t, "", "open", link, "--json")
	if code != 0 || !strings.Contains(stdout, `"text":"title\u001b]0;x\u0007 and\r a CR"`) {
		t.Errorf("--json: exit %d, stdout %q", code, stdout)
	}
}
