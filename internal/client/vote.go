package client

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// voteKey opens the vote prompt (Ctrl-\). The guest client keeps it
// rather than forwarding it, so it never reaches the shared shell as
// SIGQUIT.
const voteKey = 0x1c

// voteLine is a one-line prompt for voting on multisig proposals in a
// raw-mode terminal: it echoes input after "vote>" and handles
// backspace, Ctrl-U, and cancelling with Esc or Ctrl-C.
type voteLine struct {
	buf []byte
}

func newVoteLine() *voteLine {
	fmt.Fprint(os.Stderr, "\r\n\x1b[1;33mvote>\x1b[0m (approve N / reject N) ")
	return &voteLine{}
}

// key feeds one byte. It returns done when the line is finished: with
// its text on Enter, or "" when cancelled.
func (v *voteLine) key(b byte) (text string, done bool) {
	switch b {
	case '\r', '\n':
		fmt.Fprint(os.Stderr, "\r\n")
		return string(v.buf), true
	case 0x1b, 0x03: // Esc, Ctrl-C
		fmt.Fprint(os.Stderr, " (cancelled)\r\n")
		return "", true
	case 0x7f, 0x08: // Backspace
		v.erase(1)
	case 0x15: // Ctrl-U
		v.erase(len(v.buf))
	default:
		if b >= 0x20 {
			v.buf = append(v.buf, b)
			os.Stderr.Write([]byte{b})
		}
	}
	return "", false
}

func (v *voteLine) erase(runes int) {
	for ; runes > 0 && len(v.buf) > 0; runes-- {
		_, size := utf8.DecodeLastRune(v.buf)
		v.buf = v.buf[:len(v.buf)-size]
		fmt.Fprint(os.Stderr, "\b \b")
	}
}

// parseVote reads "approve 3", "reject #3", or the short forms "y 3" and
// "n 3".
func parseVote(text string) (op string, id int, ok bool) {
	f := strings.Fields(strings.ToLower(text))
	if len(f) != 2 {
		return "", 0, false
	}
	switch f[0] {
	case "approve", "a", "y", "yes":
		op = "approve"
	case "reject", "r", "n", "no":
		op = "reject"
	default:
		return "", 0, false
	}
	id, err := strconv.Atoi(strings.TrimPrefix(f[1], "#"))
	return op, id, err == nil && id > 0
}
