// Package protocol defines the wire format for cotty sessions.
//
// Version 0 is intentionally simple: JSON frames over a websocket. The
// framing is expected to be replaced by a binary protocol (and eventually
// CRDT-based shared state) in later versions; keeping v0 as JSON makes the
// protocol trivially debuggable with any websocket client.
package protocol

import "strings"

// SanitizeText strips terminal control characters from remote-supplied
// notice text (TypeInfo, TypeControlResult, ...). Notices travel outside
// the end-to-end encryption and are printed to the local terminal, so
// without this a malicious peer or relay could inject ANSI escape
// sequences into it.
func SanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// SanitizeLines is SanitizeText for multi-line text (guest lists, agent
// answers): it keeps line breaks, normalized to \n, and strips every
// other control character.
func SanitizeLines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = SanitizeText(l)
	}
	return strings.Join(lines, "\n")
}

// MaxSayBytes caps the text of one TypeAsk or TypeSay frame.
const MaxSayBytes = 8 << 10

// FormatSay renders a participant's chat text (sanitized with
// SanitizeLines) for a raw-mode terminal: the speaker on the first line,
// continuation lines indented beneath it.
func FormatSay(who, text string) string {
	lines := strings.Split(text, "\n")
	var b strings.Builder
	b.WriteString("\r\ncotty: " + who + ": " + lines[0] + "\r\n")
	pad := strings.Repeat(" ", len("cotty: ")+len(who)+2)
	for _, l := range lines[1:] {
		b.WriteString(pad + l + "\r\n")
	}
	return b.String()
}

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
	// TypeActivity announces that the guest in Name is typing; sent to
	// other participants, throttled per guest.
	TypeActivity MsgType = "activity"
	// TypeAsk carries a question for the session's agents (`cotty ask`),
	// from the host to every guest. Data holds the question, sealed like
	// output in end-to-end encrypted sessions; Name is who asked.
	TypeAsk MsgType = "ask"
	// TypeSay carries a participant's chat text, typically an agent's
	// answer. Guests send it to the host (a relay attributes it with
	// Name); the host then fans it out to every guest with Name set.
	// Data holds the text, sealed in end-to-end encrypted sessions.
	TypeSay MsgType = "say"
	// TypeProposal tells guests about keystrokes a supervised
	// participant (an agent) proposed, and their votes so far: Data is a
	// Proposal as JSON, sealed like output in encrypted sessions.
	TypeProposal MsgType = "proposal"
	// TypeVote carries a guest's vote on a proposal: Data is a Vote as
	// JSON, sealed in encrypted sessions so a relay can neither read nor
	// forge it. The relay attributes it in Name, and the host checks that
	// against the voter sealed inside.
	TypeVote MsgType = "vote"
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
	// Name is the guest a TypeControl frame targets, the typist on
	// TypeActivity frames, and the attributed sender on TypeInput frames
	// forwarded by a relay.
	Name string `json:"name,omitempty"`
	// Ok reports success on TypeControlResult frames.
	Ok bool `json:"ok,omitempty"`
	// Encrypted marks a session as end-to-end encrypted, on TypeRegister
	// (host to relay) and TypeHello (to each guest) frames.
	Encrypted bool `json:"enc,omitempty"`
}

// Proposal is the state of keystrokes held for multisig approval.
type Proposal struct {
	ID        int      `json:"id"`
	Who       string   `json:"who"`
	Command   string   `json:"command"` // the keystrokes, control keys shown as ⏎ and ^X
	Quorum    int      `json:"quorum"`
	Approvers []string `json:"approvers"`
	Approved  []string `json:"approved"`
	Rejected  []string `json:"rejected"`
	State     string   `json:"state"` // pending, executed, rejected, expired
}

// Vote is one approver's vote on a Proposal.
type Vote struct {
	ID    int    `json:"id"`
	Op    string `json:"op"` // approve or reject
	Voter string `json:"voter"`
}
