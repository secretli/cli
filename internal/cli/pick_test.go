package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share/sharetest"
	"github.com/secretli/format/bundle"
)

func TestPickFiles(t *testing.T) {
	names := []string{"holiday.mov", "notes.txt", "IMG_0001.JPG", "IMG_0002.jpg", "file[1].txt", "2"}
	tests := []struct {
		answer string
		want   []int
	}{
		{"", []int{0, 1, 2, 3, 4, 5}},
		{"   ", []int{0, 1, 2, 3, 4, 5}},
		{"2", []int{1}},
		{"1 3", []int{0, 2}},
		{"3,1", []int{0, 2}},
		{"1, 3", []int{0, 2}},
		{"2-4", []int{1, 2, 3}},
		{"4-2", []int{1, 2, 3}},
		{"1 1 1-2", []int{0, 1}},
		{"*.jpg", []int{2, 3}},
		{"img_*", []int{2, 3}},
		{"notes.txt", []int{1}},
		{"NOTES.TXT", []int{1}},
		{"*.txt 1", []int{0, 1, 4}},
		{"file[1].txt", []int{4}},
		{"6", []int{5}},
		{"*", []int{0, 1, 2, 3, 4, 5}},
	}
	for _, tt := range tests {
		got, err := pickFiles(tt.answer, names)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("pickFiles(%q) = %v, %v; want %v", tt.answer, got, err, tt.want)
		}
	}
}

func TestPickFilesRefusesWhatIsNotThere(t *testing.T) {
	names := []string{"a.txt", "b.txt", "c.txt"}
	tests := []struct {
		answer, want string
	}{
		{"0", "there is no file 0; the files are numbered 1 to 3"},
		{"4", "there is no file 4; the files are numbered 1 to 3"},
		{"2-9", "there is no file 2-9; the files are numbered 1 to 3"},
		{"*.png", `no file is called "*.png"`},
		{"1 d.txt", `no file is called "d.txt"`},
		{"1-", `no file is called "1-"`},
		{"[", `no file is called "["`},
	}
	for _, tt := range tests {
		got, err := pickFiles(tt.answer, names)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("pickFiles(%q) = %v, %v; want the error %q", tt.answer, got, err, tt.want)
		}
	}
}

func TestListFiles(t *testing.T) {
	var out strings.Builder
	listFiles(&out, []bundle.Entry{
		{Name: "a.txt", Size: 5},
		{Name: "holiday.mov", Size: 2621440},
		{Name: "red\x1b[31m", Size: 1536},
	})
	want := "" +
		"  1  a.txt           5 B\n" +
		"  2  holiday.mov  2.5 MB\n" +
		"  3  red?[31m     1.5 KB\n"
	if out.String() != want {
		t.Errorf("list:\n%s\nwant:\n%s", out.String(), want)
	}

	// A long list shows its first files and says how many more there are.
	many := make([]bundle.Entry, 2000)
	for i := range many {
		many[i] = bundle.Entry{Index: i, Name: fmt.Sprintf("IMG_%04d.jpg", i+1), Size: 1024}
	}
	out.Reset()
	listFiles(&out, many)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 31 || lines[0] != "   1  IMG_0001.jpg  1.0 KB" || lines[29] != "  30  IMG_0030.jpg  1.0 KB" || lines[30] != "  … and 1,970 more" {
		t.Errorf("long list: %d lines, first %q, last two %q", len(lines), lines[0], lines[len(lines)-2:])
	}

	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1970: "1,970", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

// shareFiles shares files that hold "this is " and their name, and returns
// the link.
func shareFiles(t *testing.T, server string, names []string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	args := append([]string{"share", server}, extra...)
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("this is "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, path)
	}
	stdout, stderr, code := runCLI(t, "", args...)
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	return strings.TrimSpace(stdout)
}

// savedIn lists the files in dir and checks that each holds what
// shareFiles put in it.
func savedIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		if got, err := os.ReadFile(filepath.Join(dir, entry.Name())); err != nil || string(got) != "this is "+entry.Name() {
			t.Errorf("%s holds %q, %v", entry.Name(), got, err)
		}
	}
	return names
}

func TestOpenAsksWhichFilesToSave(t *testing.T) {
	srv := fakeServer(t)
	link := shareFiles(t, "--server="+srv.URL, []string{"a.txt", "b.jpg", "c.jpg"}, "--reusable")
	question := "Save which? [Enter = all, or e.g. 2 · 1 3 · 1-2 · *.jpg] "

	t.Run("a subset", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out")
		asked := answeringInTurn(t, "1 3")
		_, stderr, code := runCLI(t, "", "open", link, "--out", out)
		if code != 0 || !strings.Contains(stderr, "Saved a.txt (13 B), c.jpg (13 B) to "+out+".") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"a.txt", "c.jpg"}) {
			t.Errorf("saved %v", got)
		}
		want := "  1  a.txt  13 B\n  2  b.jpg  13 B\n  3  c.jpg  13 B\n" + question
		if asked.String() != want {
			t.Errorf("asked %q, want %q", asked.String(), want)
		}
	})

	t.Run("Enter saves all", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out")
		answeringInTurn(t, "")
		if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"a.txt", "b.jpg", "c.jpg"}) {
			t.Errorf("saved %v", got)
		}
	})

	t.Run("a wrong answer asks again", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out")
		asked := answeringInTurn(t, "4", "*.png", "*.JPG")
		if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"b.jpg", "c.jpg"}) {
			t.Errorf("saved %v", got)
		}
		if strings.Count(asked.String(), question) != 3 ||
			!strings.Contains(asked.String(), question+"there is no file 4; the files are numbered 1 to 3\n") ||
			!strings.Contains(asked.String(), question+"no file is called \"*.png\"\n") {
			t.Errorf("asked %q", asked.String())
		}
	})

	// Only the chosen files are checked, and before anything is written:
	// at the question, a file that exists is refused like a wrong answer.
	t.Run("only the chosen files must not exist", func(t *testing.T) {
		out := t.TempDir()
		if err := os.WriteFile(filepath.Join(out, "a.txt"), []byte("this is a.txt"), 0o600); err != nil {
			t.Fatal(err)
		}
		asked := answeringInTurn(t, "", "2-3")
		if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"a.txt", "b.jpg", "c.jpg"}) {
			t.Errorf("saved %v", got)
		}
		if !strings.Contains(asked.String(), question+filepath.Join(out, "a.txt")+" exists; use --force") {
			t.Errorf("asked %q", asked.String())
		}

		// Without a terminal every file is chosen, so the command refuses.
		out = t.TempDir()
		if err := os.WriteFile(filepath.Join(out, "c.jpg"), []byte("this is c.jpg"), 0o600); err != nil {
			t.Fatal(err)
		}
		openTTY = func() (*terminal, error) { return nil, errNoTerminal }
		if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != ExitError || !strings.Contains(stderr, "c.jpg exists") {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"c.jpg"}) {
			t.Errorf("saved %v before refusing", got)
		}
	})
}

func TestOneTimeFilesWarnBeforeTheQuestion(t *testing.T) {
	srv := fakeServer(t)
	link := shareFiles(t, "--server="+srv.URL, []string{"a.txt", "b.jpg"})
	out := filepath.Join(t.TempDir(), "out")
	asked := answeringInTurn(t, "y", "b.jpg")
	if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"b.jpg"}) {
		t.Errorf("saved %v", got)
	}
	if !strings.Contains(asked.String(), "[y/N]   1  a.txt  13 B\n  2  b.jpg  13 B\nFiles you don't save now are gone with this one-time secret.\nSave which?") {
		t.Errorf("asked %q", asked.String())
	}
}

// shareLargeFiles shares a.txt, which holds "this is a.txt", and big.bin,
// which makes the secret too large to be fetched whole, so its files are
// downloaded after the question. It returns the link and big.bin's content.
func shareLargeFiles(t *testing.T, server string) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	big := bytes.Repeat([]byte("this is big.bin\n"), 80*1024)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("this is a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "", "share", server, filepath.Join(dir, "a.txt"), filepath.Join(dir, "big.bin"))
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	return strings.TrimSpace(stdout), big
}

// deadlineWarning is the warning before the question for a one-time secret
// whose files are downloaded after it, as it reads for a secret opened
// between from and to: the download may go on for 15 minutes.
func deadlineWarning(from, to time.Time) []string {
	var warnings []string
	for _, at := range []time.Time{from, to} {
		warnings = append(warnings, "Files you don't save by "+formatBy(at.Add(15*time.Minute), at)+" are gone with this one-time secret.\n")
	}
	return warnings
}

// A one-time secret downloaded after the question says by when its files
// have to be saved.
func TestLargeOneTimeFilesWarnOfTheDeadline(t *testing.T) {
	srv := fakeServer(t)
	link, big := shareLargeFiles(t, "--server="+srv.URL)
	out := filepath.Join(t.TempDir(), "out")
	asked := answeringInTurn(t, "y", "2")
	from := time.Now()
	if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	warnings := deadlineWarning(from, time.Now())
	if !strings.Contains(asked.String(), "big.bin  1.2 MB\n"+warnings[0]+"Save which?") && !strings.Contains(asked.String(), "big.bin  1.2 MB\n"+warnings[1]+"Save which?") {
		t.Errorf("asked %q, want the warning %q", asked.String(), warnings)
	}
	if got, err := os.ReadFile(filepath.Join(out, "big.bin")); err != nil || !bytes.Equal(got, big) {
		t.Errorf("big.bin holds %d bytes, %v", len(got), err)
	}
}

func TestFilesAreSavedWithoutAQuestion(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	names := []string{"a.txt", "b.jpg", "c.jpg"}

	t.Run("--yes", func(t *testing.T) {
		link := shareFiles(t, server, names)
		out := filepath.Join(t.TempDir(), "out")
		asked := answeringInTurn(t, "1")
		if _, stderr, code := runCLI(t, "", "open", link, "--out", out, "--yes"); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, names) || asked.Len() != 0 {
			t.Errorf("saved %v, asked %q", got, asked.String())
		}
	})

	t.Run("no terminal", func(t *testing.T) {
		link := shareFiles(t, server, names, "--reusable")
		out := filepath.Join(t.TempDir(), "out")
		if _, stderr, code := runCLI(t, "", "open", link, "--out", out); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, names) {
			t.Errorf("saved %v", got)
		}
	})

	t.Run("--json", func(t *testing.T) {
		link := shareFiles(t, server, names, "--reusable")
		out := filepath.Join(t.TempDir(), "out")
		asked := answeringInTurn(t, "1")
		stdout, stderr, code := runCLI(t, "", "open", link, "--out", out, "--json")
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		var opened struct {
			Files []struct {
				Name string `json:"name"`
			} `json:"files"`
		}
		if err := json.Unmarshal([]byte(stdout), &opened); err != nil || len(opened.Files) != 3 {
			t.Errorf("json %q, %v", stdout, err)
		}
		if got := savedIn(t, out); !reflect.DeepEqual(got, names) || asked.Len() != 0 {
			t.Errorf("saved %v, asked %q", got, asked.String())
		}
	})

	t.Run("text", func(t *testing.T) {
		link, _ := shareText(t, server, "just text\n", "--reusable")
		asked := answeringInTurn(t, "1")
		if stdout, stderr, code := runCLI(t, "", "open", link); code != 0 || stdout != "just text\n" || asked.Len() != 0 {
			t.Errorf("exit %d, stdout %q, stderr %q, asked %q", code, stdout, stderr, asked.String())
		}
	})
}

func TestStdoutTakesOneOfSeveralFiles(t *testing.T) {
	srv := fakeServer(t)
	link := shareFiles(t, "--server="+srv.URL, []string{"a.txt", "b.jpg", "c.jpg"}, "--reusable")

	// At a terminal it asks which one, until the answer is exactly one.
	asked := answeringInTurn(t, "", "1 2", "c.jpg")
	stdout, stderr, code := runCLI(t, "", "open", link, "--stdout")
	if code != 0 || stdout != "this is c.jpg" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if strings.Count(asked.String(), "Which one? ") != 3 || strings.Count(asked.String(), "--stdout writes a single file; pick one\n") != 2 || !strings.Contains(asked.String(), "  3  c.jpg  13 B\n") {
		t.Errorf("asked %q", asked.String())
	}

	// Without one it can't ask.
	openTTY = func() (*terminal, error) { return nil, errNoTerminal }
	if _, stderr, code := runCLI(t, "", "open", link, "--stdout"); code != ExitError || !strings.Contains(stderr, "--stdout is for a single file, and this secret has 3") {
		t.Errorf("without a terminal: exit %d, stderr %q", code, stderr)
	}
}

func TestReceiveAsksWhichFilesToSave(t *testing.T) {
	srv := fakeServer(t)
	server := "--server=" + srv.URL
	link := shareFiles(t, server, []string{"a.txt", "b.jpg"})
	sender := startCLI(t, "", "send", link)
	transferCode := firstLine(t, sender)

	out := filepath.Join(t.TempDir(), "out")
	asked := answeringInTurn(t, "y", "1")
	if _, stderr, code := runCLI(t, "", "receive", transferCode, server, "--out", out); code != 0 {
		t.Fatalf("receive: exit %d, stderr %q", code, stderr)
	}
	if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"a.txt"}) || !strings.Contains(asked.String(), "Save which?") {
		t.Errorf("saved %v, asked %q", got, asked.String())
	}
	if code := exitOf(t, sender); code != 0 {
		t.Errorf("send: exit %d", code)
	}
}

func TestOpenPicksFromBundleVersion3(t *testing.T) {
	srv := fakeServer(t)
	var files []bundle.Source
	for _, name := range []string{"a.txt", "b.jpg", "c.jpg"} {
		content := "this is " + name
		files = append(files, bundle.Source{Name: name, Size: int64(len(content)), Reader: strings.NewReader(content)})
	}
	link := sharetest.ShareStream(t, srv.URL, "bundle", true, files...)

	out := filepath.Join(t.TempDir(), "out")
	answeringInTurn(t, "2")
	if _, stderr, code := runCLI(t, "", "open", link.Recipient().String(), "--out", out); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := savedIn(t, out); !reflect.DeepEqual(got, []string{"b.jpg"}) {
		t.Errorf("saved %v", got)
	}
}

// Ctrl-C at the question ends the command, and nothing is saved.
func TestAnInterruptEndsTheQuestion(t *testing.T) {
	srv := fakeServer(t)
	link := shareFiles(t, "--server="+srv.URL, []string{"a.txt", "b.jpg"}, "--reusable")

	// The terminal never answers; what is asked on it goes to a file, which
	// the test can read while the command waits.
	tty, never, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	defer never.Close()
	dir := t.TempDir()
	asked, _ := os.Create(filepath.Join(dir, "asked"))
	defer asked.Close()
	openTTY = func() (*terminal, error) { return &terminal{in: tty, out: asked, fd: int(tty.Fd())}, nil }
	t.Cleanup(func() { openTTY = func() (*terminal, error) { return nil, errNoTerminal } })

	stdin, _ := os.Create(filepath.Join(dir, "stdin"))
	stdout, _ := os.Create(filepath.Join(dir, "stdout"))
	stderr, _ := os.Create(filepath.Join(dir, "stderr"))
	defer stdin.Close()
	defer stdout.Close()
	defer stderr.Close()
	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- execute(ctx, []string{"open", link, "--out", out}, stdin, stdout, stderr) }()

	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, _ := os.ReadFile(filepath.Join(dir, "asked")); strings.Contains(string(b), "Save which?") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never asked which files to save")
		}
	}
	cancel()
	select {
	case code := <-done:
		said, _ := os.ReadFile(filepath.Join(dir, "stderr"))
		if code != ExitError || !strings.Contains(string(said), "interrupted") {
			t.Errorf("exit %d, stderr %q", code, said)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for an answer after the interrupt")
	}
	if got := savedIn(t, out); len(got) != 0 {
		t.Errorf("saved %v", got)
	}
}
