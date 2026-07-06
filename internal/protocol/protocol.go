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
)

// Message is a single frame. Data is base64-encoded by encoding/json.
type Message struct {
	Type    MsgType `json:"type"`
	Version int     `json:"v,omitempty"`
	Data    []byte  `json:"data,omitempty"`
	Cols    int     `json:"cols,omitempty"`
	Rows    int     `json:"rows,omitempty"`
	Text    string  `json:"text,omitempty"`
	// Writable tells a guest whether its input will be applied.
	Writable bool `json:"writable,omitempty"`
}
