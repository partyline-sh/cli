package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The local half of peer messaging. Why a local store exists at all: the ASK is short (you type a
// question and want your session back) but the ANSWER is long — the peer may be a human who has to
// type `approve-consult` in a daemon console, and the answering side gets a whole read-only engine
// turn (consult_answer.go consultTimeout = 5m). Foreground waiting can therefore never be the
// primary path; cancelling has to be free, and free means the answer must survive the cancel. It
// lands here, and the mux banners you when it does.
//
// The record is a neutral MESSAGE with a direction, not "my pending consults". Outbound (I asked a
// peer) lives ON DISK because its answer must outlive the ask. Inbound (a peer asked me) does NOT:
// its truth is the durable consult row, readable at any time from the control plane
// (api.ListConsults), and caching it locally could only ever show a question that was already
// answered. So the inbox is the union of the two — the disk store plus a live inbound read — and both
// halves render through the same row.

const (
	dirOutbound = "outbound" // I asked them
	dirInbound  = "inbound"  // they asked me

	// How long a message stays on disk. Longer than the server's consult window so a resolved
	// answer survives a lunch break, short enough that the file stays a few KB.
	peerMsgTTL = 48 * time.Hour
)

// peerMessage.Status uses A2A TaskState names (Google's Agent2Agent protocol, now Linux Foundation),
// whose task lifecycle is this flow exactly. Chosen while these identifiers are still soft and local,
// so a future A2A gateway is an ADAPTER over this store rather than a rewrite of it.
//
// These are OUR internal identifiers only. The `consults` table and the web API keep their own
// vocabulary (pending / delivered / answered / declined / timed_out / failed) — renaming those is a
// migration, and not this change. a2aTaskState is the boundary: every wire status is translated on the
// way in, exactly once. Human-facing copy stays human (peerStateLabel) — nobody wants to read
// "completed" where the sentence means "they answered you".
const (
	taskSubmitted    = "submitted"     // acknowledged by the control plane, not started
	taskAuthRequired = "auth_required" // interrupted: a human has to decide before any work happens
	taskWorking      = "working"       // the answering engine turn is running
	taskCompleted    = "completed"     // terminal success — an answer came back
	taskRejected     = "rejected"      // terminal: the peer declined
	taskFailed       = "failed"        // terminal error, including the consult window expiring
	taskCanceled     = "canceled"      // terminal, user-initiated (A2A's US spelling). The ASKER withdrew the
	// question: `ptln peer cancel <id>`, or cancel from the ctrl-\ p inbox. This state had no producer
	// until cancel existed — esc on a wait only hands the ask to the watcher, which is not a withdrawal —
	// and the whole point of naming it early was that the state machine would be complete when one arrived.
	// The wire status is `canceled` too (supabase/migrations/20260726000000_consult_cancel.sql), so the
	// translation below is the identity for once.
)

// a2aTaskState maps a control-plane consult status onto the A2A TaskState we store. The only place the
// wire vocabulary is allowed to leak in.
//
// `delivered` (the target daemon has the question and its owner hasn't decided) is genuinely
// auth_required, not submitted — that distinction is the reason to use A2A's names rather than one
// "waiting". Nothing writes `delivered` server-side yet, so today the distinction shows up on the
// inbound side instead: a question sitting in YOUR inbox is by definition waiting on YOUR decision.
func a2aTaskState(wire string) string {
	switch wire {
	case "pending":
		return taskSubmitted
	case "delivered":
		return taskAuthRequired
	case "answered":
		return taskCompleted
	case "declined":
		return taskRejected
	case "timed_out", "failed":
		return taskFailed
	case "canceled":
		return taskCanceled // the asker withdrew it — terminal, and OUR doing, not the peer's
	}
	return taskSubmitted // an unknown status is still an open task, not a terminal one
}

// peerStateLabel is the human word for a state. The identifiers are A2A's; the screen isn't.
func peerStateLabel(status string) string {
	switch status {
	case taskSubmitted:
		return "still out"
	case taskAuthRequired:
		return "waiting on you"
	case taskWorking:
		return "answering"
	case taskCompleted:
		return "answered"
	case taskRejected:
		return "declined"
	case taskFailed:
		return "no answer"
	case taskCanceled:
		// "withdrawn", not "canceled": the identifier is A2A's, the screen is a human's, and on an inbox row
		// this state always means YOU took the question back.
		return "withdrawn"
	}
	return status
}

// peerMessage is one question-and-answer between this machine and a peer's agent.
type peerMessage struct {
	ID         string    `json:"id"` // consult id — the server-side handle
	Direction  string    `json:"direction"`
	Peer       string    `json:"peer"`    // outbound: the device we asked · inbound: who asked us
	Project    string    `json:"project"` // advertised project label the consult is scoped to
	Question   string    `json:"question"`
	Answer     string    `json:"answer,omitempty"`
	Status     string    `json:"status"` // an A2A TaskState — see the constants below
	AskedAt    time.Time `json:"asked_at"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
	Read       bool      `json:"read"`
	// Inbound only: the daemon that must ANSWER this question, and its label. Answering needs that
	// machine's device token and its own checkout (see daemon_control.go), so these are what let the
	// modal say "answer this on <device>" instead of failing obscurely on the wrong box.
	Daemon string `json:"daemon,omitempty"`
	Device string `json:"device,omitempty"`
	// Session (outbound only) is the llms session key of the agent that ASKED — the mux child key,
	// which cg-mcp learns from PARTYLINE_SESSION_KEY and the modal from mx.ActiveLaunch(). It is the
	// delivery address, and it is the ONLY one: an answer is injected into the session that asked or
	// into nothing at all. Never the focused session, never every session (peer_deliver.go).
	Session string `json:"session,omitempty"`
	// Delivered records that the answer was put into that session's prompt, so a rescan can't stage
	// the same block twice — a duplicate paste is indistinguishable from the peer answering twice.
	Delivered bool `json:"delivered,omitempty"`
}

// Resolved reports whether the message has stopped moving on its own — an A2A terminal state.
func (m peerMessage) Resolved() bool {
	switch m.Status {
	case "", taskSubmitted, taskAuthRequired, taskWorking:
		return false
	}
	return true
}

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

// ---- the inbound half: questions a peer is waiting on ME to answer ----

// consultsLister is the control-plane read the inbox needs (an *api.Client satisfies it).
type consultsLister interface {
	ListConsults(direction string) ([]api.Consult, error)
}

// inboundPeerMessages reads the open questions addressed to this account's machines and shapes them
// as messages. Read-only and never cached: the durable row is the truth, so a question that was
// answered from the daemon console a second ago is simply absent next time you open the modal.
//
// An error is an empty list on purpose — a control-plane hiccup must not cost you the modal (your own
// outbound answers are on disk and still render).
func inboundPeerMessages(c consultsLister) []peerMessage {
	if c == nil {
		return nil
	}
	cs, err := c.ListConsults(dirInbound)
	if err != nil {
		return nil
	}
	out := make([]peerMessage, 0, len(cs))
	for _, x := range cs {
		out = append(out, peerMessage{
			ID: x.ConsultID, Direction: dirInbound, Peer: x.Peer, Project: x.ProjectLabel,
			// auth_required, not submitted: a question in YOUR inbox is interrupted waiting on YOUR
			// decision, which is precisely the state A2A separates out.
			Question: x.Question, Status: taskAuthRequired, AskedAt: x.CreatedAt,
			Daemon: x.DaemonID, Device: x.DeviceLabel,
		})
	}
	return out
}

// mergePeerMessages is the one inbox: the local store (my asks, and the answers that landed while I
// worked) plus the live inbound questions, newest first. Deduped by consult id with the LOCAL record
// winning — if a message is in both, the local copy is the one that may carry a resolved answer.
func mergePeerMessages(local, inbound []peerMessage) []peerMessage {
	out := make([]peerMessage, 0, len(local)+len(inbound))
	seen := map[string]bool{}
	for _, m := range local {
		seen[m.ID] = true
		out = append(out, m)
	}
	for _, m := range inbound {
		if !seen[m.ID] {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AskedAt.After(out[j].AskedAt) })
	return out
}
