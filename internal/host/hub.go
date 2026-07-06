package host

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/tylerbroqs/cotty/internal/protocol"
)

const writeTimeout = 10 * time.Second

// guest is one connected websocket client. Writes to the socket are
// serialized through mu so the broadcast loop and per-guest notices can't
// interleave frames.
type guest struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (g *guest) send(msg protocol.Message) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, g.ws, msg)
}

// hub tracks connected guests and fans PTY output out to them.
type hub struct {
	mu     sync.Mutex
	guests map[*guest]struct{}
}

func newHub() *hub {
	return &hub{guests: make(map[*guest]struct{})}
}

func (h *hub) add(ws *websocket.Conn) *guest {
	g := &guest{ws: ws}
	h.mu.Lock()
	h.guests[g] = struct{}{}
	h.mu.Unlock()
	return g
}

func (h *hub) remove(g *guest) {
	h.mu.Lock()
	delete(h.guests, g)
	h.mu.Unlock()
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.guests)
}

// broadcast sends msg to every guest, dropping guests whose sockets fail.
func (h *hub) broadcast(msg protocol.Message) {
	h.mu.Lock()
	snapshot := make([]*guest, 0, len(h.guests))
	for g := range h.guests {
		snapshot = append(snapshot, g)
	}
	h.mu.Unlock()

	for _, g := range snapshot {
		if err := g.send(msg); err != nil {
			h.remove(g)
			g.ws.Close(websocket.StatusInternalError, "write failed")
		}
	}
}
