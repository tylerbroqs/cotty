package host

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/hub"
	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

// localTransport serves guests directly from the host machine: an HTTP
// server with a /ws endpoint, one hub fan-out for output.
type localTransport struct {
	guests     *hub.Hub
	server     *http.Server
	code       string
	allowWrite bool
	writeInput func([]byte)
}

func listenLocal(addr, code string, allowWrite bool, writeInput func([]byte)) (*localTransport, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listening on %s: %w", addr, err)
	}

	t := &localTransport{
		guests:     hub.New(),
		code:       code,
		allowWrite: allowWrite,
		writeInput: writeInput,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", t.handleWS)
	t.server = &http.Server{Handler: mux}
	go t.server.Serve(ln)

	joinHost := ln.Addr().String()
	if hostPart, port, err := net.SplitHostPort(joinHost); err == nil {
		if hostPart == "" || hostPart == "::" || hostPart == "0.0.0.0" {
			joinHost = "<this-host>:" + port
		}
	}
	joinURL := fmt.Sprintf("ws://%s/ws?code=%s", joinHost, code)
	return t, joinURL, nil
}

func (t *localTransport) broadcast(msg protocol.Message) {
	t.guests.Broadcast(msg)
}

func (t *localTransport) close() {
	t.guests.Broadcast(protocol.Message{Type: protocol.TypeInfo, Text: "host ended the session"})
	t.guests.CloseAll()
	t.server.Close()
}

func (t *localTransport) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("code") != t.code {
		http.Error(w, "invalid session code", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	g := wsconn.New(ws)

	t.guests.Add(g)
	defer func() {
		t.guests.Remove(g)
		g.CloseNow()
		fmt.Fprintf(os.Stderr, "\r\ncotty: guest left (%d connected)\r\n", t.guests.Count())
	}()

	g.Send(protocol.Message{
		Type:     protocol.TypeHello,
		Version:  protocol.Version,
		Text:     "welcome to cotty session " + t.code,
		Writable: t.allowWrite,
	})
	fmt.Fprintf(os.Stderr, "\r\ncotty: guest joined (%d connected)\r\n", t.guests.Count())

	warnedReadOnly := false
	for {
		var msg protocol.Message
		if err := g.Read(r.Context(), &msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			if t.allowWrite {
				t.writeInput(msg.Data)
			} else if !warnedReadOnly {
				warnedReadOnly = true
				g.Send(protocol.Message{
					Type: protocol.TypeInfo,
					Text: "this session is view-only; the host started it without --write",
				})
			}
		default:
			// Ignore unknown frames so old clients keep working against
			// newer hosts.
		}
	}
}
