// Package client implements `cotty join`: it connects to a hosted session,
// mirrors the host PTY to the local terminal, and forwards keystrokes when
// the session allows guest input.
package client

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/term"

	"github.com/tylerbroqs/cotty/internal/protocol"
)

// escapeKey disconnects the guest locally (Ctrl-], like telnet).
const escapeKey = 0x1d

// withName adds the guest's display name to the join URL. An empty name
// falls back to $USER, then to the server-side default.
func withName(rawURL, name string) string {
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// Run joins the session at rawURL (ws://host:port/ws?code=...) as name and
// blocks until the session ends or the user presses the escape key.
func Run(rawURL, name string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := withName(rawURL, name)
	ws, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", rawURL, err)
	}
	ws.SetReadLimit(1 << 20)
	defer ws.CloseNow()

	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		oldState, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("setting raw mode: %w", err)
		}
		defer term.Restore(stdinFd, oldState)
	}

	fmt.Fprintf(os.Stderr, "cotty: joined; press Ctrl-] to leave\r\n")

	// Keystrokes go to the host. The host decides whether to apply them.
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for _, b := range buf[:n] {
					if b == escapeKey {
						return
					}
				}
				data := make([]byte, n)
				copy(data, buf[:n])
				if werr := wsjson.Write(ctx, ws, protocol.Message{
					Type: protocol.TypeInput,
					Data: data,
				}); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		var msg protocol.Message
		if err := wsjson.Read(ctx, ws, &msg); err != nil {
			fmt.Fprintf(os.Stderr, "\r\ncotty: disconnected\r\n")
			return nil
		}
		switch msg.Type {
		case protocol.TypeOutput:
			os.Stdout.Write(msg.Data)
		case protocol.TypeHello:
			mode := "view-only"
			if msg.Writable {
				mode = "read-write"
			}
			fmt.Fprintf(os.Stderr, "cotty: %s (%s)\r\n", msg.Text, mode)
		case protocol.TypeInfo:
			fmt.Fprintf(os.Stderr, "\r\ncotty: %s\r\n", msg.Text)
		case protocol.TypeWritable:
			mode := "view-only"
			if msg.Writable {
				mode = "read-write"
			}
			fmt.Fprintf(os.Stderr, "\r\ncotty: your connection is now %s\r\n", mode)
		case protocol.TypeResize:
			// v0 guests don't resize their terminal; a size mismatch just
			// means wrapped output. The web client (roadmap) will handle
			// this properly.
		}
	}
}
