// Package ctl is the control channel for a running `cotty host`: a unix
// socket next to the session that `cotty ctl` connects to for guest
// management (list/allow/deny/kick). One JSON request and one JSON reply
// per connection.
package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const callTimeout = 15 * time.Second

// Handler executes one control command.
type Handler func(op, name string) (string, error)

type request struct {
	Op   string `json:"op"`
	Name string `json:"name,omitempty"`
}

type response struct {
	Ok   bool   `json:"ok"`
	Text string `json:"text"`
}

// SocketPath returns the control socket path for a session code.
func SocketPath(code string) string {
	return filepath.Join(os.TempDir(), "cotty-"+strings.ToUpper(code)+".sock")
}

// Server is a listening control socket.
type Server struct {
	ln   net.Listener
	path string
}

// Serve listens on path and dispatches commands to h until Close.
func Serve(path string, h Handler) (*Server, error) {
	os.Remove(path) // a stale socket from a dead session
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on control socket %s: %w", path, err)
	}
	s := &Server{ln: ln, path: path}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn, h)
		}
	}()
	return s, nil
}

func handle(conn net.Conn, h Handler) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(callTimeout))

	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	text, err := h(req.Op, req.Name)
	resp := response{Ok: err == nil, Text: text}
	if err != nil {
		resp.Text = err.Error()
	}
	json.NewEncoder(conn).Encode(resp)
}

func (s *Server) Close() error {
	err := s.ln.Close()
	os.Remove(s.path)
	return err
}

// Call sends one command to the control socket at path.
func Call(path, op, name string) (string, error) {
	conn, err := net.DialTimeout("unix", path, callTimeout)
	if err != nil {
		return "", fmt.Errorf("no cotty session at %s (is the host still running?)", path)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(callTimeout))

	if err := json.NewEncoder(conn).Encode(request{Op: op, Name: name}); err != nil {
		return "", err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return "", err
	}
	if !resp.Ok {
		return "", errors.New(resp.Text)
	}
	return resp.Text, nil
}

// Discover finds the control socket to talk to: an explicit -code wins,
// then $COTTY_SESSION (set inside every hosted shell), then a lone
// session on this machine.
func Discover(code string) (string, error) {
	if code != "" {
		return existing(SocketPath(code))
	}
	if env := os.Getenv("COTTY_SESSION"); env != "" {
		return existing(SocketPath(env))
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "cotty-*.sock"))
	switch len(matches) {
	case 0:
		return "", errors.New("no active cotty session found on this machine")
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%d active sessions found; pick one with -code", len(matches))
	}
}

func existing(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no session socket at %s", path)
	}
	return path, nil
}
