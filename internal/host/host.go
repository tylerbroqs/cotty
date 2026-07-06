// Package host implements `cotty host`: it spawns the user's shell in a
// PTY, keeps the local terminal attached as usual, and serves the session
// over a websocket so guests can watch (and, if allowed, type).
package host

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/tylerbroqs/cotty/internal/protocol"
)

// Options configures a hosted session.
type Options struct {
	// Addr is the listen address for guests, e.g. ":7373".
	Addr string
	// Shell overrides $SHELL. Empty means $SHELL, falling back to /bin/sh.
	Shell string
	// AllowWrite lets guests send input to the PTY. Default is view-only.
	AllowWrite bool
	// Code overrides the generated session code (useful for tests).
	Code string
}

// Host is a running session.
type Host struct {
	opts Options
	code string
	hub  *hub
	ptmx *os.File
}

// codeAlphabet avoids ambiguous characters (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b), nil
}

// Run hosts a session and blocks until the shell exits.
func Run(opts Options) error {
	code := opts.Code
	if code == "" {
		var err error
		if code, err = newCode(); err != nil {
			return fmt.Errorf("generating session code: %w", err)
		}
	}

	shell := opts.Shell
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}

	h := &Host{opts: opts, code: code, hub: newHub()}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.Addr, err)
	}

	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "COTTY_SESSION="+code)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		ln.Close()
		return fmt.Errorf("starting %s in a pty: %w", shell, err)
	}
	h.ptmx = ptmx
	defer ptmx.Close()

	mode := "view-only"
	if opts.AllowWrite {
		mode = "read-write"
	}
	joinHost := ln.Addr().String()
	if hostPart, port, err := net.SplitHostPort(joinHost); err == nil {
		if hostPart == "" || hostPart == "::" || hostPart == "0.0.0.0" {
			joinHost = "<this-host>:" + port
		}
	}
	fmt.Fprintf(os.Stderr, "cotty: hosting %s on %s (guests are %s)\n", shell, ln.Addr(), mode)
	fmt.Fprintf(os.Stderr, "cotty: session code %s\n", code)
	fmt.Fprintf(os.Stderr, "cotty: guests join with: cotty join \"ws://%s/ws?code=%s\"\n\n", joinHost, code)

	// Attach the local terminal. When stdin isn't a TTY (headless hosting,
	// tests, CI) skip raw mode and size handling instead of failing.
	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		if err := pty.InheritSize(os.Stdin, ptmx); err == nil {
			h.broadcastSize()
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if err := pty.InheritSize(os.Stdin, ptmx); err == nil {
					h.broadcastSize()
				}
			}
		}()

		oldState, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("setting raw mode: %w", err)
		}
		defer term.Restore(stdinFd, oldState)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.handleWS)
	server := &http.Server{Handler: mux}
	go server.Serve(ln)
	defer server.Close()

	// Local keystrokes go straight to the PTY.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if _, werr := ptmx.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// PTY output goes to the local terminal and every guest. Reading the
	// PTY returns EIO once the shell exits, which ends the session.
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			data := make([]byte, n)
			copy(data, buf[:n])
			h.hub.broadcast(protocol.Message{Type: protocol.TypeOutput, Data: data})
		}
		if err != nil {
			break
		}
	}

	h.hub.broadcast(protocol.Message{Type: protocol.TypeInfo, Text: "host ended the session"})
	cmd.Wait()
	fmt.Fprintf(os.Stderr, "\r\ncotty: session ended\r\n")
	return nil
}

func (h *Host) broadcastSize() {
	if ws, err := pty.GetsizeFull(h.ptmx); err == nil {
		h.hub.broadcast(protocol.Message{
			Type: protocol.TypeResize,
			Cols: int(ws.Cols),
			Rows: int(ws.Rows),
		})
	}
}

func (h *Host) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("code") != h.code {
		http.Error(w, "invalid session code", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)

	g := h.hub.add(ws)
	defer func() {
		h.hub.remove(g)
		ws.CloseNow()
		fmt.Fprintf(os.Stderr, "\r\ncotty: guest left (%d connected)\r\n", h.hub.count())
	}()

	g.send(protocol.Message{
		Type:     protocol.TypeHello,
		Version:  protocol.Version,
		Text:     "welcome to cotty session " + h.code,
		Writable: h.opts.AllowWrite,
	})
	fmt.Fprintf(os.Stderr, "\r\ncotty: guest joined (%d connected)\r\n", h.hub.count())

	warnedReadOnly := false
	for {
		var msg protocol.Message
		if err := wsjson.Read(r.Context(), ws, &msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			if h.opts.AllowWrite {
				h.ptmx.Write(msg.Data)
			} else if !warnedReadOnly {
				warnedReadOnly = true
				g.send(protocol.Message{
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
