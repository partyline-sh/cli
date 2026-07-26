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

// consultTerminal reports whether a consult status has stopped moving. Anything else (pending,
// delivered) means the peer hasn't finished with it.
func consultTerminal(status string) bool {
	switch status {
	case "answered", "declined", "timed_out", "failed":
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
		m.Status, m.Answer, m.AnsweredAt = res.Status, res.Answer, time.Now()
		if res.Detail != "" && m.Answer == "" {
			m.Answer = res.Detail
		}
		return true
	})
	if !resolved {
		m.Status = "timed_out"
		m.AnsweredAt = time.Now()
	}
	store(m)
	if mx != nil {
		mx.SetBanner(peerBanner(m))
	}
}

// peerBanner is the one-line notice in the mux status row. It names who, and the key that reads it.
func peerBanner(m peerMessage) string {
	verb := map[string]string{
		"answered":  "answered your question",
		"declined":  "declined your question",
		"timed_out": "never answered your question",
		"failed":    "couldn't answer your question",
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
