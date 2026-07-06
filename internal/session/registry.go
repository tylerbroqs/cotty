// Package session tracks the guests of one cotty session: their names,
// per-guest write permission, and presence. It backs both a locally hosted
// session and a session living on a relay.
package session

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

// Guest is one connected participant.
type Guest struct {
	Name     string
	Writable bool
	JoinedAt time.Time

	conn     *wsconn.Conn
	warnedRO bool
}

// Registry is the guest list of one session. All methods are safe for
// concurrent use.
type Registry struct {
	mu              sync.Mutex
	guests          []*Guest
	defaultWritable bool
}

func NewRegistry(defaultWritable bool) *Registry {
	return &Registry{defaultWritable: defaultWritable}
}

var nameClean = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeName(s string) string {
	s = nameClean.ReplaceAllString(s, "")
	if len(s) > 24 {
		s = s[:24]
	}
	if s == "" {
		s = "guest"
	}
	return s
}

func (r *Registry) findLocked(name string) *Guest {
	for _, g := range r.guests {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// Join adds a guest under a sanitized, unique version of its requested
// name and returns it.
func (r *Registry) Join(c *wsconn.Conn, requested string) *Guest {
	base := sanitizeName(requested)
	r.mu.Lock()
	defer r.mu.Unlock()
	name := base
	for n := 2; r.findLocked(name) != nil; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	g := &Guest{
		Name:     name,
		Writable: r.defaultWritable,
		JoinedAt: time.Now(),
		conn:     c,
	}
	r.guests = append(r.guests, g)
	return g
}

// Leave removes a guest (idempotent).
func (r *Registry) Leave(g *Guest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, other := range r.guests {
		if other == g {
			r.guests = append(r.guests[:i], r.guests[i+1:]...)
			return
		}
	}
}

func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.guests)
}

func (r *Registry) snapshot() []*Guest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Guest(nil), r.guests...)
}

// Broadcast sends msg to every guest, dropping guests whose sockets fail.
func (r *Registry) Broadcast(msg protocol.Message) {
	r.broadcastExcept(nil, msg)
}

// BroadcastExcept sends msg to every guest but skip (used for join/leave
// notices, which the affected guest doesn't need to hear about itself).
func (r *Registry) BroadcastExcept(skip *Guest, msg protocol.Message) {
	r.broadcastExcept(skip, msg)
}

func (r *Registry) broadcastExcept(skip *Guest, msg protocol.Message) {
	for _, g := range r.snapshot() {
		if g == skip {
			continue
		}
		if err := g.conn.Send(msg); err != nil {
			r.Leave(g)
			g.conn.Close(websocket.StatusInternalError, "write failed")
		}
	}
}

// CloseAll disconnects every guest and empties the registry.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	guests := r.guests
	r.guests = nil
	r.mu.Unlock()
	for _, g := range guests {
		g.conn.Close(websocket.StatusNormalClosure, "session ended")
	}
}

// HandleInput applies one guest's keystrokes if that guest may write,
// warning it once otherwise.
func (r *Registry) HandleInput(g *Guest, data []byte, write func([]byte)) {
	r.mu.Lock()
	writable := g.Writable
	warned := g.warnedRO
	if !writable {
		g.warnedRO = true
	}
	r.mu.Unlock()

	if writable {
		write(data)
		return
	}
	if !warned {
		g.conn.Send(protocol.Message{
			Type: protocol.TypeInfo,
			Text: "your connection is view-only; ask the host to run: cotty ctl allow " + g.Name,
		})
	}
}

// Apply executes a guest-management command and returns a human-readable
// result. Ops: list, allow NAME, deny NAME, kick NAME.
func (r *Registry) Apply(op, name string) (string, error) {
	switch op {
	case "list":
		guests := r.snapshot()
		if len(guests) == 0 {
			return "no guests connected", nil
		}
		var b strings.Builder
		for i, g := range guests {
			mode := "view-only"
			if g.Writable {
				mode = "read-write"
			}
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%-24s  %-10s  joined %s ago", g.Name, mode, time.Since(g.JoinedAt).Round(time.Second))
		}
		return b.String(), nil

	case "allow", "deny":
		writable := op == "allow"
		r.mu.Lock()
		g := r.findLocked(name)
		if g != nil {
			g.Writable = writable
			if writable {
				g.warnedRO = false
			}
		}
		r.mu.Unlock()
		if g == nil {
			return "", fmt.Errorf("no guest named %q", name)
		}
		notice := "the host made your connection view-only"
		result := g.Name + " is now view-only"
		if writable {
			notice = "the host granted you write access"
			result = g.Name + " can now type"
		}
		g.conn.Send(protocol.Message{Type: protocol.TypeWritable, Writable: writable})
		g.conn.Send(protocol.Message{Type: protocol.TypeInfo, Text: notice})
		return result, nil

	case "kick":
		r.mu.Lock()
		g := r.findLocked(name)
		r.mu.Unlock()
		if g == nil {
			return "", fmt.Errorf("no guest named %q", name)
		}
		g.conn.Send(protocol.Message{Type: protocol.TypeInfo, Text: "you were kicked by the host"})
		g.conn.Close(websocket.StatusNormalClosure, "kicked by host")
		r.Leave(g)
		return "kicked " + g.Name, nil

	default:
		return "", fmt.Errorf("unknown command %q (use list, allow, deny, kick)", op)
	}
}
