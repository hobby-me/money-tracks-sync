package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const dash = "—"

// humanBytes — 0 → тире, дальше K/M/G с одним знаком после запятой.
func humanBytes(n int64) string {
	if n <= 0 {
		return dash
	}
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + "B"
	}
	units := "KMGT"
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + string(units[i])
}

// groupInt — 1234567 → "1 234 567" (тонкая группировка читается лучше запятых).
func groupInt(n int64) string {
	if n == 0 {
		return dash
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	head := len(s) % 3
	if head > 0 {
		b.WriteString(s[:head])
	}
	for i := head; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// ago — «5s», «3m», «2h», «5d»; нулевое время — тире.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return dash
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}

// countdown — «9:59» до истечения; истёкшее — «0:00».
func countdown(until, now time.Time) string {
	d := until.Sub(now)
	if d <= 0 {
		return "0:00"
	}
	sec := int(d / time.Second)
	m, s := sec/60, sec%60
	out := strconv.Itoa(m) + ":"
	if s < 10 {
		out += "0"
	}
	return out + strconv.Itoa(s)
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return ansi.Truncate(s, w-1, "") + "…"
}

func padRight(s string, w int) string {
	n := ansi.StringWidth(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

func padLeft(s string, w int) string {
	n := ansi.StringWidth(s)
	if n >= w {
		return s
	}
	return strings.Repeat(" ", w-n) + s
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

func localTime(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.Local().Format("2006-01-02 15:04")
}
