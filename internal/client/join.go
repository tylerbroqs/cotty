// Package client implements `cotty join`: it connects to a hosted session,
// mirrors the host PTY to the local terminal, and forwards keystrokes when
// the session allows guest input.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/term"

	"github.com/tylerbroqs/cotty/internal/e2ee"
	"github.com/tylerbroqs/cotty/internal/protocol"
)

// escapeKey disconnects the guest locally (Ctrl-], like telnet).
const escapeKey = 0x1d

// parseJoinURL extracts the end-to-end encryption key from the URL's
// #k=... fragment (never sent over the network) and adds the guest's
// display name, returning the URL to dial. An empty name falls back to
// $USER, then to the server-side default.
func parseJoinURL(rawURL, name string) (target string, cipher *e2ee.Cipher, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if u.Fragment != "" {
		vals, _ := url.ParseQuery(u.Fragment)
		keyStr := vals.Get("k")
		if keyStr == "" {
			return "", nil, fmt.Errorf("unrecognized URL fragment %q (expected #k=SESSION-KEY)", u.Fragment)
		}
		key, err := e2ee.DecodeKey(keyStr)
		if err != nil {
			return "", nil, err
		}
		if cipher, err = e2ee.New(key); err != nil {
			return "", nil, err
		}
		u.Fragment = ""
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if name != "" {
		q := u.Query()
		q.Set("name", name)
		u.RawQuery = q.Encode()
	}
	return u.String(), cipher, nil
}

// Run joins the session at rawURL (ws://host:port/ws?code=...) as name and
// blocks until the session ends or the user presses the escape key.
func Run(rawURL, name string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target, cipher, err := parseJoinURL(rawURL, name)
	if err != nil {
		return err
	}
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
				if cipher != nil {
					data = cipher.Seal(data)
				}
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
			data := msg.Data
			if cipher != nil {
				if data, err = cipher.Open(data); err != nil {
					return errors.New("cannot decrypt session output — wrong session key?")
				}
			}
			os.Stdout.Write(data)
		case protocol.TypeHello:
			if msg.Encrypted && cipher == nil {
				return errors.New("this session is end-to-end encrypted; join with the full URL, including its #k= part")
			}
			if !msg.Encrypted && cipher != nil {
				return errors.New("the URL carries a session key but this session is not encrypted; refusing to join")
			}
			mode := "view-only"
			if msg.Writable {
				mode = "read-write"
			}
			if msg.Encrypted {
				mode += ", end-to-end encrypted"
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
