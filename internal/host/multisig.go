package host

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tylerbroqs/cotty/internal/protocol"
)

// proposalTTL is how long a held proposal waits for its votes.
const proposalTTL = 10 * time.Minute

// multisig holds the keystrokes of supervised participants (agents) as
// proposals until enough of the named approvers vote for them: M of N,
// like a multisig wallet. It sits on the host, after per-guest write
// permission, so a supervised agent needs both `cotty ctl allow` to
// propose and a quorum to execute, and can't route around either.
type multisig struct {
	quorum     int
	approvers  []string        // in the order given, for display
	approver   map[string]bool // by session name
	supervised map[string]bool

	// execute applies approved keystrokes to the PTY on behalf of who.
	execute func(who string, data []byte)
	// announce tells everyone in the session what happened.
	announce func(p protocol.Proposal, text string)
	// event records a step in the audit trail.
	event func(kind, who, note string)
	now   func() time.Time

	mu        sync.Mutex
	next      int
	proposals map[int]*proposal
}

type proposal struct {
	id       int
	who      string
	data     []byte
	created  time.Time
	approved []string
	rejected []string
	state    string // "pending", "executed", "rejected", "expired"
}

func newMultisig(quorum int, approvers, supervised []string) (*multisig, error) {
	m := &multisig{
		quorum:     quorum,
		approver:   map[string]bool{},
		supervised: map[string]bool{},
		proposals:  map[int]*proposal{},
		now:        time.Now,
	}
	for _, a := range approvers {
		if a = strings.TrimSpace(a); a != "" && !m.approver[a] {
			m.approver[a] = true
			m.approvers = append(m.approvers, a)
		}
	}
	for _, s := range supervised {
		if s = strings.TrimSpace(s); s != "" {
			m.supervised[s] = true
		}
	}
	switch {
	case quorum < 1:
		return nil, errors.New("-quorum must be at least 1")
	case len(m.approvers) < quorum:
		return nil, fmt.Errorf("-quorum %d needs at least %d -approvers (got %d)", quorum, quorum, len(m.approvers))
	case len(m.supervised) == 0:
		return nil, errors.New("-supervise names no one")
	}
	for s := range m.supervised {
		if m.approver[s] {
			return nil, fmt.Errorf("%s can't both be supervised and approve its own commands", s)
		}
	}
	return m, nil
}

// supervises reports whether who's keystrokes need approval.
func (m *multisig) supervises(who string) bool { return m.supervised[who] }

// hold turns keystrokes from a supervised participant into a proposal.
func (m *multisig) hold(who string, data []byte) {
	m.mu.Lock()
	m.expireLocked()
	m.next++
	p := &proposal{id: m.next, who: who, data: append([]byte(nil), data...), created: m.now(), state: "pending"}
	m.proposals[p.id] = p
	view := m.viewLocked(p)
	m.mu.Unlock()

	m.event("propose", who, fmt.Sprintf("#%d %s", p.id, view.Command))
	m.announce(view, fmt.Sprintf("#%d %s wants to run: %s  (needs %d of %s; vote with: cotty approve %d / cotty reject %d)",
		p.id, who, view.Command, m.quorum, strings.Join(m.approvers, ", "), p.id, p.id))
}

// vote records an approver's approve or reject on a pending proposal,
// executing it once the quorum is reached and rejecting it once the
// quorum can no longer be reached.
func (m *multisig) vote(voter, op, idText string) (string, error) {
	if op != "approve" && op != "reject" {
		return "", fmt.Errorf("unknown vote %q", op)
	}
	id, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(idText), "#"))
	if err != nil {
		return "", fmt.Errorf("usage: cotty %s PROPOSAL-NUMBER", op)
	}
	if !m.approver[voter] {
		return "", fmt.Errorf("%s is not an approver (approvers: %s)", voter, strings.Join(m.approvers, ", "))
	}

	m.mu.Lock()
	m.expireLocked()
	p := m.proposals[id]
	switch {
	case p == nil:
		m.mu.Unlock()
		return "", fmt.Errorf("no proposal #%d", id)
	case p.state != "pending":
		m.mu.Unlock()
		return "", fmt.Errorf("proposal #%d is already %s", id, p.state)
	case contains(p.approved, voter) || contains(p.rejected, voter):
		m.mu.Unlock()
		return "", fmt.Errorf("%s already voted on #%d", voter, id)
	}
	if op == "approve" {
		p.approved = append(p.approved, voter)
	} else {
		p.rejected = append(p.rejected, voter)
	}
	execute := len(p.approved) >= m.quorum
	if execute {
		p.state = "executed"
	} else if len(m.approvers)-len(p.rejected) < m.quorum {
		p.state = "rejected"
	}
	view := m.viewLocked(p)
	data := p.data
	m.mu.Unlock()

	m.event(op, voter, fmt.Sprintf("#%d", id))
	var text string
	switch view.State {
	case "executed":
		text = fmt.Sprintf("#%d approved %d/%d by %s; running: %s", id, len(view.Approved), m.quorum, strings.Join(view.Approved, ", "), view.Command)
		m.event("execute", p.who, fmt.Sprintf("#%d approved by %s", id, strings.Join(view.Approved, ", ")))
		m.announce(view, text)
		m.execute(p.who, data)
		return text, nil
	case "rejected":
		text = fmt.Sprintf("#%d rejected by %s; it will not run", id, strings.Join(view.Rejected, ", "))
		m.event("rejected", p.who, fmt.Sprintf("#%d", id))
	default:
		text = fmt.Sprintf("#%d %sd by %s (%d/%d)", id, op, voter, len(view.Approved), m.quorum)
	}
	m.announce(view, text)
	return text, nil
}

// pending lists proposals still waiting for votes.
func (m *multisig) pending() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	var ids []int
	for id, p := range m.proposals {
		if p.state == "pending" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "no proposals waiting"
	}
	sort.Ints(ids)
	var b strings.Builder
	for i, id := range ids {
		v := m.viewLocked(m.proposals[id])
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "#%d  %s: %s  (%d/%d approvals", id, v.Who, v.Command, len(v.Approved), v.Quorum)
		if len(v.Approved) > 0 {
			b.WriteString(": " + strings.Join(v.Approved, ", "))
		}
		b.WriteString(")")
	}
	return b.String()
}

// expireLocked closes proposals nobody voted through in time, and forgets
// closed ones after a while.
func (m *multisig) expireLocked() {
	now := m.now()
	for id, p := range m.proposals {
		age := now.Sub(p.created)
		switch {
		case p.state == "pending" && age > proposalTTL:
			p.state = "expired"
			m.event("expired", p.who, fmt.Sprintf("#%d", id))
		case p.state != "pending" && age > 4*proposalTTL:
			delete(m.proposals, id)
		}
	}
}

func (m *multisig) viewLocked(p *proposal) protocol.Proposal {
	return protocol.Proposal{
		ID:        p.id,
		Who:       p.who,
		Command:   printable(p.data),
		Quorum:    m.quorum,
		Approvers: m.approvers,
		Approved:  append([]string(nil), p.approved...),
		Rejected:  append([]string(nil), p.rejected...),
		State:     p.state,
	}
}

// printable shows keystrokes as text someone can judge: Enter as ⏎,
// other control keys as ^X, escape sequences as ^[.
func printable(data []byte) string {
	var b strings.Builder
	for _, r := range string(data) {
		switch {
		case r == '\r' || r == '\n':
			b.WriteString("⏎")
		case r < 0x20:
			b.WriteString("^" + string(rune(r+'@')))
		case r == 0x7f:
			b.WriteString("^?")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
