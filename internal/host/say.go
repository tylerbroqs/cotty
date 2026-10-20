package host

import (
	"errors"
	"sync"
	"time"
)

// askWait is how long `cotty ask` waits for an agent to answer. It stays
// under the control socket's ask timeout so the caller gets a real reply.
const askWait = 90 * time.Second

var errNoAnswer = errors.New("no answer yet; is an agent in the session? start one with: cotty agent <join-url>")

// Chat isn't gated by write permission (a view-only agent must be able to
// answer), so each sender is rate limited instead: at most sayBurst
// messages per sayWindow.
const (
	sayBurst  = 5
	sayWindow = 10 * time.Second
)

// sayLimiter tracks recent chat messages per sender.
type sayLimiter struct {
	mu     sync.Mutex
	recent map[string][]time.Time
}

// allow reports whether who may send another message now.
func (l *sayLimiter) allow(who string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recent == nil {
		l.recent = make(map[string][]time.Time)
	}
	now := time.Now()
	kept := l.recent[who][:0]
	for _, t := range l.recent[who] {
		if now.Sub(t) < sayWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= sayBurst {
		l.recent[who] = kept
		return false
	}
	l.recent[who] = append(kept, now)
	return true
}

// answers hands agent replies (TypeSay frames) to pending `cotty ask`
// calls, oldest question first. A reply nobody is waiting for is left to
// the caller to show as a notice.
type answers struct {
	mu      sync.Mutex
	waiters []chan string
}

// wait blocks until a reply is delivered or askWait passes.
func (a *answers) wait() (string, error) {
	ch := make(chan string, 1)
	a.mu.Lock()
	a.waiters = append(a.waiters, ch)
	a.mu.Unlock()

	select {
	case text := <-ch:
		return text, nil
	case <-time.After(askWait):
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, w := range a.waiters {
			if w == ch {
				a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
				return "", errNoAnswer
			}
		}
		// Delivered while timing out.
		return <-ch, nil
	}
}

// deliver passes text to the oldest pending ask and reports whether
// anyone was waiting.
func (a *answers) deliver(text string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.waiters) == 0 {
		return false
	}
	ch := a.waiters[0]
	a.waiters = a.waiters[1:]
	ch <- text
	return true
}
