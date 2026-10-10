package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/term"
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

// progressBar redraws one line on stderr while bytes move: how far along,
// and once it can tell, how fast and how long is left. It stays quiet for
// small transfers, with --quiet or --json, and when stderr is not a
// terminal that takes escape codes.
type progressBar struct {
	w     io.Writer
	label string
	// width is the terminal's, or 0 when unknown: a longer line would wrap,
	// and a wrapped line can't be redrawn in place.
	width int

	last  time.Time
	shown bool

	// samples are how far the transfer was at times at least
	// sampleInterval apart, over the last rateWindow and one from before.
	samples []progressSample
}

type progressSample struct {
	at   time.Time
	done int64
}

const (
	progressThreshold = 1024 * 1024
	// The speed is what moved over the last few seconds: long enough to
	// even out parts and ranges that arrive in bursts, short enough to
	// follow a change.
	sampleInterval = 250 * time.Millisecond
	rateWindow     = 5 * time.Second
	// The speed is shown once it rests on this much time or this much
	// data.
	rateAfter      = time.Second
	rateAfterBytes = 4 * 1024 * 1024
)

func (e *env) progress(label string) *progressBar {
	if e.quiet || e.json || !e.stderrEscapes {
		return nil
	}
	p := &progressBar{w: e.stderr, label: label}
	if f, ok := e.stderr.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil {
			p.width = width
		}
	}
	return p
}

func (p *progressBar) update(done, total int64) {
	if p == nil || total < progressThreshold {
		return
	}
	now := time.Now()
	p.sample(done, now)
	if done < total && now.Sub(p.last) < 100*time.Millisecond {
		return
	}
	p.last = now
	p.shown = true
	percent := int64(100)
	if total > 0 {
		percent = done * 100 / total
	}
	line := fmt.Sprintf("%s %d%% (%s of %s)", p.label, percent, formatSize(done), formatSize(total))
	if rate, over, moved := p.rate(); over >= rateAfter || moved >= rateAfterBytes {
		if speed := formatSpeed(rate, total-done); speed != "" {
			line += " · " + speed
		}
	}
	if p.width > 0 && utf8.RuneCountInString(line) >= p.width {
		// Too long for the terminal: leave out the speed rather than wrap.
		line, _, _ = strings.Cut(line, " · ")
	}
	_, _ = fmt.Fprintf(p.w, "\r\033[K%s", line)
}

// sample notes how far the transfer is, and forgets what is too old to
// count.
func (p *progressBar) sample(done int64, now time.Time) {
	if n := len(p.samples); n > 0 && now.Sub(p.samples[n-1].at) < sampleInterval {
		return
	}
	p.samples = append(p.samples, progressSample{at: now, done: done})
	for len(p.samples) > 2 && now.Sub(p.samples[1].at) >= rateWindow {
		p.samples = p.samples[1:]
	}
}

// rate is the speed in bytes a second over the samples, and the time and
// the bytes it rests on.
func (p *progressBar) rate() (float64, time.Duration, int64) {
	if len(p.samples) < 2 {
		return 0, 0, 0
	}
	first, last := p.samples[0], p.samples[len(p.samples)-1]
	over, moved := last.at.Sub(first.at), last.done-first.done
	return float64(moved) / over.Seconds(), over, moved
}

// formatSpeed says how fast bytes move and how long the rest will take,
// as in "18.0 MB/s · 33 s left". Without a speed it says nothing, and the
// time left only when it is worth saying.
func formatSpeed(rate float64, left int64) string {
	if rate < 1 {
		return ""
	}
	s := formatSize(int64(rate)) + "/s"
	if left <= 0 {
		return s
	}
	if eta := formatLeft(time.Duration(float64(left) / rate * float64(time.Second))); eta != "" {
		s += " · " + eta
	}
	return s
}

// formatLeft says how long is left: seconds under a minute, minutes under
// an hour, then hours and minutes; nothing beyond a day, which would be a
// guess.
func formatLeft(d time.Duration) string {
	switch {
	case d <= 0 || d > 24*time.Hour:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("%d s left", max(1, int(math.Round(d.Seconds()))))
	case d < time.Hour:
		return fmt.Sprintf("%d min left", int(math.Round(d.Minutes())))
	}
	minutes := int(math.Round(d.Minutes()))
	if minutes%60 == 0 {
		return fmt.Sprintf("%d h left", minutes/60)
	}
	return fmt.Sprintf("%d h %d min left", minutes/60, minutes%60)
}

func (p *progressBar) finish() {
	if p == nil || !p.shown {
		return
	}
	_, _ = fmt.Fprint(p.w, "\r\033[K")
}
