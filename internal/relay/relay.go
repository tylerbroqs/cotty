// Package relay implements `cotty relay`: a public rendezvous server that
// lets hosts behind NAT share sessions. Hosts dial the /host endpoint and
// register a session code; guests join via /ws?code=... exactly like they
// would join a locally hosted session, so `cotty join` needs no changes.
//
// The relay only forwards frames — in v0.2 it can read them. End-to-end
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

	"github.com/tylerbroqs/cotty/internal/hub"
	"github.com/tylerbroqs/cotty/internal/protocol"
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

// session is one live hosted session on the relay.
type session struct {
	code     string
	writable bool
	host     *wsconn.Conn
	guests   *hub.Hub
}

// Server is a running relay.
type Server struct {
	opts     Options
	mu       sync.Mutex
	sessions map[string]*session
}

var codeRE = regexp.MustCompile(`^[A-Z0-9]{4,16}$`)

const registerTimeout = 15 * time.Second

// Run starts the relay and blocks.
func Run(opts Options) error {
	s := &Server{opts: opts, sessions: make(map[string]*session)}

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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		n := len(s.sessions)
		s.mu.Unlock()
		fmt.Fprintf(w, "cotty relay — %d active session(s)\n", n)
	})

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

// handleHost registers a hosted session and forwards its frames to guests
// until the host disconnects.
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

	sess := &session{code: code, writable: reg.Writable, host: conn, guests: hub.New()}
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
	g := wsconn.New(ws)

	sess.guests.Add(g)
	defer func() {
		sess.guests.Remove(g)
		g.CloseNow()
		sess.host.Send(protocol.Message{
			Type: protocol.TypeInfo,
			Text: fmt.Sprintf("guest left (%d connected)", sess.guests.Count()),
		})
	}()

	g.Send(protocol.Message{
		Type:     protocol.TypeHello,
		Version:  protocol.Version,
		Text:     "welcome to cotty session " + code,
		Writable: sess.writable,
	})
	sess.host.Send(protocol.Message{
		Type: protocol.TypeInfo,
		Text: fmt.Sprintf("guest joined (%d connected)", sess.guests.Count()),
	})

	warnedReadOnly := false
	for {
		var msg protocol.Message
		if err := g.Read(r.Context(), &msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			if sess.writable {
				sess.host.Send(msg)
			} else if !warnedReadOnly {
				warnedReadOnly = true
				g.Send(protocol.Message{
					Type: protocol.TypeInfo,
					Text: "this session is view-only; the host started it without --write",
				})
			}
		default:
			// Ignore unknown frames for forward compatibility.
		}
	}
}
