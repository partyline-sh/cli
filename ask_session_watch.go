package main

import (
	"strings"
	"time"

	"partyline.sh/partyline/internal/ptymux"
	"partyline.sh/partyline/internal/ptysess"
)

// The mux half of ask_session: pick up asks written by cg-mcp, put them into the named session, and
// capture what it says back.
//
// Runs in the mux process because that is the only place the children live. Structured like
// startPeerAskAdopter (peer_watch.go): a slow poll over a local store, one goroutine per accepted
// ask, bounded by the ask's own TTL so goroutines cannot accumulate.

const (
	askPoll        = 2 * time.Second        // store poll — an ask is typed by a human-speed agent, not a firehose
	askAnswerPoll  = 750 * time.Millisecond // how often to re-read the target's screen while it works
	askBusyRetry   = 5 * time.Second        // how long to keep re-checking a busy target before giving up on it
	askScrapeLines = 400                    // scrollback lines to scan — an answer longer than this is not an answer
)

// askTarget is the mux surface this needs. An interface so the logic can be driven by a fake in a
// test: there is no way to spin up real PTY children in one, and the parts worth testing (choose a
// target, refuse an unsafe one, decide when an answer is complete) don't need them.
type askTarget interface {
	LiveSessions() []ptymux.LiveSession
	SessionByKey(key string) (sess *ptysess.Session, label, dir string, ok bool)
	UnsubmittedInput(key string) (int, bool)
	SessionStatus(key string) string
	SetBanner(string)
}

// startSessionAskWatch runs the pickup loop for the lifetime of the mux.
func startSessionAskWatch(mx askTarget) {
	if mx == nil {
		return
	}
	taken := map[string]bool{}
	go func() {
		for {
			// Publish the roster on the same beat as the pickup: list_sessions runs in cg-mcp, which
			// cannot see the children, so this file is the only way an agent learns what it can address.
			live := make([]sessionCandidate, 0, 8)
			for _, s := range mx.LiveSessions() {
				live = append(live, sessionCandidate{Key: s.Key, Label: s.Label})
			}
			publishSessions(live)

			for _, a := range openAsks() {
				if a.Status != askOpen || taken[a.ID] {
					continue
				}
				taken[a.ID] = true
				go runSessionAsk(mx, a)
			}
			time.Sleep(askPoll)
		}
	}()
}

// runSessionAsk carries one ask through: resolve the name, check it is safe to type into, inject,
// then watch for the answer. Every exit writes a terminal status — an ask that just stops would
// leave the asking agent polling until its own timeout with no reason why.
func runSessionAsk(mx askTarget, a sessionAsk) {
	fail := func(reason string) {
		a.Status, a.Reason, a.DoneAt = askFailed, reason, time.Now()
		putAsk(a)
	}

	live := make([]sessionCandidate, 0, 8)
	for _, s := range mx.LiveSessions() {
		live = append(live, sessionCandidate{Key: s.Key, Label: s.Label})
	}
	key, rerr := resolveSessionName(a.Target, live, a.From)
	if rerr != "" {
		fail(rerr)
		return
	}
	sess, label, _, ok := mx.SessionByKey(key)
	if !ok || sess == nil {
		fail("that session just closed")
		return
	}

	// Wait briefly for a target that is mid-turn or has a half-typed prompt in it. Briefly, not
	// forever: an agent waiting on an answer should be told "it's busy" in seconds rather than
	// discover it after its own timeout.
	deadline := time.Now().Add(askBusyRetry)
	for {
		if why := unsafeToAsk(mx, key); why == "" {
			break
		} else if time.Now().After(deadline) {
			fail(label + " " + why)
			return
		}
		time.Sleep(askAnswerPoll)
	}

	// The screen BEFORE we type. The marker check alone is not enough to know the answer is new —
	// a previous consult in the same session left its own marker in the scrollback, and scraping
	// would return that stale answer instantly. So capture the prior length and only consider text
	// that appears after it.
	before := len(sess.SnapshotHistory(askScrapeLines, 0))

	block := askPrompt(a.FromLabel, a.Question)
	// submit: true — unlike a peer answer (which is STAGED for a human to send, peer_deliver.go), a
	// question nobody sends is never answered. The trust difference that licenses this: both sessions
	// are the same human's, on this machine. The framing in askPrompt is the only brake on what the
	// target then does, and it is a soft one.
	pasteBlock(sess, block, true, func() string { return unsafeToAsk(mx, key) })

	a.Status = askDelivered
	putAsk(a)
	mx.SetBanner("↔ " + a.FromLabel + " asked " + label + " a question")

	if answer, ok := awaitAnswer(sess, before, a.AskedAt.Add(askTTL)); ok {
		a.Status, a.Answer, a.DoneAt = askAnswered, answer, time.Now()
		putAsk(a)
		mx.SetBanner("↔ " + label + " answered " + a.FromLabel)
		return
	}
	fail("timed out — " + label + " didn't finish answering within " + askTTL.String())
}

// unsafeToAsk mirrors unsafeToSubmit (peer_deliver.go) and exists for the same reason: typing into a
// child whose state you cannot see is how you answer someone's "Allow Bash? (y/n)" with the first
// character of a question.
//
// It reads the same two signals, and like that one it reports the dangers we can OBSERVE — never
// proof of safety. See child.unsubmitted: an engine can hold text in its box with no stdin traffic,
// so "no evidence" is the strongest claim available.
func unsafeToAsk(mx askTarget, key string) string {
	if n, known := mx.UnsubmittedInput(key); !known {
		return "isn't reachable from here"
	} else if n > 0 {
		return "has something half-typed in it — a question would be sent joined to it"
	}
	if st := mx.SessionStatus(key); st != "waiting" {
		return "is still working"
	}
	return ""
}

// awaitAnswer polls the target's scrollback until the reply is complete.
//
// Only text that appeared AFTER we typed is considered (see `before`), because a session that has
// answered a consult before still has that marker in its history — without the offset the very first
// poll would return the previous answer, which is both wrong and instant, the worst combination.
func awaitAnswer(sess *ptysess.Session, before int, deadline time.Time) (string, bool) {
	for time.Now().Before(deadline) {
		scr := string(sess.SnapshotHistory(askScrapeLines, 0))
		if len(scr) > before {
			if ans, ok := extractAnswer(scr[before:]); ok {
				return strings.TrimSpace(ans), true
			}
		}
		time.Sleep(askAnswerPoll)
	}
	return "", false
}
