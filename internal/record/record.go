// Package record writes session recordings in asciicast v2 format, so
// they can be replayed with `cotty replay` or any asciinema-compatible
// player.
//
// Output that is not valid UTF-8 is stored with the Unicode replacement
// character, as JSON requires; typical shell output is unaffected.
package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Recorder appends events to one .cast file. Methods are safe for
// concurrent use and safe on a nil receiver (no-ops), so call sites don't
// need to check whether recording is enabled.
type Recorder struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	start time.Time
}

type header struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Env       map[string]string `json:"env"`
}

// New creates path and writes the asciicast header.
func New(path string, cols, rows int, shell string) (*Recorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating recording %s: %w", path, err)
	}
	w := bufio.NewWriter(f)
	h := header{
		Version:   2,
		Width:     cols,
		Height:    rows,
		Timestamp: time.Now().Unix(),
		Env:       map[string]string{"SHELL": shell, "TERM": os.Getenv("TERM")},
	}
	line, err := json.Marshal(h)
	if err != nil {
		f.Close()
		return nil, err
	}
	w.Write(line)
	w.WriteByte('\n')
	return &Recorder{f: f, w: w, start: time.Now()}, nil
}

func (r *Recorder) event(code, data string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t := time.Since(r.start).Seconds()
	line, err := json.Marshal([]any{t, code, data})
	if err != nil {
		return
	}
	r.w.Write(line)
	r.w.WriteByte('\n')
}

// Output records PTY output.
func (r *Recorder) Output(data []byte) {
	r.event("o", string(data))
}

// Resize records a terminal size change.
func (r *Recorder) Resize(cols, rows int) {
	r.event("r", fmt.Sprintf("%dx%d", cols, rows))
}

// Close flushes and closes the file.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.w.Flush()
	return r.f.Close()
}
