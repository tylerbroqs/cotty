// Package wsconn wraps a websocket connection with a write lock so
// concurrent senders (broadcast loops, per-connection notices) can't
// interleave frames.
package wsconn

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/tylerbroqs/cotty/internal/protocol"
)

const writeTimeout = 10 * time.Second

// Conn is a websocket connection carrying protocol.Message frames.
type Conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func New(ws *websocket.Conn) *Conn {
	return &Conn{ws: ws}
}

// Send writes one frame, serialized against other senders.
func (c *Conn) Send(msg protocol.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, c.ws, msg)
}

// Read reads one frame. Reads are expected from a single goroutine.
func (c *Conn) Read(ctx context.Context, msg *protocol.Message) error {
	return wsjson.Read(ctx, c.ws, msg)
}

func (c *Conn) Ping(ctx context.Context) error {
	return c.ws.Ping(ctx)
}

func (c *Conn) Close(code websocket.StatusCode, reason string) {
	c.ws.Close(code, reason)
}

func (c *Conn) CloseNow() {
	c.ws.CloseNow()
}
