// Package replay plays back an asciicast v2 recording (as written by
// `cotty host -record`, or by asciinema) in the local terminal.
package replay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Options configures playback.
type Options struct {
	// Speed multiplies playback speed (2 = twice as fast).
	Speed float64
	// MaxIdle caps pauses between events; 0 means no cap.
	MaxIdle time.Duration
}

// Run plays the recording at path to stdout and blocks until done.
func Run(path string, opts Options) error {
	if opts.Speed <= 0 {
		opts.Speed = 1
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	if !sc.Scan() {
		return fmt.Errorf("%s: empty file", path)
	}
	var h struct {
		Version int `json:"version"`
		Width   int `json:"width"`
		Height  int `json:"height"`
	}
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Version != 2 {
		return fmt.Errorf("%s: not an asciicast v2 recording", path)
	}
	fmt.Fprintf(os.Stderr, "cotty: replaying %s (%dx%d, speed %gx)\n\n", path, h.Width, h.Height, opts.Speed)

	last := 0.0
	for sc.Scan() {
		var ev []json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || len(ev) < 3 {
			continue
		}
		var t float64
		var code, data string
		if json.Unmarshal(ev[0], &t) != nil || json.Unmarshal(ev[1], &code) != nil || json.Unmarshal(ev[2], &data) != nil {
			continue
		}
		if code != "o" {
			last = t
			continue
		}
		dt := t - last
		last = t
		if dt > 0 {
			if opts.MaxIdle > 0 && dt > opts.MaxIdle.Seconds() {
				dt = opts.MaxIdle.Seconds()
			}
			time.Sleep(time.Duration(dt / opts.Speed * float64(time.Second)))
		}
		os.Stdout.WriteString(data)
	}
	fmt.Fprintf(os.Stderr, "\r\ncotty: replay finished\r\n")
	return sc.Err()
}
