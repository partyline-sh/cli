package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The local half of peer messaging. Why a local store exists at all: the ASK is short (you type a
// question and want your session back) but the ANSWER is long — the peer may be a human who has to
// type `approve-consult` in a daemon console, and the answering side gets a whole read-only engine
// turn (consult_answer.go consultTimeout = 5m). Foreground waiting can therefore never be the
// primary path; cancelling has to be free, and free means the answer must survive the cancel. It
// lands here, and the mux banners you when it does.
//
// The record is a neutral MESSAGE with a direction, not "my pending consults". Outbound (I asked a
// peer) is all that exists today; inbound (a peer asked me — answered from the daemon console at
// daemon.go approve-consult) is the other half of the same conversation and will land in this same
// list, so the shape and the renderer already carry the distinction.

const (
	dirOutbound = "outbound" // I asked them
	dirInbound  = "inbound"  // they asked me (not produced yet — see the report)

	msgWaiting = "waiting" // asked, no terminal state yet

	// How long a message stays on disk. Longer than the server's consult window so a resolved
	// answer survives a lunch break, short enough that the file stays a few KB.
	peerMsgTTL = 48 * time.Hour
)

// peerMessage is one question-and-answer between this machine and a peer's agent.
type peerMessage struct {
	ID         string    `json:"id"` // consult id — the server-side handle
	Direction  string    `json:"direction"`
	Peer       string    `json:"peer"`    // device label
	Project    string    `json:"project"` // advertised project label the consult is scoped to
	Question   string    `json:"question"`
	Answer     string    `json:"answer,omitempty"`
	Status     string    `json:"status"` // waiting | answered | declined | timed_out | failed
	AskedAt    time.Time `json:"asked_at"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
	Read       bool      `json:"read"`
}

// Resolved reports whether the message has stopped moving on its own.
func (m peerMessage) Resolved() bool { return m.Status != "" && m.Status != msgWaiting }

var peerStoreMu sync.Mutex // the watcher goroutines and the modal both write this file

func peerStorePath() string { return filepath.Join(stateDir(), "peer-messages.json") }

// loadPeerMessagesAt reads the store, newest first. A missing or corrupt file is an empty list —
// this is a cache of remote state, never something to fail a menu over.
func loadPeerMessagesAt(path string) []peerMessage {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out struct {
		Messages []peerMessage `json:"messages"`
	}
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	sort.SliceStable(out.Messages, func(i, j int) bool {
		return out.Messages[i].AskedAt.After(out.Messages[j].AskedAt)
	})
	return out.Messages
}

func savePeerMessagesAt(path string, msgs []peerMessage) error {
	b, err := json.MarshalIndent(struct {
		Messages []peerMessage `json:"messages"`
	}{msgs}, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// prunePeerMessages drops what nobody will read again: anything past the TTL, and anything already
// read (its answer is in the session, which is the durable copy). Pure, so the retention rule is
// testable without a filesystem.
func prunePeerMessages(msgs []peerMessage, now time.Time) []peerMessage {
	kept := make([]peerMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Read || now.Sub(m.AskedAt) > peerMsgTTL {
			continue
		}
		kept = append(kept, m)
	}
	return kept
}

// putPeerMessageAt upserts by id and prunes, so every write bounds the file.
func putPeerMessageAt(path string, m peerMessage) error {
	msgs := prunePeerMessages(loadPeerMessagesAt(path), time.Now())
	found := false
	for i := range msgs {
		if msgs[i].ID == m.ID {
			msgs[i], found = m, true
			break
		}
	}
	if !found {
		msgs = append(msgs, m)
	}
	return savePeerMessagesAt(path, msgs)
}

// markPeerMessageReadAt marks one read; the next prune drops it.
func markPeerMessageReadAt(path, id string) error {
	msgs := loadPeerMessagesAt(path)
	for i := range msgs {
		if msgs[i].ID == id {
			msgs[i].Read = true
		}
	}
	return savePeerMessagesAt(path, prunePeerMessages(msgs, time.Now()))
}

// ---- the locked, default-path wrappers the UI and the watcher actually call ----

func putPeerMessage(m peerMessage) {
	peerStoreMu.Lock()
	defer peerStoreMu.Unlock()
	_ = putPeerMessageAt(peerStorePath(), m)
}

func markPeerMessageRead(id string) {
	peerStoreMu.Lock()
	defer peerStoreMu.Unlock()
	_ = markPeerMessageReadAt(peerStorePath(), id)
}

// openPeerMessages is the inbox: every unread message, newest first — answers that came back while
// you worked, plus asks still out there. Pruning on read keeps this list the whole store.
func openPeerMessages() []peerMessage {
	peerStoreMu.Lock()
	defer peerStoreMu.Unlock()
	path := peerStorePath()
	msgs := prunePeerMessages(loadPeerMessagesAt(path), time.Now())
	_ = savePeerMessagesAt(path, msgs)
	return msgs
}
