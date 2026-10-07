package cli

import (
	"fmt"
	"io"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// printQR draws the link as a QR code, for a phone to scan off the screen.
func printQR(w io.Writer, text string) error {
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("make QR code: %w", err)
	}
	_, err = io.WriteString(w, q.ToSmallString(false))
	return err
}

// progressBar redraws one line on stderr while bytes move. It stays quiet
// for small transfers and when stderr is not a terminal.
type progressBar struct {
	w       io.Writer
	label   string
	started time.Time
	last    time.Time
	shown   bool
}

const progressThreshold = 1024 * 1024

func (e *env) progress(label string) *progressBar {
	if e.quiet || !e.stderrTTY {
		return nil
	}
	return &progressBar{w: e.stderr, label: label, started: time.Now()}
}

func (p *progressBar) update(done, total int64) {
	if p == nil || total < progressThreshold {
		return
	}
	now := time.Now()
	if done < total && now.Sub(p.last) < 100*time.Millisecond {
		return
	}
	p.last = now
	p.shown = true
	percent := int64(100)
	if total > 0 {
		percent = done * 100 / total
	}
	_, _ = fmt.Fprintf(p.w, "\r\033[K%s %d%% (%s of %s)", p.label, percent, formatSize(done), formatSize(total))
}

func (p *progressBar) finish() {
	if p == nil || !p.shown {
		return
	}
	_, _ = fmt.Fprint(p.w, "\r\033[K")
}
