// Package bus is the live channel between everything that speaks partyline: the CLI on your
// machine, each agent session, and a teammate's machine across the internet.
//
// WHY IT EXISTS. Shared project memory travels by git, which is the right DURABLE store —
// reviewable, offline, no server required. What git cannot do is tell your session that a
// teammate recorded something thirty seconds ago; polling does that, badly, and only at the
// interval you are willing to spend. The bus carries the news. The two are layered on purpose:
// git holds the truth, the bus says it changed. If the bus is down the system degrades to the
// interval; if git is unreachable the memory is still on every machine that has it.
//
// The envelope is defined apart from any transport so the wire format can be tested without a
// socket, and so a second transport (the SSE stream, a future QUIC path) can carry the same
// messages without redefining them.
package bus

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Type is what a message is. A CLOSED SET, deliberately: an open one becomes a dumping ground,
// and every consumer then has to guess what it may ignore.
type Type string

const (
	// TypeMemoryChanged: somebody recorded a fact. Carries the project and the fact id, never
	// the fact itself — the receiver pulls from git, so the bus stays a notification channel and
	// cannot become a second, divergent copy of the memory.
	TypeMemoryChanged Type = "memory.changed"
	// TypeAsk / TypeAnswer: one agent asking another a question, and the reply. This is the
	// thing people currently do by pasting a session into chat and waiting.
	TypeAsk    Type = "ask"
	TypeAnswer Type = "answer"
	// TypePresence: a machine or session arriving or leaving, so a fleet view is live rather
	// than polled.
	TypePresence Type = "presence"
)

// Envelope is one message on the bus.
type Envelope struct {
	Type Type `json:"type"`
	// Project scopes delivery: a message is only ever fanned out to connections that declared
	// the same project. Cross-project leakage is the one mistake a shared bus must not make.
	Project string `json:"project,omitempty"`
	// From is the sending connection's identity, filled in BY THE SERVER. A client-supplied
	// sender would let any connection impersonate a teammate.
	From string `json:"from,omitempty"`
	// To addresses one session or machine; empty means everyone in the project.
	To      string          `json:"to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	At      time.Time       `json:"at,omitempty"`
	// ID lets a reply name what it is replying to (ask → answer).
	ID string `json:"id,omitempty"`
}

var errUnknownType = errors.New("unknown message type")

// Validate checks an inbound message before it is fanned out. Fails closed: an envelope the
// server does not understand is dropped with a reason, never forwarded on the assumption that
// some other client will know what to do with it.
func (e Envelope) Validate() error {
	switch e.Type {
	case TypeMemoryChanged, TypeAsk, TypeAnswer, TypePresence:
	default:
		return errUnknownType
	}
	if strings.TrimSpace(e.Project) == "" {
		return errors.New("every message names a project — delivery is scoped to one")
	}
	if len(e.Payload) > maxPayload {
		return errors.New("payload too large — the bus carries news, not content")
	}
	return nil
}

// maxPayload is small on purpose. The bus says what changed; the receiver fetches it from the
// store. A generous limit here would invite agents to ship transcripts through the notification
// channel, which is how a bus turns into an unreviewable, unpersisted second database.
const maxPayload = 8 << 10

// Deliver decides whether a connection should receive this message: same project, never the
// sender's own echo, and an addressed message only to its addressee.
func (e Envelope) Deliver(toIdentity, toProject string) bool {
	if e.Project != toProject {
		return false
	}
	if e.From != "" && e.From == toIdentity {
		return false
	}
	if e.To != "" && e.To != toIdentity {
		return false
	}
	return true
}
