// Package protocol defines the wire format for cotty sessions.
//
// Version 0 is intentionally simple: JSON frames over a websocket. The
// framing is expected to be replaced by a binary protocol (and eventually
// CRDT-based shared state) in later versions; keeping v0 as JSON makes the
// protocol trivially debuggable with any websocket client.
package protocol

// Version is the protocol version spoken by this build.
const Version = 0

// MsgType discriminates websocket frames.
type MsgType string

const (
	// TypeHello is sent by the host to a guest right after it joins.
	TypeHello MsgType = "hello"
	// TypeOutput carries raw PTY output from the host to guests.
	TypeOutput MsgType = "output"
	// TypeInput carries keystrokes from a guest to the host PTY.
	TypeInput MsgType = "input"
	// TypeResize announces the host terminal size to guests.
	TypeResize MsgType = "resize"
	// TypeInfo carries human-readable notices (join/leave, read-only, ...).
	TypeInfo MsgType = "info"
	// TypeRegister is the first frame a relay-hosted session sends on a
	// relay's /host endpoint; it carries Code and Writable.
	TypeRegister MsgType = "register"
	// TypeRegistered is the relay's success reply; Text carries the URL
	// guests should join with.
	TypeRegistered MsgType = "registered"
	// TypeError reports a fatal protocol error; Text says why.
	TypeError MsgType = "error"
	// TypeControl carries a guest-management command (list/allow/deny/kick)
	// from a relay-hosted session's host to the relay; Op and Name hold the
	// command and its target.
	TypeControl MsgType = "control"
	// TypeControlResult answers a TypeControl frame; Ok and Text carry the
	// outcome.
	TypeControlResult MsgType = "control-result"
	// TypeWritable tells a guest its write permission changed.
	TypeWritable MsgType = "writable"
)

// Message is a single frame. Data is base64-encoded by encoding/json.
type Message struct {
	Type    MsgType `json:"type"`
	Version int     `json:"v,omitempty"`
	Data    []byte  `json:"data,omitempty"`
	Cols    int     `json:"cols,omitempty"`
	Rows    int     `json:"rows,omitempty"`
	Text    string  `json:"text,omitempty"`
	// Code is the session code, set on TypeRegister frames.
	Code string `json:"code,omitempty"`
	// Writable tells a guest whether its input will be applied.
	Writable bool `json:"writable,omitempty"`
	// Op is the command on TypeControl frames: list, allow, deny, kick.
	Op string `json:"op,omitempty"`
	// Name is the guest a TypeControl frame targets.
	Name string `json:"name,omitempty"`
	// Ok reports success on TypeControlResult frames.
	Ok bool `json:"ok,omitempty"`
	// Encrypted marks a session as end-to-end encrypted, on TypeRegister
	// (host to relay) and TypeHello (to each guest) frames.
	Encrypted bool `json:"enc,omitempty"`
}
