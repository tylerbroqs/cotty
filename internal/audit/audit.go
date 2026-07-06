// Package audit writes a session's "who did what" trail as JSON lines:
// every applied keystroke with the participant it came from, plus joins,
// leaves, permission changes, and kicks.
//
// Input entries record raw keystrokes (including control sequences) as
// they were applied to the PTY — that is what actually ran, so that is
// what is logged.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Logger appends entries to one audit file. Methods are safe for
// concurrent use and safe on a nil receiver (no-ops), so call sites don't
// need to check whether auditing is enabled.
type Logger struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

type entry struct {
	Time string `json:"time"`
	Kind string `json:"kind"`
	Who  string `json:"who,omitempty"`
	Data string `json:"data,omitempty"`
	Note string `json:"note,omitempty"`
}

// New creates (or truncates) the audit file at path.
func New(path string) (*Logger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating audit log %s: %w", path, err)
	}
	return &Logger{f: f, w: bufio.NewWriter(f)}, nil
}

func (l *Logger) write(e entry) {
	if l == nil {
		return
	}
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.Write(line)
	l.w.WriteByte('\n')
	l.w.Flush() // an audit trail should survive a crash
}

// Input records keystrokes applied to the PTY on behalf of who.
func (l *Logger) Input(who string, data []byte) {
	l.write(entry{Kind: "input", Who: who, Data: string(data)})
}

// Event records a session event: join, leave, allow, deny, kick, info,
// session.
func (l *Logger) Event(kind, who, note string) {
	l.write(entry{Kind: kind, Who: who, Note: note})
}

// Close flushes and closes the file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.Flush()
	return l.f.Close()
}
