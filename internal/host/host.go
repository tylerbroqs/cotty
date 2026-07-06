// Package host implements `cotty host`: it spawns the user's shell in a
// PTY, keeps the local terminal attached as usual, and shares the session
// with guests — either by listening locally or by dialing out to a relay.
package host

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/tylerbroqs/cotty/internal/protocol"
)

// Options configures a hosted session.
type Options struct {
	// Addr is the listen address for guests, e.g. ":7373". Ignored when
	// Relay is set.
	Addr string
	// Shell overrides $SHELL. Empty means $SHELL, falling back to /bin/sh.
	Shell string
	// AllowWrite lets guests send input to the PTY. Default is view-only.
	AllowWrite bool
	// Code overrides the generated session code (useful for tests).
	Code string
	// Relay is a relay server to host through (e.g. "relay.example.com:7374").
	// When set, the host dials out instead of listening, so it works from
	// behind NAT without port forwarding.
	Relay string
}

// transport delivers frames from the host to its guests. Guest input
// flows back through the writeInput callback each transport is built with.
type transport interface {
	broadcast(protocol.Message)
	close()
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

	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "COTTY_SESSION="+code)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("starting %s in a pty: %w", shell, err)
	}
	defer ptmx.Close()

	// writeInput applies guest keystrokes. The AllowWrite check lives here
	// so the host enforces it even if the far side (a relay) misbehaves.
	writeInput := func(data []byte) {
		if opts.AllowWrite {
			ptmx.Write(data)
		}
	}

	var (
		tr      transport
		joinURL string
	)
	if opts.Relay != "" {
		tr, joinURL, err = dialRelay(opts.Relay, code, opts.AllowWrite, writeInput)
	} else {
		tr, joinURL, err = listenLocal(opts.Addr, code, opts.AllowWrite, writeInput)
	}
	if err != nil {
		return err
	}
	defer tr.close()

	mode := "view-only"
	if opts.AllowWrite {
		mode = "read-write"
	}
	where := "locally"
	if opts.Relay != "" {
		where = "via relay " + opts.Relay
	}
	fmt.Fprintf(os.Stderr, "cotty: hosting %s %s (guests are %s)\n", shell, where, mode)
	fmt.Fprintf(os.Stderr, "cotty: session code %s\n", code)
	fmt.Fprintf(os.Stderr, "cotty: guests join with: cotty join %q\n\n", joinURL)

	// Attach the local terminal. When stdin isn't a TTY (headless hosting,
	// tests, CI) skip raw mode and size handling instead of failing.
	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		if err := pty.InheritSize(os.Stdin, ptmx); err == nil {
			broadcastSize(tr, ptmx)
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if err := pty.InheritSize(os.Stdin, ptmx); err == nil {
					broadcastSize(tr, ptmx)
				}
			}
		}()

		oldState, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("setting raw mode: %w", err)
		}
		defer term.Restore(stdinFd, oldState)
	}

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
			tr.broadcast(protocol.Message{Type: protocol.TypeOutput, Data: data})
		}
		if err != nil {
			break
		}
	}

	// The "host ended the session" notice is each transport's job: the
	// local transport sends it on close, the relay sends it to guests when
	// the host connection drops.
	cmd.Wait()
	fmt.Fprintf(os.Stderr, "\r\ncotty: session ended\r\n")
	return nil
}

func broadcastSize(tr transport, ptmx *os.File) {
	if ws, err := pty.GetsizeFull(ptmx); err == nil {
		tr.broadcast(protocol.Message{
			Type: protocol.TypeResize,
			Cols: int(ws.Cols),
			Rows: int(ws.Rows),
		})
	}
}
