package agent

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// screen turns the session's raw terminal output into the plain text an
// agent reads. It is not a terminal emulator: it keeps a scrollback of
// lines, drops escape sequences and colors, honors carriage returns and
// backspaces, and starts over when the screen is cleared (which is how
// full-screen redraws like `cotty desk` stay current).
type screen struct {
	mu      sync.Mutex
	lines   []string
	cur     []rune
	cr      bool   // a lone \r: the next printable rune overwrites the line
	esc     []byte // a partial escape sequence carried across writes
	pending []byte // a partial UTF-8 sequence carried across writes
	last    time.Time
}

const (
	maxLines    = 400
	maxSnapshot = 12 << 10
)

func (s *screen) Write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()

	buf := append(s.pending, p...)
	s.pending = nil
	for len(buf) > 0 {
		if len(s.esc) > 0 || buf[0] == 0x1b {
			n, done := s.escape(buf)
			buf = buf[n:]
			if !done {
				return
			}
			continue
		}
		r, size := utf8.DecodeRune(buf)
		if r == utf8.RuneError && size <= 1 && !utf8.FullRune(buf) {
			s.pending = append([]byte(nil), buf...)
			return
		}
		buf = buf[size:]
		switch r {
		case '\n':
			s.newline()
		case '\r':
			s.cr = true
		case '\b':
			if len(s.cur) > 0 {
				s.cur = s.cur[:len(s.cur)-1]
			}
		case '\t':
			s.put(' ')
			for len(s.cur)%8 != 0 {
				s.cur = append(s.cur, ' ')
			}
		default:
			if r >= 0x20 && r != 0x7f && r != utf8.RuneError {
				s.put(r)
			}
		}
	}
}

func (s *screen) put(r rune) {
	if s.cr {
		s.cur = s.cur[:0]
		s.cr = false
	}
	s.cur = append(s.cur, r)
}

func (s *screen) newline() {
	s.cr = false
	s.lines = append(s.lines, strings.TrimRight(string(s.cur), " "))
	s.cur = s.cur[:0]
	if len(s.lines) > maxLines {
		s.lines = append([]string(nil), s.lines[len(s.lines)-maxLines:]...)
	}
}

// escape consumes an escape sequence from the start of buf (continuing a
// partial one in s.esc) and reports how many bytes it used and whether the
// sequence is complete.
func (s *screen) escape(buf []byte) (int, bool) {
	for i, b := range buf {
		s.esc = append(s.esc, b)
		seq := s.esc
		if len(seq) < 2 {
			continue
		}
		switch seq[1] {
		case '[': // CSI: parameters, then a final byte in 0x40-0x7e
			if len(seq) > 2 && b >= 0x40 && b <= 0x7e {
				s.csi(string(seq[2:len(seq)-1]), b)
				s.esc = nil
				return i + 1, true
			}
		case ']', 'P', '_', '^': // OSC/DCS/APC/PM: up to BEL or ST
			if b == 0x07 || (b == '\\' && len(seq) > 2 && seq[len(seq)-2] == 0x1b) {
				s.esc = nil
				return i + 1, true
			}
		case '(', ')', '*', '+': // charset designation: one more byte
			if len(seq) == 3 {
				s.esc = nil
				return i + 1, true
			}
		default: // two-byte sequences (ESC 7, ESC =, ...)
			s.esc = nil
			return i + 1, true
		}
		if len(s.esc) > 256 { // runaway sequence: drop it
			s.esc = nil
			return i + 1, true
		}
	}
	return len(buf), false
}

func (s *screen) csi(params string, final byte) {
	switch final {
	case 'J': // erase in display: 2 and 3 clear everything
		if params == "2" || params == "3" {
			s.lines = nil
			s.cur = s.cur[:0]
			s.cr = false
		}
	case 'K': // erase in line: approximate "erase to end" after a \r
		if params == "" || params == "0" || params == "2" {
			if s.cr {
				s.cur = s.cur[:0]
			}
		}
	}
}

// Snapshot returns the most recent text on screen, bounded in size.
func (s *screen) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := append(append([]string(nil), s.lines...), string(s.cur))
	text := strings.TrimRight(strings.Join(all, "\n"), "\n ")
	if len(text) > maxSnapshot {
		text = text[len(text)-maxSnapshot:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text
}

// quietFor reports whether no output has arrived for d.
func (s *screen) quietFor(d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.last) >= d
}
