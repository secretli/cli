package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/format/bundle"
)

type openOptions struct {
	out          string
	toStdout     bool
	force        bool
	password     bool
	passwordFile string
	yes          bool
	copy         bool
}

// errNotOpened is a one-time secret the person chose not to open yet.
var errNotOpened = errors.New("not opened")

// errCopyIsForText is --copy on a secret that holds files, found out
// before it is opened.
var errCopyIsForText = errors.New("--copy is for text, and this secret holds files; leave out --copy to save them")

func newOpenCmd(e *env) *cobra.Command {
	var o openOptions
	cmd := &cobra.Command{
		Use:   "open [link]",
		Short: "Open a secret: text to stdout, files to disk",
		Long: `Opens a link. Text is written to stdout exactly as it was shared; files are
saved to the current directory, or to --out, and never overwrite anything
unless --force is given.

Of a secret with several files, at a terminal, the files are listed and you
are asked which to save: Enter saves them all, and numbers, ranges and
names or patterns pick some, as in 2, 1 3, 1-2 or *.jpg. --stdout asks for
the one to write. With --yes or --json, or where there is no terminal to
ask on, nothing is asked: every file is saved, and --stdout needs a secret
of a single file.

--copy puts text on the clipboard instead, without its final line break,
so it never shows in the terminal or its scrollback. The clipboard is
cleared 45 seconds later, unless something else was copied by then.

Opening a one-time secret is what uses it up, so the link is described first
and you are asked before it is opened. --yes opens it without asking, and is
needed where there is no terminal to ask on. Reusable secrets open right away.
Without an argument the link is read from stdin, or asked for at a terminal,
which keeps it out of your shell history.`,
		Example: `  secretli open 'https://secretli.app/s#…'
  secretli open 'https://secretli.app/s#…' --out ./received
  secretli open 'https://secretli.app/s#…' --stdout > backup.tgz
  secretli open 'https://secretli.app/s#…' --copy
  pbpaste | secretli open`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			err := e.open(cmd.Context(), o, args)
			if errors.Is(err, errNotOpened) {
				e.note("Not opened; the link still works.\n")
				return &exitWith{code: ExitError}
			}
			return err
		}),
	}
	addOpenFlags(cmd, &o)
	return cmd
}

// addOpenFlags adds what opening a secret takes; receive opens one too.
func addOpenFlags(cmd *cobra.Command, o *openOptions) {
	f := cmd.Flags()
	f.StringVarP(&o.out, "out", "o", ".", "the directory files are saved to")
	f.BoolVar(&o.toStdout, "stdout", false, "write a single file to stdout instead of saving it")
	f.BoolVar(&o.force, "force", false, "overwrite files that already exist")
	f.BoolVarP(&o.password, "password", "p", false, "the secret has a password (asked for, or SECRETLI_PASSWORD)")
	f.StringVar(&o.passwordFile, "password-file", "", "read the password from the first line of this file")
	f.BoolVarP(&o.yes, "yes", "y", false, "open a one-time secret without asking; needed when not at a terminal")
	f.BoolVarP(&o.copy, "copy", "c", false, "copy text to the clipboard instead of printing it; cleared after 45 seconds")
	_ = cmd.MarkFlagDirname("out")
}

// checkCopy refuses --copy where it cannot work. It runs before the link or
// the code is taken, so that nothing is used up for a refusal.
func (e *env) checkCopy(o openOptions) error {
	if !o.copy {
		return nil
	}
	switch {
	case o.toStdout:
		return errors.New("--stdout writes a file and --copy copies text; use one of them")
	case e.json:
		return errors.New("--json prints the secret; leave out --copy")
	}
	if err := clipboard.Available(); err != nil {
		return fmt.Errorf("--copy: %w", err)
	}
	return nil
}

func (e *env) open(ctx context.Context, o openOptions, args []string) error {
	if err := e.checkCopy(o); err != nil {
		return err
	}
	link, err := e.linkArg(ctx, args)
	if err != nil {
		return err
	}
	return e.openLink(ctx, o, link)
}

// openLink describes the secret behind a link, then opens it.
func (e *env) openLink(ctx context.Context, o openOptions, link share.Link) error {
	c := e.client(link.Origin)
	now := time.Now()
	info, err := share.Inspect(ctx, c, link)
	if err != nil {
		return err
	}
	e.say("%s\n", describeInfo(info, link.IsOwner(), now))
	if o.copy && info.Kind != share.KindText {
		return errCopyIsForText
	}
	if !info.Reusable && !o.yes {
		if err := e.confirmOpen(ctx, link.IsOwner()); err != nil {
			return err
		}
	}

	source := passwordSource{flag: o.password, file: o.passwordFile}
	password, err := source.resolve(ctx, e, info.PasswordProtected, false)
	if err != nil {
		if info.PasswordProtected {
			return fmt.Errorf("%w: %w", share.ErrPasswordRequired, err)
		}
		return err
	}

	sink := &fileSink{e: e, dir: o.out, toStdout: o.toStdout, force: o.force, ask: !o.yes && !e.json, oneTime: !info.Reusable}
	bar := e.progress("Downloading and decrypting…")
	for attempt := 1; ; attempt++ {
		opened, err := share.Open(ctx, c, link, password, sink, bar.update)
		bar.finish()
		if err == nil {
			return e.printOpened(opened, sink, o.copy)
		}
		// A mistyped password costs nothing: the server only hands out the
		// secret once the right token arrives. Ask again at a terminal.
		if errors.Is(err, share.ErrWrongPassword) && source.file == "" && os.Getenv("SECRETLI_PASSWORD") == "" && attempt < 3 {
			if t, terr := e.terminal(); terr == nil {
				e.note("Wrong password. Try again.\n")
				password, err = t.askSecret(ctx, "Password: ")
				t.close()
				if err == nil && password != "" {
					continue
				}
			}
		}
		return err
	}
}

// confirmOpen asks before a one-time secret is used up, which cannot be
// undone. It returns errNotOpened for a no; without a terminal to ask on,
// --yes has to answer.
func (e *env) confirmOpen(ctx context.Context, owner bool) error {
	t, err := e.terminal()
	if err != nil {
		return errors.New("this secret opens only once; use --yes to open it without being asked")
	}
	defer t.close()
	warning := "It opens only once; after that the link stops working."
	if owner {
		warning = "It opens only once, so the person you sent it to won't be able to."
	}
	ok, err := t.confirm(ctx, "Open it now? "+t.paint(yellow, warning))
	if err != nil {
		return err
	}
	if !ok {
		return errNotOpened
	}
	return nil
}

// linkArg takes the link from the arguments, from stdin, or from a prompt.
func (e *env) linkArg(ctx context.Context, args []string) (share.Link, error) {
	raw := ""
	switch {
	case len(args) == 1:
		raw = args[0]
	case !e.stdinTTY:
		line, err := interruptible(ctx, func() (string, error) { return bufio.NewReader(e.stdin).ReadString('\n') })
		if ctx.Err() != nil {
			return share.Link{}, ctx.Err()
		}
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return share.Link{}, errors.New("no link given, and nothing on stdin")
		}
		raw = line
	default:
		t, err := e.terminal()
		if err != nil {
			return share.Link{}, errors.New("no link given")
		}
		defer t.close()
		if raw, err = t.askLine(ctx, "Paste the link: "); err != nil {
			return share.Link{}, err
		}
	}
	link, err := share.ParseLink(raw)
	if err != nil {
		return share.Link{}, fmt.Errorf("%w: expected https://host/s#… as the web app prints it", err)
	}
	return link, nil
}

// fileSink writes a text secret to stdout and files to a directory, or a
// single file to stdout.
type fileSink struct {
	e        *env
	dir      string
	toStdout bool
	force    bool
	// ask allows asking which of several files to save, at a terminal.
	ask bool
	// oneTime is a secret whose files are gone once it is opened.
	oneTime bool

	text  []byte
	saved []savedFile
}

type savedFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"path,omitempty"`
}

// Choose picks the files to save: every one, or, at a terminal and when
// there are several, the ones named in the answer. Before anything is
// written it checks that none of the chosen files would overwrite one.
func (s *fileSink) Choose(ctx context.Context, files []bundle.Entry, deadline time.Time) ([]int, error) {
	var t *terminal
	if s.ask && len(files) > 1 {
		if tt, err := s.e.terminal(); err == nil {
			t = tt
			defer t.close()
		}
	}
	if s.toStdout {
		switch {
		case len(files) == 1:
			return nil, nil
		case t == nil:
			return nil, fmt.Errorf("--stdout is for a single file, and this secret has %d", len(files))
		}
		return s.pick(ctx, t, files, deadline, "Which one? ", func(chosen []int) error {
			if len(chosen) != 1 {
				return errors.New("--stdout writes a single file; pick one")
			}
			return nil
		})
	}
	if t == nil {
		return nil, s.prepare(files, nil)
	}
	return s.pick(ctx, t, files, deadline, "Save which? [Enter = all, or e.g. 2 · 1 3 · 1-2 · *.jpg] ", func(chosen []int) error {
		return s.prepare(files, chosen)
	})
}

// pick lists the files on the terminal and asks until the answer names
// files that check accepts. Of a one-time secret, it says first that the
// files not saved are gone, and by when they have to be saved, if the
// download goes on after the question.
func (s *fileSink) pick(ctx context.Context, t *terminal, files []bundle.Entry, deadline time.Time, question string, check func(chosen []int) error) ([]int, error) {
	listFiles(t.out, files)
	if s.oneTime {
		when := "now"
		if !deadline.IsZero() {
			when = "by " + formatBy(deadline, time.Now())
		}
		_, _ = fmt.Fprintln(t.out, t.paint(yellow, "Files you don't save "+when+" are gone with this one-time secret."))
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	for {
		answer, err := t.askLine(ctx, question)
		if err != nil {
			return nil, err
		}
		chosen, err := pickFiles(answer, names)
		if err == nil {
			err = check(chosen)
		}
		if err == nil {
			return chosen, nil
		}
		_, _ = fmt.Fprintln(t.out, err)
	}
}

// prepare makes the output directory and checks that none of the chosen
// files, nil meaning all, exists there.
func (s *fileSink) prepare(files []bundle.Entry, chosen []int) error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	if s.force {
		return nil
	}
	if chosen == nil {
		chosen = make([]int, len(files))
		for i := range chosen {
			chosen[i] = i
		}
	}
	for _, i := range chosen {
		path := filepath.Join(s.dir, safeName(files[i].Name))
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists; use --force to overwrite, or --out for another directory", path)
		}
	}
	return nil
}

func (s *fileSink) Text(text []byte) error {
	s.text = text
	return nil
}

func (s *fileSink) File(f bundle.Entry) (io.WriteCloser, error) {
	if s.toStdout {
		s.saved = append(s.saved, savedFile{Name: f.Name, Size: f.Size})
		return nopWriteCloser{s.e.stdout}, nil
	}
	path := filepath.Join(s.dir, safeName(f.Name))
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !s.force {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600) //nolint:gosec // the name is sanitised and confined to the output directory
	if err != nil {
		return nil, err
	}
	s.saved = append(s.saved, savedFile{Name: f.Name, Size: f.Size, Path: path})
	return file, nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// safeName keeps a shared file's name from leaving the output directory.
func safeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(filepath.Clean("/" + name))
	if name == "" || name == "/" || name == "." {
		return "file"
	}
	return name
}

func (e *env) printOpened(opened *share.Opened, sink *fileSink, copyText bool) error {
	if e.json {
		out := map[string]any{
			"kind":       opened.Info.Kind,
			"reusable":   opened.Info.Reusable,
			"password":   opened.Info.PasswordProtected,
			"expires_at": opened.Info.ExpiresAt.UTC(),
			"created_at": opened.Info.CreatedAt.UTC(),
		}
		if opened.Info.Kind == share.KindText && sink.text != nil {
			out["text"] = string(sink.text)
		} else {
			out["files"] = sink.saved
		}
		return e.emitJSON(out)
	}
	if opened.Info.Kind == share.KindText && sink.text != nil {
		if copyText {
			return e.copySecret(string(sink.text), "the secret")
		}
		if _, err := e.stdout.Write(sink.text); err != nil {
			return err
		}
		if e.stdoutTTY && len(sink.text) > 0 && sink.text[len(sink.text)-1] != '\n' {
			_, _ = fmt.Fprintln(e.stdout)
		}
		return nil
	}
	if sink.toStdout {
		return nil
	}
	names := make([]string, 0, len(sink.saved))
	for _, f := range sink.saved {
		names = append(names, fmt.Sprintf("%s (%s)", printable(f.Name), formatSize(f.Size)))
	}
	e.done("Saved %s to %s.\n", strings.Join(names, ", "), sink.dir)
	return nil
}
