package host

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/audit"
	"github.com/tylerbroqs/cotty/internal/e2ee"
	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

const (
	relayDialTimeout = 15 * time.Second
	pingInterval     = 30 * time.Second
)

// relayTransport hosts a session through a relay server: the host dials
// out (NAT-friendly) and the relay owns the guest registry. Control
// commands travel to the relay as frames; per-guest write permission is
// enforced there, with a coarse re-check here so a misbehaving relay
// can't inject input into a session that never allowed writing.
type relayTransport struct {
	conn   *wsconn.Conn
	cancel context.CancelFunc
	// cipher seals output and opens guest input for end-to-end encrypted
	// sessions; nil when the host opted out with -plain.
	cipher *e2ee.Cipher

	ctlMu   sync.Mutex // serializes control calls
	pending chan protocol.Message

	mu           sync.Mutex
	pendingMu    sync.Mutex
	defaultWrite bool
	granted      map[string]bool
	anyWrite     bool
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

func dialRelay(relay, code string, writable, encrypt bool, writeInput func(who string, data []byte), aud *audit.Logger) (*relayTransport, string, error) {
	target, err := normalizeRelayURL(relay)
	if err != nil {
		return nil, "", err
	}

	var (
		cipher *e2ee.Cipher
		keyStr string
	)
	if encrypt {
		key, err := e2ee.NewKey()
		if err != nil {
			return nil, "", fmt.Errorf("generating session key: %w", err)
		}
		if cipher, err = e2ee.New(key); err != nil {
			return nil, "", err
		}
		keyStr = e2ee.EncodeKey(key)
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
		Type:      protocol.TypeRegister,
		Version:   protocol.Version,
		Code:      code,
		Writable:  writable,
		Encrypted: encrypt,
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
	t := &relayTransport{
		conn:         conn,
		cancel:       cancel,
		cipher:       cipher,
		defaultWrite: writable,
		granted:      make(map[string]bool),
		anyWrite:     writable,
	}
	go t.readLoop(ctx, writeInput, aud)
	go t.pingLoop(ctx)

	// The key travels in the URL fragment, which clients never send over
	// the network — so guests get it, the relay doesn't.
	joinURL := reply.Text
	if encrypt {
		joinURL += "#k=" + keyStr
	}
	return t, joinURL, nil
}

// readLoop handles frames coming down from the relay: guest input,
// join/leave notices, and control-command results.
func (t *relayTransport) readLoop(ctx context.Context, writeInput func(who string, data []byte), aud *audit.Logger) {
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
			t.mu.Lock()
			ok := t.anyWrite
			t.mu.Unlock()
			if !ok {
				continue
			}
			data := msg.Data
			if t.cipher != nil {
				var err error
				if data, err = t.cipher.Open(data); err != nil {
					continue // wrong key or tampering; drop
				}
			}
			who := msg.Name
			if who == "" {
				who = "guest"
			}
			writeInput(who, data)
		case protocol.TypeInfo:
			aud.Event("info", "", msg.Text)
			fmt.Fprintf(os.Stderr, "\r\ncotty: %s\r\n", msg.Text)
		case protocol.TypeControlResult:
			t.pendingMu.Lock()
			ch := t.pending
			t.pendingMu.Unlock()
			if ch != nil {
				select {
				case ch <- msg:
				default:
				}
			}
		}
	}
}

// control forwards a guest-management command to the relay and waits for
// its result.
func (t *relayTransport) control(op, name string) (string, error) {
	t.ctlMu.Lock()
	defer t.ctlMu.Unlock()

	ch := make(chan protocol.Message, 1)
	t.pendingMu.Lock()
	t.pending = ch
	t.pendingMu.Unlock()
	defer func() {
		t.pendingMu.Lock()
		t.pending = nil
		t.pendingMu.Unlock()
	}()

	if err := t.conn.Send(protocol.Message{Type: protocol.TypeControl, Op: op, Name: name}); err != nil {
		return "", fmt.Errorf("sending command to relay: %w", err)
	}

	select {
	case msg := <-ch:
		if !msg.Ok {
			return "", errors.New(msg.Text)
		}
		if op == "allow" || op == "deny" {
			t.mu.Lock()
			t.granted[name] = op == "allow"
			t.anyWrite = t.defaultWrite
			for _, w := range t.granted {
				if w {
					t.anyWrite = true
				}
			}
			t.mu.Unlock()
		}
		return msg.Text, nil
	case <-time.After(relayDialTimeout):
		return "", errors.New("timed out waiting for the relay")
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
	if t.cipher != nil && msg.Type == protocol.TypeOutput {
		msg.Data = t.cipher.Seal(msg.Data)
	}
	t.conn.Send(msg)
}

func (t *relayTransport) close() {
	t.cancel()
	t.conn.Close(websocket.StatusNormalClosure, "session ended")
}
