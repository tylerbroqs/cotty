package desk

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tylerbroqs/cotty/internal/hood"
)

var newYork = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// session is the US equities session by New York time. Exchange
// holidays are not modeled: they read as a regular weekday.
type session int

const (
	regular session = iota
	premarket
	afterHours
	overnight
	weekend
)

func (s session) String() string {
	return [...]string{"REGULAR", "PRE-MARKET", "AFTER-HOURS", "OVERNIGHT", "WEEKEND"}[s]
}

// sessionAt classifies t. The weekend runs from Friday 20:00 to Sunday
// 20:00, when the 24/5 overnight session (and the Stock Token feeds)
// resume.
func sessionAt(t time.Time) session {
	t = t.In(newYork)
	mins := t.Hour()*60 + t.Minute()
	switch wd := t.Weekday(); {
	case wd == time.Saturday,
		wd == time.Friday && mins >= 20*60,
		wd == time.Sunday && mins < 20*60:
		return weekend
	case wd == time.Sunday:
		return overnight
	}
	switch {
	case mins < 4*60:
		return overnight
	case mins < 9*60+30:
		return premarket
	case mins < 16*60:
		return regular
	case mins < 20*60:
		return afterHours
	default:
		return overnight
	}
}

const (
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	orange = "\x1b[38;5;208m"
	reset  = "\x1b[0m"
)

func (d *desk) paint(code, s string) string {
	if !d.color {
		return s
	}
	return code + s + reset
}

// signed formats a percentage, green when up and red when down.
func (d *desk) signed(pct float64) string {
	s := fmt.Sprintf("%+.2f%%", pct)
	switch {
	case pct > 0:
		return d.paint(green, s)
	case pct < 0:
		return d.paint(red, s)
	}
	return s
}

// pad right-aligns s to width n, ignoring ANSI escapes when measuring.
func pad(s string, n int) string {
	w := visibleWidth(s)
	if w >= n {
		return s
	}
	return strings.Repeat(" ", n-w) + s
}

// padRight left-aligns s to width n, ignoring ANSI escapes.
func padRight(s string, n int) string {
	if w := visibleWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func visibleWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		w++
	}
	return w
}

func price(p float64) string {
	switch {
	case p >= 1000:
		cents := uint64(math.Round(p * 100))
		return fmt.Sprintf("%s.%02d", commas(cents/100), cents%100)
	case p >= 1:
		return fmt.Sprintf("%.2f", p)
	default:
		return fmt.Sprintf("%.4f", p)
	}
}

func commas(n uint64) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// age formats a duration compactly: 42s, 7m, 3h12m, 2d4h.
func age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func gwei(h hood.Head) string {
	if h.BaseFee == nil {
		return "-"
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(h.BaseFee), big.NewFloat(1e9)).Float64()
	return fmt.Sprintf("%.4f", f)
}

// Session names the US equities session at t in New York time, e.g.
// "REGULAR" or "WEEKEND".
func Session(t time.Time) string { return sessionAt(t).String() }
