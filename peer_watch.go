package main

import (
	"context"
	"fmt"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The background half of peer messaging. Cancelling a wait must not lose the answer: the consult
// lives server-side and the peer is often a human who still has to approve it, so "esc, get told
// later" is the NORMAL path, not a failure. This watcher is what makes that true — it outlives the
// modal, polls slowly, files the result in the local store, and banners the mux.
//
// Same structure as the shared-context checkup (llms_mux.go startThreadCheckup): a goroutine that
// polls and calls SetBanner. Informational only — nothing here is ever injected into the agent.

const (
	// The server closes a consult after ~10 minutes (see api.AskPeer / the daemon's answer path),
	// so that is the watcher's deadline. Bounded by construction: no unbounded loop.
	peerWatchWindow = 10 * time.Minute
	// Low rate: nobody is looking at this, and the answering side takes minutes.
	peerWatchPoll = 5 * time.Second
)

type consultPoller interface {
	GetConsult(id string) (*api.ConsultResult, error)
}

type bannerSink interface {
	SetBanner(string)
}

// deliverSink is what a resolved message is handed to. peerDeliverTarget (peer_deliver.go) is the real
// one; watchPeerMessage takes the narrower bannerSink so the existing tests keep working, and delivery
// happens only when the sink can actually reach a session.
type deliverSink interface {
	peerDeliverTarget
}

// consultTerminal reports whether a CONTROL-PLANE consult status has stopped moving. Wire vocabulary
// on purpose — this is what the poll returns; a2aTaskState translates it before we store it. Anything
// else (pending, delivered) means the peer hasn't finished with it.
func consultTerminal(status string) bool {
	switch status {
	// `canceled` is here because a withdrawal has to STOP the watcher. Without it a cancelled ask polls
	// on to the 10-minute deadline and then files itself as `failed` — the inbox would say "no answer"
	// about a question the user took back themselves, and the goroutine would outlive the thing it watches
	// by minutes for nothing.
	case "answered", "declined", "timed_out", "failed", "canceled":
		return true
	}
	return false
}

// startPeerWatch hands a still-open message to a background watcher. Returns immediately.
func startPeerWatch(mx bannerSink, c consultPoller, m peerMessage) {
	ctx, cancel := context.WithDeadline(context.Background(), m.AskedAt.Add(peerWatchWindow))
	go func() {
		defer cancel()
		watchPeerMessage(ctx, c, m, peerWatchPoll, putPeerMessage, mx)
	}()
}

// watchPeerMessage polls one consult to its terminal state, files it via `store`, and banners.
// If the deadline passes first it files a timed_out message anyway, so the inbox never shows an ask
// that is silently dead. Split out from startPeerWatch so a test can drive it with a fake poller,
// a fake store and a already-expiring context.
func watchPeerMessage(ctx context.Context, c consultPoller, m peerMessage, poll time.Duration, store func(peerMessage), mx bannerSink) {
	resolved := pollUntil(ctx, poll, func() bool {
		res, err := c.GetConsult(m.ID)
		if err != nil || !consultTerminal(res.Status) {
			return false // a poll error is transient; the deadline is what ends this
		}
		m.Status, m.Answer, m.AnsweredAt = a2aTaskState(res.Status), res.Answer, time.Now()
		if res.Detail != "" && m.Answer == "" {
			m.Answer = res.Detail
		}
		return true
	})
	if !resolved {
		m.Status = taskFailed // A2A folds a timeout into failed: terminal, not the peer's choice
		m.AnsweredAt = time.Now()
	}
	if mx == nil {
		store(m)
		return
	}
	// DELIVERY, not just a banner. If the sink can reach sessions, hand the answer to the one that
	// asked (peer_deliver.go decides stage vs submit and sets the banner). Otherwise keep the old
	// informational behaviour. Delivered is recorded BEFORE the store write so a rescan can't stage
	// the same block twice.
	if d, ok := mx.(deliverSink); ok && !m.Delivered {
		mode, _ := deliverToAskingSession(d, m, pasteBlock)
		if mode != deliverBanner {
			m.Delivered = true
		}
		store(m)
		return
	}
	store(m)
	mx.SetBanner(peerBanner(m))
}

// peerBanner is the one-line notice in the mux status row. It names who, and the key that reads it.
func peerBanner(m peerMessage) string {
	verb := map[string]string{
		taskCompleted: "answered your question",
		taskRejected:  "declined your question",
		taskFailed:    "never answered your question",
		taskCanceled:  "cancelled your question",
	}[m.Status]
	if verb == "" {
		verb = "replied"
	}
	return fmt.Sprintf("☎ %s %s — ctrl-\\ p to read", m.Peer, verb)
}

// pollUntil calls fn every interval until it returns true or ctx is done, and polls once up front.
// Returns whether fn ever said true. The context deadline is the ONLY other exit — that is what
// keeps a watcher from outliving the thing it watches.
func pollUntil(ctx context.Context, interval time.Duration, fn func() bool) bool {
	if fn() {
		return true
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if fn() {
				return true
			}
		}
	}
}

// ---- adopting asks the MUX didn't make -------------------------------------

const peerAdoptPoll = 20 * time.Second

// startPeerAskAdopter is what makes the AGENT-initiated flow work. An ask made through the ask_peer
// MCP tool happens in the cg-mcp process — a child of the engine, not of the mux — so the mux has
// never known such an ask existed and had nothing watching for its answer. cg_mcp.go now records
// every ask in the local store stamped with PARTYLINE_SESSION_KEY, and this loop adopts any that are
// still open into the ordinary watcher. So "user 1's LLM asks, user 2 approves six minutes later, the
// answer arrives in user 1's session" has something running the whole time.
//
// Idempotent by id: a message is adopted once per mux lifetime, and the watcher itself is bounded by
// the consult window, so this can never accumulate goroutines.
func startPeerAskAdopter(mx deliverSink, c consultPoller) {
	if c == nil {
		return
	}
	adopted := map[string]bool{}
	go func() {
		for {
			for _, m := range openPeerMessages() {
				// Outbound only (an inbound question is not mine to collect), still open, and with a
				// session to deliver into — an ask from outside any mux has no address here and is
				// collected with check_consult instead.
				if m.Direction != dirOutbound || m.Resolved() || m.Delivered || m.Session == "" || adopted[m.ID] {
					continue
				}
				adopted[m.ID] = true
				startPeerWatch(mx, c, m)
			}
			time.Sleep(peerAdoptPoll)
		}
	}()
}
