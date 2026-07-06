package host

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/session"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

// localTransport serves guests directly from the host machine: an HTTP
// server with a /ws endpoint and an in-process guest registry.
type localTransport struct {
	guests     *session.Registry
	server     *http.Server
	code       string
	writeInput func([]byte)
}

func listenLocal(addr, code string, allowWrite bool, writeInput func([]byte)) (*localTransport, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listening on %s: %w", addr, err)
	}

	t := &localTransport{
		guests:     session.NewRegistry(allowWrite),
		code:       code,
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

func (t *localTransport) control(op, name string) (string, error) {
	return t.guests.Apply(op, name)
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
	conn := wsconn.New(ws)

	g := t.guests.Join(conn, r.URL.Query().Get("name"))
	defer func() {
		t.guests.Leave(g)
		conn.CloseNow()
		notice := fmt.Sprintf("%s left (%d connected)", g.Name, t.guests.Count())
		fmt.Fprintf(os.Stderr, "\r\ncotty: %s\r\n", notice)
		t.guests.Broadcast(protocol.Message{Type: protocol.TypeInfo, Text: notice})
	}()

	conn.Send(protocol.Message{
		Type:     protocol.TypeHello,
		Version:  protocol.Version,
		Text:     fmt.Sprintf("welcome to cotty session %s — you are %s", t.code, g.Name),
		Writable: g.Writable,
	})
	notice := fmt.Sprintf("%s joined (%d connected)", g.Name, t.guests.Count())
	fmt.Fprintf(os.Stderr, "\r\ncotty: %s\r\n", notice)
	t.guests.BroadcastExcept(g, protocol.Message{Type: protocol.TypeInfo, Text: notice})

	for {
		var msg protocol.Message
		if err := conn.Read(r.Context(), &msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			t.guests.HandleInput(g, msg.Data, t.writeInput)
		default:
			// Ignore unknown frames so old clients keep working against
			// newer hosts.
		}
	}
}
