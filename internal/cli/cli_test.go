package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share/sharetest"
)

// runCLI runs Main with stdin content and returns stdout, stderr and the exit
// code. Files stand in for the pipes, so nothing here is a terminal.
func runCLI(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	r := startCLI(t, stdin, args...)
	code := <-r.done
	return r.stdout(), r.stderr(), code
}

// running is a command started with startCLI, whose output can be read
// while it runs.
type running struct {
	dir  string
	done chan int
}

func (r *running) stdout() string { return r.read("stdout") }
func (r *running) stderr() string { return r.read("stderr") }

func (r *running) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.dir, name))
	return string(b)
}

// startCLI runs Main in the background.
func startCLI(t *testing.T, stdin string, args ...string) *running {
	t.Helper()
	r := &running{dir: t.TempDir(), done: make(chan int, 1)}
	write := func(name, content string) *os.File {
		path := filepath.Join(r.dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	in, out, errOut := write("stdin", stdin), write("stdout", ""), write("stderr", "")
	go func() {
		defer in.Close()
		defer out.Close()
		defer errOut.Close()
		r.done <- Main(args, in, out, errOut)
	}()
	return r
}

// fakeServer is the fake server, or the real one named by
// SECRETLI_TEST_SERVER.
func fakeServer(t *testing.T) *sharetest.Target {
	t.Helper()
	return sharetest.Start(t, 32*1024*1024)
}

func TestShareOpenStatusDeleteFromThePipe(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL

	stdout, stderr, code := runCLI(t, "the launch code\n", "share", server)
	if code != 0 {
		t.Fatalf("share: exit %d, stderr %q", code, stderr)
	}
	// Piped: stdout is the link alone; the owner link goes to stderr.
	link := strings.TrimSpace(stdout)
	if !strings.HasPrefix(link, srv.URL+"/s#") || strings.Contains(link, "!") || strings.Contains(stdout, "\n") && strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "Opens once") || !strings.Contains(stderr, link+"!") {
		t.Errorf("stderr = %q", stderr)
	}
	ownerLink := ""
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, srv.URL) {
			ownerLink = line
		}
	}

	stdout, stderr, code = runCLI(t, "", "status", ownerLink)
	if code != 0 || !strings.Contains(stdout, "A one-time text secret.") || !strings.Contains(stdout, "Not opened yet.") {
		t.Errorf("status: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	// The text comes back exactly, newline included, with the description on stderr.
	stdout, stderr, code = runCLI(t, "", "open", link, "--yes")
	if code != 0 || stdout != "the launch code\n" {
		t.Fatalf("open: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "A one-time text secret, sent just now") {
		t.Errorf("open stderr = %q", stderr)
	}

	// Gone now: status says so and exits 4, for the owner in the owner's words
	// and without a time.
	stdout, _, code = runCLI(t, "", "status", ownerLink)
	if code != ExitGone || stdout != "Your secret was opened. It was a one-time secret, so nothing is left on the server.\n" {
		t.Errorf("status after open: exit %d, stdout %q", code, stdout)
	}
	_, stderr, code = runCLI(t, "", "open", link)
	if code != ExitGone || stderr != "secretli: This secret was already opened. If that wasn't you, tell the sender: the link may have reached someone else.\n" {
		t.Errorf("open after open: exit %d, stderr %q", code, stderr)
	}
}

func TestJSONAndFilesAndPasswords(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pass"), []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, "", "share", server, "--json", "--reusable", "-e", "1h", "--password-file", filepath.Join(dir, "pass"), filepath.Join(dir, "notes.txt"))
	if code != 0 {
		t.Fatalf("share: exit %d, stderr %q", code, stderr)
	}
	var shared struct {
		Link      string   `json:"link"`
		OwnerLink string   `json:"owner_link"`
		Opens     string   `json:"opens"`
		Password  bool     `json:"password"`
		Kind      string   `json:"kind"`
		Files     []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(stdout), &shared); err != nil {
		t.Fatalf("share json: %v in %q", err, stdout)
	}
	if shared.Opens != "until_expiry" || !shared.Password || shared.Kind != "files" || len(shared.Files) != 1 || shared.Files[0] != "notes.txt" {
		t.Errorf("shared = %+v", shared)
	}
	stdout, _, code = runCLI(t, "", "status", shared.OwnerLink, "--json")
	var status map[string]any
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != 0 || status["state"] != "live" || status["opened"] != false {
		t.Errorf("status before opening: exit %d, %v, %q", code, err, stdout)
	}

	// No password where one is needed, and a wrong one: exit 3 both times.
	_, stderr, code = runCLI(t, "", "open", shared.Link)
	if code != ExitPassword || !strings.Contains(stderr, "needs a password") {
		t.Errorf("open without password: exit %d, stderr %q", code, stderr)
	}
	t.Setenv("SECRETLI_PASSWORD", "wrong")
	_, stderr, code = runCLI(t, "", "open", shared.Link)
	if code != ExitPassword || !strings.Contains(stderr, "wrong password") {
		t.Errorf("open with wrong password: exit %d, stderr %q", code, stderr)
	}
	t.Setenv("SECRETLI_PASSWORD", "")

	out := filepath.Join(dir, "received")
	stdout, stderr, code = runCLI(t, "", "open", shared.Link, "--password-file", filepath.Join(dir, "pass"), "--out", out, "--json")
	if code != 0 {
		t.Fatalf("open: exit %d, stderr %q", code, stderr)
	}
	var opened struct {
		Kind  string `json:"kind"`
		Files []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(stdout), &opened); err != nil {
		t.Fatalf("open json: %v in %q", err, stdout)
	}
	if opened.Kind != "files" || len(opened.Files) != 1 || opened.Files[0].Name != "notes.txt" {
		t.Errorf("opened = %+v", opened)
	}
	got, err := os.ReadFile(filepath.Join(out, "notes.txt"))
	if err != nil || string(got) != "notes" {
		t.Errorf("saved file = %q, %v", got, err)
	}

	// Saving again refuses to overwrite; --force allows it; --stdout streams it.
	_, stderr, code = runCLI(t, "", "open", shared.Link, "--password-file", filepath.Join(dir, "pass"), "--out", out)
	if code != ExitError || !strings.Contains(stderr, "exists") {
		t.Errorf("overwrite: exit %d, stderr %q", code, stderr)
	}
	if _, _, code = runCLI(t, "", "open", shared.Link, "--password-file", filepath.Join(dir, "pass"), "--out", out, "--force"); code != 0 {
		t.Errorf("force: exit %d", code)
	}
	stdout, _, code = runCLI(t, "", "open", shared.Link, "--password-file", filepath.Join(dir, "pass"), "--stdout")
	if code != 0 || stdout != "notes" {
		t.Errorf("stdout mode: exit %d, stdout %q", code, stdout)
	}

	// Status as JSON, then deletion with --yes, then the story.
	stdout, _, code = runCLI(t, "", "status", shared.OwnerLink, "--json")
	status = nil
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != 0 || status["state"] != "live" || status["opened"] != true {
		t.Errorf("status: exit %d, %v, %q", code, err, stdout)
	}
	if _, ok := status["opened_at"]; ok {
		t.Errorf("status tells when it was opened: %q", stdout)
	}
	if _, stderr, code = runCLI(t, "", "delete", shared.Link, "--yes"); code != ExitError || !strings.Contains(stderr, "owner link") {
		t.Errorf("delete with the recipient link: exit %d, stderr %q", code, stderr)
	}
	if _, stderr, code = runCLI(t, "", "delete", shared.OwnerLink); code != ExitError || !strings.Contains(stderr, "--yes") {
		t.Errorf("delete without --yes off a terminal: exit %d, stderr %q", code, stderr)
	}
	if stdout, _, code = runCLI(t, "", "delete", shared.OwnerLink, "--yes", "--json"); code != 0 || !strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("delete: exit %d, stdout %q", code, stdout)
	}
	// Gone, the story is the outcome and whether the secret was one-time, and
	// no time and no one's name, here and in the error of any other command.
	stdout, _, code = runCLI(t, "", "status", shared.OwnerLink, "--json")
	want := map[string]any{"state": "gone", "outcome": "deleted", "one_time": false}
	status = nil
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != ExitGone || !reflect.DeepEqual(status, want) {
		t.Errorf("status after delete: exit %d, %v, %q", code, err, stdout)
	}
	stdout, _, code = runCLI(t, "", "open", shared.Link, "--json")
	status = nil
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != ExitGone || status["outcome"] != "deleted" || len(status) != 3 {
		t.Errorf("open after delete: exit %d, %v, %q", code, err, stdout)
	}
}

func TestUsageAndInputRules(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL

	if _, stderr, code := runCLI(t, "", "share", server, "--expires", "2h", "-t", "x"); code != ExitError || !strings.Contains(stderr, "5m, 10m") {
		t.Errorf("bad expiry: exit %d, stderr %q", code, stderr)
	}
	if _, stderr, code := runCLI(t, "", "share", server, "--bogus"); code != ExitUsage || !strings.Contains(stderr, "unknown flag") {
		t.Errorf("unknown flag: exit %d, stderr %q", code, stderr)
	}
	if _, stderr, code := runCLI(t, "", "share", server); code != ExitError || !strings.Contains(stderr, "nothing to share") {
		t.Errorf("empty stdin: exit %d, stderr %q", code, stderr)
	}
	if _, stderr, code := runCLI(t, "", "open", "https://secretli.app/share"); code != ExitError || !strings.Contains(stderr, "not a Secretli link") {
		t.Errorf("bad link: exit %d, stderr %q", code, stderr)
	}
	if _, stderr, code := runCLI(t, "", "share", server, "-p", "-t", "x"); code != ExitError || !strings.Contains(stderr, "SECRETLI_PASSWORD") {
		t.Errorf("password off a terminal: exit %d, stderr %q", code, stderr)
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, "", "share", server, "-t", "x", file); code != ExitError || !strings.Contains(stderr, "not both") {
		t.Errorf("text and files: exit %d, stderr %q", code, stderr)
	}
	if _, stderr, code := runCLI(t, "", "share", server, t.TempDir()); code != ExitError || !strings.Contains(stderr, "is a directory") {
		t.Errorf("directory: exit %d, stderr %q", code, stderr)
	}

	// stdin as a named file, quietly: just the link.
	stdout, stderr, code := runCLI(t, "payload", "share", server, "-q", "--name", "dump.bin", "-")
	if code != 0 || strings.Count(strings.TrimSpace(stdout), "\n") != 0 || stderr != "" {
		t.Fatalf("named stdin: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	stdout, _, code = runCLI(t, "", "status", strings.TrimSpace(stdout))
	if code != 0 || !strings.Contains(stdout, "set of files (dump.bin)") {
		t.Errorf("status: exit %d, stdout %q", code, stdout)
	}

	// The server's error, with the request id, when it is down.
	if _, stderr, code := runCLI(t, "", "share", "--server=http://127.0.0.1:1", "-t", "x"); code != ExitServer || !strings.Contains(stderr, "network error") {
		t.Errorf("server down: exit %d, stderr %q", code, stderr)
	}
}

func TestFormatting(t *testing.T) {
	now := time.Date(2026, 10, 5, 19, 53, 0, 0, time.Local) // a Monday
	cases := map[string]time.Time{
		"today at 23:30":          time.Date(2026, 10, 5, 23, 30, 0, 0, time.Local),
		"tomorrow at 00:10":       time.Date(2026, 10, 6, 0, 10, 0, 0, time.Local),
		"yesterday at 08:10":      time.Date(2026, 10, 4, 8, 10, 0, 0, time.Local),
		"on Thursday at 14:02":    time.Date(2026, 10, 1, 14, 2, 0, 0, time.Local),
		"on 28 Sep at 14:02":      time.Date(2026, 9, 28, 14, 2, 0, 0, time.Local),
		"on 31 Dec 2025 at 23:59": time.Date(2025, 12, 31, 23, 59, 0, 0, time.Local),
		"on 12 Oct at 19:53":      time.Date(2026, 10, 12, 19, 53, 0, 0, time.Local),
	}
	for want, at := range cases {
		if got := formatMoment(at, now); got != want {
			t.Errorf("formatMoment(%v) = %q, want %q", at, got, want)
		}
	}
	if got := formatSize(1536); got != "1.5 KB" {
		t.Errorf("formatSize = %q", got)
	}
	if got := formatAgo(now.Add(-3*time.Minute), now); got != "3 minutes ago" {
		t.Errorf("formatAgo = %q", got)
	}
	if got := safeName("../../etc/passwd"); got != "passwd" {
		t.Errorf("safeName = %q", got)
	}
	if got := safeName("..\\..\\x.txt"); got != "x.txt" {
		t.Errorf("safeName backslashes = %q", got)
	}
}
