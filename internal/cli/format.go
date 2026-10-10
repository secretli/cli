package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/secretli/cli/internal/share"
)

// formatMoment says a time the way a person would: "today at 19:53",
// "tomorrow at 9:00", "yesterday at 8:10", a weekday within the past week,
// and the date beyond that.
func formatMoment(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	clock := t.Format("15:04")
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	days := int(day(t).Sub(day(now)).Hours() / 24)
	switch {
	case days == 0:
		return "today at " + clock
	case days == 1:
		return "tomorrow at " + clock
	case days == -1:
		return "yesterday at " + clock
	case days < 0 && days > -7:
		return "on " + t.Format("Monday") + " at " + clock
	case t.Year() == now.Year():
		return "on " + t.Format("2 Jan") + " at " + clock
	}
	return "on " + t.Format("2 Jan 2006") + " at " + clock
}

// formatBy says a moment after "by": the time alone today, as in "by
// 19:42", and formatMoment's words on another day, as in "by tomorrow at
// 00:05".
func formatBy(t, now time.Time) string {
	moment := formatMoment(t, now)
	if clock, today := strings.CutPrefix(moment, "today at "); today {
		return clock
	}
	return strings.TrimPrefix(moment, "on ")
}

// formatAgo says how long ago something happened.
func formatAgo(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	}
	return plural(int(d.Hours()/24), "day") + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// formatSize is the web app's size: B, KB, MB, GB in powers of 1024.
func formatSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

// describeInfo is one sentence about a live secret, for the person opening it.
func describeInfo(info *share.Info, owner bool, now time.Time) string {
	what := "A one-time "
	if info.Reusable {
		what = "A reusable "
	}
	if info.Kind == share.KindText {
		what += "text secret"
	} else {
		what += "set of files"
	}
	if info.PasswordProtected {
		what += " with a password"
	}
	s := fmt.Sprintf("%s, sent %s, expires %s.", what, formatAgo(info.CreatedAt, now), formatMoment(info.ExpiresAt, now))
	if owner && info.Reusable {
		if info.Opened {
			s += " It has been opened."
		} else {
			s += " Nobody has opened it yet."
		}
	}
	return s
}

// notFoundSentence is what status says of a secret that is gone. The server
// keeps nothing about a secret once it was opened, deleted or has expired, and
// answers the same 404 as for a link to nothing, to the owner and a recipient
// alike, so it can only list what may have become of the secret.
const notFoundSentence = "This secret is gone: it may have expired, been opened or been deleted."
