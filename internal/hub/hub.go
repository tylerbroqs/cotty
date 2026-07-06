// Package hub fans protocol frames out to a set of websocket connections.
// It backs both a locally hosted session's guest list and a relay
// session's guest list.
package hub

import (
	"sync"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

// Hub is a set of guest connections. All methods are safe for concurrent
// use.
type Hub struct {
	mu    sync.Mutex
	conns map[*wsconn.Conn]struct{}
}

func New() *Hub {
	return &Hub{conns: make(map[*wsconn.Conn]struct{})}
}

func (h *Hub) Add(c *wsconn.Conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) Remove(c *wsconn.Conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

func (h *Hub) snapshot() []*wsconn.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := make([]*wsconn.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	return conns
}

// Broadcast sends msg to every connection, dropping ones whose sockets
// fail.
func (h *Hub) Broadcast(msg protocol.Message) {
	for _, c := range h.snapshot() {
		if err := c.Send(msg); err != nil {
			h.Remove(c)
			c.Close(websocket.StatusInternalError, "write failed")
		}
	}
}

// CloseAll disconnects every connection and empties the hub.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	conns := make([]*wsconn.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.conns = make(map[*wsconn.Conn]struct{})
	h.mu.Unlock()
	for _, c := range conns {
		c.Close(websocket.StatusNormalClosure, "session ended")
	}
}
