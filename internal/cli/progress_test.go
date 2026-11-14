package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share"
)

func TestFormatSpeed(t *testing.T) {
	const mb = 1024 * 1024
	cases := []struct {
		rate float64
		left int64
		want string
	}{
		{18 * mb, 33 * 18 * mb, "18.0 MB/s · 33 s left"},
		{18 * mb, 18 * mb / 4, "18.0 MB/s · 1 s left"},
		{2 * mb, 5 * 60 * 2 * mb, "2.0 MB/s · 5 min left"},
		{2 * mb, 90 * 2 * mb, "2.0 MB/s · 2 min left"},
		{100 * 1024, 2 * 3600 * 100 * 1024, "100.0 KB/s · 2 h left"},
		{100 * 1024, (3600 + 25*60) * 100 * 1024, "100.0 KB/s · 1 h 25 min left"},
		// Done: the speed alone.
		{18 * mb, 0, "18.0 MB/s"},
		// More than a day left is a guess: the speed alone.
		{10, 1024 * mb, "10 B/s"},
		// No speed yet, or none to speak of: nothing.
		{0, 100 * mb, ""},
		{0.5, 100 * mb, ""},
	}
	for _, tc := range cases {
		if got := formatSpeed(tc.rate, tc.left); got != tc.want {
			t.Errorf("formatSpeed(%v, %d) = %q, want %q", tc.rate, tc.left, got, tc.want)
		}
	}
}

// The speed is what moved over the last few seconds: parts that arrive
// in bursts even out, and a lasting change shows within the window.
func TestProgressRate(t *testing.T) {
	start := time.Now()
	p := &progressBar{}
	if rate, _, _ := p.rate(); rate != 0 {
		t.Errorf("a speed before anything moved: %v", rate)
	}
	// 4 MB every second, in one burst at the start of each.
	for second := range 10 {
		at := start.Add(time.Duration(second) * time.Second)
		p.sample(int64(second)*4<<20, at)
		p.sample(int64(second)*4<<20, at.Add(500*time.Millisecond))
	}
	if rate, over, _ := p.rate(); rate != 4<<20 || over < rateWindow {
		t.Errorf("rate %v over %s, want %d over the window", rate, over, 4<<20)
	}
	// Then twice as fast: after a window's time, the speed is the new one.
	for second := range 6 {
		p.sample(36<<20+int64(second+1)*8<<20, start.Add(time.Duration(10+second)*time.Second))
	}
	if rate, _, _ := p.rate(); rate != 8<<20 {
		t.Errorf("rate %v, want %d", rate, 8<<20)
	}
	// Samples closer together than sampleInterval count once.
	before := len(p.samples)
	p.sample(100<<20, start.Add(15*time.Second+100*time.Millisecond))
	if len(p.samples) != before {
		t.Errorf("%d samples, want %d", len(p.samples), before)
	}
}

func TestProgressShowsTheSpeedOnceItCanTell(t *testing.T) {
	var line lineRecorder
	p := &progressBar{w: &line, label: "Uploading…"}
	// Half a second in, too little has moved to tell.
	p.samples = []progressSample{{at: time.Now().Add(-500 * time.Millisecond)}}
	p.update(1<<20, 256<<20)
	if want := "\r\033[KUploading… 0% (1.0 MB of 256.0 MB)"; line.last != want {
		t.Errorf("line %q, want %q", line.last, want)
	}
	// Two seconds in, 64 MiB have moved: 32 MiB a second.
	p.samples, p.last = []progressSample{{at: time.Now().Add(-2 * time.Second)}}, time.Time{}
	p.update(64<<20, 256<<20)
	if want := "\r\033[KUploading… 25% (64.0 MB of 256.0 MB) · 32.0 MB/s · 6 s left"; line.last != want {
		t.Errorf("line %q, want %q", line.last, want)
	}
	// A narrow terminal gets the line without the speed.
	p.width, p.last = 50, time.Time{}
	p.update(64<<20, 256<<20)
	if want := "\r\033[KUploading… 25% (64.0 MB of 256.0 MB)"; line.last != want {
		t.Errorf("narrow line %q, want %q", line.last, want)
	}
}

// lineRecorder keeps the last line drawn.
type lineRecorder struct{ last string }

func (l *lineRecorder) Write(b []byte) (int, error) {
	l.last = string(b)
	return len(b), nil
}

func TestStageBar(t *testing.T) {
	cases := []struct {
		sent, encrypted int64
		want            string
	}{
		{0, 0, "░░░░░░░░░░"},
		{0, 50, "▓▓▓▓▓░░░░░"},
		{20, 50, "██▓▓▓░░░░░"},
		// A cell is sent only once all of it is.
		{99, 100, "█████████▓"},
		{100, 100, "██████████"},
	}
	for _, tc := range cases {
		got := stageBar(share.UploadProgress{Total: 100, Encrypted: tc.encrypted, Sent: tc.sent}, 10)
		if got != tc.want {
			t.Errorf("sent %d, encrypted %d: %q, want %q", tc.sent, tc.encrypted, got, tc.want)
		}
	}
}

func TestUploadLine(t *testing.T) {
	const mb = 1 << 20
	var line lineRecorder
	p := &progressBar{w: &line, label: "Uploading"}
	// Two seconds in, 64 MiB sent: 32 MiB a second.
	p.samples = []progressSample{{at: time.Now().Add(-2 * time.Second)}}
	u := share.UploadProgress{Total: 256 * mb, Encrypted: 128 * mb, Sent: 64 * mb, Uploaded: 32 * mb}
	p.stages(u)
	if want := "\r\033[KUploading █████▓▓▓▓▓░░░░░░░░░░ 25% · 64.0 MB of 256.0 MB · 32.0 MB/s · 6 s left"; line.last != want {
		t.Errorf("line %q,\nwant %q", line.last, want)
	}
	// A retry shows at once, in place of the speed.
	u.Retrying = true
	p.stages(u)
	if !strings.HasSuffix(line.last, "· a part didn't go through, trying it again…") {
		t.Errorf("retry line %q", line.last)
	}
	// A narrow terminal loses the speed first, then the bar.
	u.Retrying = false
	p.width, p.last = 60, time.Time{}
	p.stages(u)
	if want := "\r\033[KUploading █████▓▓▓▓▓░░░░░░░░░░ 25% · 64.0 MB of 256.0 MB"; line.last != want {
		t.Errorf("narrow line %q,\nwant %q", line.last, want)
	}
	p.width, p.last = 40, time.Time{}
	p.stages(u)
	if want := "\r\033[KUploading 25% · 64.0 MB of 256.0 MB"; line.last != want {
		t.Errorf("narrower line %q,\nwant %q", line.last, want)
	}
}

// A part sent again makes the count go back; the speed starts over then,
// rather than going negative.
func TestProgressRateStartsOverWhenTheCountGoesBack(t *testing.T) {
	start := time.Now()
	p := &progressBar{}
	p.sample(0, start)
	p.sample(10<<20, start.Add(time.Second))
	p.sample(2<<20, start.Add(2*time.Second))
	if len(p.samples) != 1 {
		t.Errorf("%d samples after going back, want 1", len(p.samples))
	}
}
