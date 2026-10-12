package cli

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/secretli/format/go/bundle"
)

func TestDiskNames(t *testing.T) {
	long := strings.Repeat("ä", 200) + ".txt" // 404 bytes
	for _, tc := range []struct {
		in, unix, windows string
	}{
		{"notes.txt", "notes.txt", "notes.txt"},
		{"../../etc/passwd", "passwd", "passwd"},
		{`..\..\x.txt`, "x.txt", "x.txt"},
		{"/", "file", "file"},
		{".", "file", "file"},
		{"..", "file", "file"},
		{"", "file", "file"},
		{"line\nbreak\x1b.txt", "line_break_.txt", "line_break_.txt"},
		{"invoice\u202Etxt.exe", "invoice_txt.exe", "invoice_txt.exe"},
		{"bad\xffbyte", "bad_byte", "bad_byte"},
		{"👨\u200D👩.png", "👨\u200D👩.png", "👨\u200D👩.png"},
		{`a<b>c:d"e|f?g*h.txt`, `a<b>c:d"e|f?g*h.txt`, "a_b_c_d_e_f_g_h.txt"},
		{"stream.txt:hidden", "stream.txt:hidden", "stream.txt_hidden"},
		{"trailing. . ", "trailing. . ", "trailing"},
		{"CON", "CON", "_CON"},
		{"con.txt", "con.txt", "_con.txt"},
		{"NUL .tar.gz", "NUL .tar.gz", "_NUL .tar.gz"},
		{"COM1", "COM1", "_COM1"},
		{"lpt9.log", "lpt9.log", "_lpt9.log"},
		{"COM\u00B9", "COM\u00B9", "_COM\u00B9"},
		{"CONOUT$", "CONOUT$", "_CONOUT$"},
		{"COM10", "COM10", "COM10"},
		{"CONSOLE.txt", "CONSOLE.txt", "CONSOLE.txt"},
		{"...", "...", "file"},
		{long, strings.Repeat("ä", 125) + ".txt", strings.Repeat("ä", 125) + ".txt"},
	} {
		if got := diskNameFor(tc.in, false); got != tc.unix {
			t.Errorf("diskNameFor(%q, unix) = %q, want %q", tc.in, got, tc.unix)
		}
		if got := diskNameFor(tc.in, true); got != tc.windows {
			t.Errorf("diskNameFor(%q, windows) = %q, want %q", tc.in, got, tc.windows)
		}
	}
	for _, name := range []string{long, strings.Repeat("x", 300), strings.Repeat("y", 250) + "." + strings.Repeat("z", 40)} {
		for _, got := range []string{diskNameFor(name, false), numbered(diskNameFor(name, false), 12)} {
			if len(got) > maxNameBytes || !strings.HasPrefix(name, got[:10]) {
				t.Errorf("%d bytes: %q", len(got), got)
			}
		}
	}
	if got := numbered("report.final.pdf", 2); got != "report.final (2).pdf" {
		t.Errorf("numbered = %q", got)
	}
	if got := numbered(long, 3); got != strings.Repeat("ä", 123)+" (3).txt" {
		t.Errorf("numbered long = %q", got)
	}
}

// saveAll saves files with these names and contents as open does, without
// a terminal: every file is chosen.
func saveAll(t *testing.T, sink *fileSink, names []string) error {
	t.Helper()
	files := make([]bundle.Entry, len(names))
	for i, name := range names {
		files[i] = bundle.Entry{Index: i, Name: name, Size: int64(len(name))}
	}
	if err := sink.prepare(files, nil); err != nil {
		return err
	}
	for _, f := range files {
		w, err := sink.File(f)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, "content of "+f.Name)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// namesIn lists the names in dir.
func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Every file of a bundle gets a place before anything is written, so one
// awkward name doesn't stop the files after it.
func TestEveryFileGetsAPlace(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("n", 325)
	names := []string{"a.txt", ".", "..", "A.txt", long, "a.txt", "last.txt"}
	if err := saveAll(t, &fileSink{dir: dir}, names); err != nil {
		t.Fatal(err)
	}
	want := []string{"A (2).txt", "a (3).txt", "a.txt", "file", "file (2)", "last.txt", strings.Repeat("n", 255)}
	if got := namesIn(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("saved %q\nwant  %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a (3).txt")); string(b) != "content of a.txt" {
		t.Errorf("a (3).txt holds %q", b)
	}
}

// Of a reusable secret, which can be opened again, nothing is replaced
// without --force, a link that leads nowhere included, and that is found
// out before anything is written.
func TestExistingNamesAreRefusedFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	err := saveAll(t, &fileSink{dir: dir}, []string{"a.txt", "b.txt"})
	if err == nil || !strings.Contains(err.Error(), "b.txt exists") {
		t.Errorf("err = %v", err)
	}
	if got := namesIn(t, dir); !reflect.DeepEqual(got, []string{"b.txt"}) {
		t.Errorf("written before the refusal: %q", got)
	}
}

// A one-time secret is used up once it is opened, so a name that exists
// doesn't lose its file: it is saved under the next number free both in
// the bundle and on disk, and what was there stays as it was.
func TestOneTimeSecretsNumberNamesThatExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "a (2).txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("there before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The disk decides what counts as the same name, so this one matches in
	// case too: Linux tells b.txt from B.txt, macOS and Windows don't.
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := saveAll(t, &fileSink{dir: dir, oneTime: true}, []string{"a.txt", "b.txt", "a.txt", "c.txt"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"a.txt":     "there before",
		"a (2).txt": "there before",
		"a (3).txt": "content of a.txt",
		"a (4).txt": "content of a.txt",
		"b (2).txt": "content of b.txt",
		"c.txt":     "content of c.txt",
	}
	for name, content := range want {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != content {
			t.Errorf("%s holds %q, %v", name, b, err)
		}
	}
	if got := len(namesIn(t, dir)); got != len(want)+1 {
		t.Errorf("%d entries in the directory, want %d: %q", got, len(want)+1, namesIn(t, dir))
	}
}

// --force replaces a file, and a link or a hard link, with a new file: it
// never writes through one into the file it leads to.
func TestForceReplacesLinksInsteadOfWritingThroughThem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "target-secret.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "normal.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveAll(t, &fileSink{dir: dir, force: true}, []string{"normal.txt", "hard.txt", "plain.txt"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep me" {
		t.Errorf("the file a link led to now holds %q", b)
	}
	for _, name := range []string{"normal.txt", "hard.txt", "plain.txt"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: %v, %v", name, info, err)
			continue
		}
		if b, _ := os.ReadFile(path); string(b) != "content of "+name {
			t.Errorf("%s holds %q", name, b)
		}
	}
}

// A directory is not a file --force could replace; that is said before
// anything is written.
func TestForceRefusesADirectoryFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "b.txt"), 0o750); err != nil {
		t.Fatal(err)
	}
	err := saveAll(t, &fileSink{dir: dir, force: true}, []string{"a.txt", "b.txt"})
	if err == nil || !strings.Contains(err.Error(), "b.txt is a directory") {
		t.Errorf("err = %v", err)
	}
	if got := namesIn(t, dir); !reflect.DeepEqual(got, []string{"b.txt"}) {
		t.Errorf("written before the refusal: %q", got)
	}
}

// Two files of a secret with the same name are both saved, and the command
// says under which name.
func TestOpenSavesFilesOfTheSameName(t *testing.T) {
	srv := fakeServer(t)
	first, second := t.TempDir(), t.TempDir()
	for dir, content := range map[string]string{first: "the first", second: "the second"} {
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, code := runCLI(t, "", "share", "--server="+srv.URL, filepath.Join(first, "notes.txt"), filepath.Join(second, "notes.txt"))
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	out := t.TempDir()
	_, stderr, code = runCLI(t, "", "open", strings.TrimSpace(stdout), "--yes", "--out", out)
	if code != 0 || !strings.Contains(stderr, "notes.txt as notes (2).txt") {
		t.Fatalf("open: exit %d, stderr %q", code, stderr)
	}
	for name, want := range map[string]string{"notes.txt": "the first", "notes (2).txt": "the second"} {
		if b, err := os.ReadFile(filepath.Join(out, name)); err != nil || string(b) != want {
			t.Errorf("%s holds %q, %v", name, b, err)
		}
	}
}

// Opening a one-time secret into a directory that has its file already
// saves it under a new number and says so; a reusable one is refused and
// can be opened again.
func TestOpenNumbersAFileThatExistsOnlyForAOneTimeSecret(t *testing.T) {
	srv := fakeServer(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("the secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "notes.txt"), []byte("there before"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, "", "share", "--server="+srv.URL, "--reusable", filepath.Join(src, "notes.txt"))
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	if _, stderr, code = runCLI(t, "", "open", strings.TrimSpace(stdout), "--yes", "--out", out); code == 0 || !strings.Contains(stderr, "notes.txt exists") {
		t.Fatalf("open reusable: exit %d, stderr %q", code, stderr)
	}

	stdout, stderr, code = runCLI(t, "", "share", "--server="+srv.URL, filepath.Join(src, "notes.txt"))
	if code != 0 {
		t.Fatalf("share: exit %d, %q", code, stderr)
	}
	if _, stderr, code = runCLI(t, "", "open", strings.TrimSpace(stdout), "--yes", "--out", out); code != 0 || !strings.Contains(stderr, "notes.txt as notes (2).txt") {
		t.Fatalf("open one-time: exit %d, stderr %q", code, stderr)
	}
	for name, want := range map[string]string{"notes.txt": "there before", "notes (2).txt": "the secret"} {
		if b, err := os.ReadFile(filepath.Join(out, name)); err != nil || string(b) != want {
			t.Errorf("%s holds %q, %v", name, b, err)
		}
	}
}
