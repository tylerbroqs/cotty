// Package relay implements `cotty relay`: a public rendezvous server that
// lets hosts behind NAT share sessions. Hosts dial the /host endpoint and
// register a session code; guests join via /ws?code=... exactly like they
// would join a locally hosted session, so `cotty join` needs no changes.
// The relay owns each relayed session's guest registry and executes the
// host's control commands (list/allow/deny/kick) against it.
//
// The relay only forwards frames — but it can read them. End-to-end
// encryption (relay sees ciphertext only) is on the roadmap for v0.4.
package relay

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tylerbroqs/cotty/internal/protocol"
	"github.com/tylerbroqs/cotty/internal/session"
	"github.com/tylerbroqs/cotty/internal/webui"
	"github.com/tylerbroqs/cotty/internal/wsconn"
)

// Options configures a relay server.
type Options struct {
	// Addr is the listen address, e.g. ":7374".
	Addr string
	// PublicURL is the base URL guests should use to reach this relay
	// (e.g. "wss://relay.example.com"). Empty means it is derived from
	// each request's Host header with a ws:// scheme.
	PublicURL string
}

// relaySession is one live hosted session on the relay.
type relaySession struct {
	code      string
	host      *wsconn.Conn
	guests    *session.Registry
	encrypted bool
}

// Server is a running relay.
type Server struct {
	opts     Options
	mu       sync.Mutex
	sessions map[string]*relaySession
}

var codeRE = regexp.MustCompile(`^[A-Z0-9]{4,16}$`)

const registerTimeout = 15 * time.Second

// Run starts the relay and blocks.
func Run(opts Options) error {
	s := &Server{opts: opts, sessions: make(map[string]*relaySession)}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.Addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/host", s.handleHost)
	mux.HandleFunc("/ws", s.handleGuest)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("/", webui.Handler())

	log.Printf("cotty relay listening on %s", ln.Addr())
	server := &http.Server{Handler: mux}
	return server.Serve(ln)
}

func (s *Server) joinURL(r *http.Request, code string) string {
	base := s.opts.PublicURL
	if base == "" {
		base = "ws://" + r.Host
	}
	base = strings.TrimSuffix(base, "/")
	return fmt.Sprintf("%s/ws?code=%s", base, code)
}

// handleHost registers a hosted session, forwards its frames to guests,
// and executes its control commands until the host disconnects.
func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	conn := wsconn.New(ws)

	var reg protocol.Message
	regCtx, cancel := context.WithTimeout(r.Context(), registerTimeout)
	err = conn.Read(regCtx, &reg)
	cancel()
	if err != nil || reg.Type != protocol.TypeRegister {
		conn.Close(websocket.StatusPolicyViolation, "expected register frame")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(reg.Code))
	if !codeRE.MatchString(code) {
		conn.Send(protocol.Message{Type: protocol.TypeError, Text: "invalid session code (4-16 chars, A-Z 0-9)"})
		conn.Close(websocket.StatusPolicyViolation, "invalid session code")
		return
	}

	sess := &relaySession{code: code, host: conn, guests: session.NewRegistry(reg.Writable), encrypted: reg.Encrypted}
	s.mu.Lock()
	if _, exists := s.sessions[code]; exists {
		s.mu.Unlock()
		conn.Send(protocol.Message{Type: protocol.TypeError, Text: "session code already in use"})
		conn.Close(websocket.StatusPolicyViolation, "duplicate session code")
		return
	}
	s.sessions[code] = sess
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, code)
		s.mu.Unlock()
		sess.guests.Broadcast(protocol.Message{Type: protocol.TypeInfo, Text: "host ended the session"})
		sess.guests.CloseAll()
		conn.CloseNow()
		log.Printf("session %s ended", code)
	}()

	if err := conn.Send(protocol.Message{
		Type:    protocol.TypeRegistered,
		Version: protocol.Version,
		Text:    s.joinURL(r, code),
	}); err != nil {
		return
	}
	log.Printf("session %s registered (writable=%v)", code, reg.Writable)

	for {
		var msg protocol.Message
		if err := conn.Read(r.Context(), &msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeOutput, protocol.TypeResize, protocol.TypeInfo:
			sess.guests.Broadcast(msg)
		case protocol.TypeControl:
			text, err := sess.guests.Apply(msg.Op, msg.Name)
			result := protocol.Message{Type: protocol.TypeControlResult, Ok: err == nil, Text: text}
			if err != nil {
				result.Text = err.Error()
			}
			conn.Send(result)
		}
	}
}

// handleGuest attaches a guest to a registered session.
func (s *Server) handleGuest(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code")))
	s.mu.Lock()
	sess := s.sessions[code]
	s.mu.Unlock()
	if sess == nil {
		http.Error(w, "unknown session code", http.StatusNotFound)
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	conn := wsconn.New(ws)

	g := sess.guests.Join(conn, r.URL.Query().Get("name"))
	defer func() {
		sess.guests.Leave(g)
		conn.CloseNow()
		notice := fmt.Sprintf("%s left (%d connected)", g.Name, sess.guests.Count())
		sess.host.Send(protocol.Message{Type: protocol.TypeInfo, Text: notice})
		sess.guests.Broadcast(protocol.Message{Type: protocol.TypeInfo, Text: notice})
	}()

	conn.Send(protocol.Message{
		Type:      protocol.TypeHello,
		Version:   protocol.Version,
		Text:      fmt.Sprintf("welcome to cotty session %s — you are %s", code, g.Name),
		Writable:  g.Writable,
		Encrypted: sess.encrypted,
	})
	notice := fmt.Sprintf("%s joined (%d connected)", g.Name, sess.guests.Count())
	sess.host.Send(protocol.Message{Type: protocol.TypeInfo, Text: notice})
	sess.guests.BroadcastExcept(g, protocol.Message{Type: protocol.TypeInfo, Text: notice})

	for {
		var msg protocol.Message
		if err := conn.Read(r.Context(), &msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			sess.guests.HandleInput(g, msg.Data, func(who string, data []byte) {
				sess.host.Send(protocol.Message{Type: protocol.TypeInput, Data: data, Name: who})
			})
		default:
			// Ignore unknown frames for forward compatibility.
		}
	}
}
