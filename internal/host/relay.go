package host

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

const (
	relayDialTimeout = 15 * time.Second
	pingInterval     = 30 * time.Second
)

// relayTransport hosts a session through a relay server: the host dials
// out (NAT-friendly) and the relay fans output out to guests.
type relayTransport struct {
	conn   *wsconn.Conn
	cancel context.CancelFunc
}

// normalizeRelayURL turns what users pass for --relay ("relay.example.com:7374",
// "ws://...", "https://...") into the relay's /host websocket endpoint.
func normalizeRelayURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid relay URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("invalid relay URL scheme %q (use ws:// or wss://)", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/host"
	return u.String(), nil
}

func dialRelay(relay, code string, writable bool, writeInput func([]byte)) (*relayTransport, string, error) {
	target, err := normalizeRelayURL(relay)
	if err != nil {
		return nil, "", err
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), relayDialTimeout)
	defer dialCancel()
	ws, _, err := websocket.Dial(dialCtx, target, nil)
	if err != nil {
		return nil, "", fmt.Errorf("connecting to relay %s: %w", relay, err)
	}
	ws.SetReadLimit(1 << 20)
	conn := wsconn.New(ws)

	if err := conn.Send(protocol.Message{
		Type:     protocol.TypeRegister,
		Version:  protocol.Version,
		Code:     code,
		Writable: writable,
	}); err != nil {
		conn.CloseNow()
		return nil, "", fmt.Errorf("registering with relay: %w", err)
	}

	var reply protocol.Message
	regCtx, regCancel := context.WithTimeout(context.Background(), relayDialTimeout)
	err = conn.Read(regCtx, &reply)
	regCancel()
	if err != nil {
		conn.CloseNow()
		return nil, "", fmt.Errorf("waiting for relay registration: %w", err)
	}
	switch reply.Type {
	case protocol.TypeRegistered:
	case protocol.TypeError:
		conn.CloseNow()
		return nil, "", fmt.Errorf("relay refused session: %s", reply.Text)
	default:
		conn.CloseNow()
		return nil, "", fmt.Errorf("unexpected relay reply %q", reply.Type)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &relayTransport{conn: conn, cancel: cancel}
	go t.readLoop(ctx, writeInput)
	go t.pingLoop(ctx)
	return t, reply.Text, nil
}

// readLoop handles frames coming down from the relay: guest input and
// join/leave notices.
func (t *relayTransport) readLoop(ctx context.Context, writeInput func([]byte)) {
	for {
		var msg protocol.Message
		if err := t.conn.Read(ctx, &msg); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "\r\ncotty: relay connection lost; guests are gone but your shell continues\r\n")
			}
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			writeInput(msg.Data)
		case protocol.TypeInfo:
			fmt.Fprintf(os.Stderr, "\r\ncotty: %s\r\n", msg.Text)
		}
	}
}

// pingLoop keeps the outbound connection alive through NATs and detects a
// dead relay.
func (t *relayTransport) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, relayDialTimeout)
			t.conn.Ping(pingCtx)
			cancel()
		}
	}
}

func (t *relayTransport) broadcast(msg protocol.Message) {
	t.conn.Send(msg)
}

func (t *relayTransport) close() {
	t.cancel()
	t.conn.Close(websocket.StatusNormalClosure, "session ended")
}
