package desk

import (
	"testing"
	"time"
)

func TestSessionAt(t *testing.T) {
	cases := []struct {
		at   string // New York local time
		want session
	}{
		{"2026-10-19 09:29", premarket}, // Monday
		{"2026-10-19 09:30", regular},
		{"2026-10-19 15:59", regular},
		{"2026-10-19 16:00", afterHours},
		{"2026-10-19 20:00", overnight},
		{"2026-10-20 03:59", overnight},  // Tuesday
		{"2026-10-23 19:59", afterHours}, // Friday
		{"2026-10-23 20:00", weekend},
		{"2026-10-24 12:00", weekend}, // Saturday
		{"2026-10-25 19:59", weekend}, // Sunday
		{"2026-10-25 20:00", overnight},
	}
	for _, c := range cases {
		at, err := time.ParseInLocation("2006-01-02 15:04", c.at, newYork)
		if err != nil {
			t.Fatal(err)
		}
		if got := sessionAt(at.UTC()); got != c.want {
			t.Errorf("sessionAt(%s) = %s, want %s", c.at, got, c.want)
		}
	}
}

func TestPad(t *testing.T) {
	d := &desk{color: true}
	got := pad(d.paint(green, "+1.00%"), 8)
	if visibleWidth(got) != 8 {
		t.Errorf("padded colored string is %d wide, want 8: %q", visibleWidth(got), got)
	}
}

func TestPrice(t *testing.T) {
	cases := map[float64]string{
		366.154:   "366.15",
		104250.5:  "104,250.50",
		1234.999:  "1,235.00",
		0.9998123: "0.9998",
	}
	for in, want := range cases {
		if got := price(in); got != want {
			t.Errorf("price(%v) = %q, want %q", in, got, want)
		}
	}
}
