package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// ask_session — one of my agents asking ANOTHER OF MY LIVE SESSIONS a question, addressed by name,
// and getting the answer back from that actual running conversation.
//
// WHY THIS IS NOT ask_peer. ask_peer reaches a teammate's machine through the control plane, and the
// answer comes from a FRESH one-shot with no history — read-only, consent-gated, budget-capped,
// because it is someone else's box. This reaches a session in MY OWN mux, and the whole point is the
// opposite: the answer comes from the warm conversation that has been working on that project for
// hours. "ACR ODOO MCP" knows things about that codebase no cold process can reconstruct.
//
// WHY IT GOES THROUGH A FILE AND NOT A SOCKET. cg-mcp runs as a child of the ENGINE, which is a child
// of the mux — it cannot call mux methods. The seam that already solves this is the local store the
// mux polls (peer_watch.go startPeerAskAdopter adopts asks the mux never saw, stamped with
// PARTYLINE_SESSION_KEY). This mirrors it exactly rather than inventing a second mechanism.
//
// THE PRIVILEGE, STATED PLAINLY. peer_deliver.go draws a hard line between STAGING text in a prompt
// and SUBMITTING it, because a submitted prompt reaches an agent holding write and shell. This
// feature has to submit — an unsubmitted question is never answered. So the honest position is:
//
//   - Both sessions are the SAME human's, on ONE machine, launched by them. That is a materially
//     smaller trust jump than accepting a teammate's text, which is why submitting is defensible here
//     and defaults off there.
//   - It is NOT zero. If the asking agent is steered by something it read (a repo, an issue, a web
//     page), it can phrase a question that manipulates the target. The framing below is the control,
//     and it is a soft one — the target keeps whatever tools it was launched with. Unlike the
//     ask_peer answering side, there is NO read-only enforcement here. Said out loud so nobody
//     reasons from the wrong guarantee.
//   - The mitigation that actually holds is visibility: the question is typed into a real tab and the
//     answer lands in that session's transcript. Every consult is watchable and auditable after the
//     fact by scrolling. Nothing happens in a hidden channel.

// askTTL bounds a question's life. Long enough for a target that is mid-turn to finish and answer,
// short enough that an abandoned ask stops occupying the store — and it is the same bound the asking
// agent is told about, so its own timeout and this one cannot disagree.
const askTTL = 10 * time.Minute

// askDoneMarker is what the target is asked to end with, and the needle the mux scrapes for. It is
// deliberately ugly and unlikely to occur in prose: the capture is a screen scrape of a TUI, so a
// marker that could show up in a normal answer would truncate it at the wrong place.
const askDoneMarker = "<<<PTLN_ANSWER_END>>>"

// sessionAsk is one question from one of my sessions to another. It lives in the same store family as
// peerMessage but is a separate record: a peer consult has a server-side id, a consent state and a
// budget, and none of those exist here. Conflating them would mean every field on either had to be
// nullable on the other.
type sessionAsk struct {
	ID string `json:"id"`
	// From is the ASKING session's key (PARTYLINE_SESSION_KEY). It is the return address, and the
	// only one — an answer goes to the session that asked or nowhere.
	From string `json:"from"`
	// FromLabel is that session's human name, used in the framing the target sees. A question that
	// arrives unattributed reads as the human typing, which is exactly the confusion to avoid.
	FromLabel string `json:"from_label,omitempty"`
	// Target is the name the asking agent used ("ACR ODOO MCP"). Stored as WRITTEN, not resolved: the
	// resolution happens in the mux where the live children are, and keeping the raw string means a
	// miss can report what was actually asked for.
	Target   string    `json:"target"`
	Question string    `json:"question"`
	Answer   string    `json:"answer,omitempty"`
	Status   string    `json:"status"` // askOpen | askDelivered | askAnswered | askFailed
	Reason   string    `json:"reason,omitempty"`
	AskedAt  time.Time `json:"asked_at"`
	DoneAt   time.Time `json:"done_at,omitempty"`
}

const (
	askOpen      = "open"      // written by cg-mcp, not yet picked up by the mux
	askDelivered = "delivered" // typed into the target and submitted; waiting for it to finish
	askAnswered  = "answered"  // terminal, with an Answer
	askFailed    = "failed"    // terminal, with a Reason (no target, target busy, timed out)
)

func (a sessionAsk) done() bool { return a.Status == askAnswered || a.Status == askFailed }

// expired reports whether an ask has outlived its window. Checked by the mux (to fail it) and by the
// asking side (to stop waiting) so neither can hang on a question the other has given up on.
func (a sessionAsk) expired(now time.Time) bool {
	return !a.done() && now.Sub(a.AskedAt) > askTTL
}

func newAskID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ask_" + hex.EncodeToString(b[:])
}

// ---- addressing ------------------------------------------------------------------------------

// sessionCandidate is one live session as the resolver sees it: the mux child key plus the human
// name shown on its tab.
type sessionCandidate struct {
	Key   string
	Label string
}

// resolveSessionName maps what the agent typed onto exactly one live session.
//
// Matching is deliberately forgiving about case and surrounding space, and deliberately STRICT about
// ambiguity. An agent naming "ACR" with both "ACR FLEET MANAGER" and "ACR ODOO MCP" open must get an
// error listing both, never a coin flip — delivering a question to the wrong project is worse than
// not delivering it, because the answer will be confidently about the wrong codebase.
//
// Order: exact (case-insensitive) beats prefix beats substring. An exact name always wins even when
// it is also a prefix of a longer one, so "ACR ODOO" stays addressable after someone opens
// "ACR ODOO MCP STAGING".
func resolveSessionName(name string, live []sessionCandidate, self string) (key string, err string) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return "", "name a session to ask — see list_sessions for what's open"
	}
	var exact, prefix, contains []sessionCandidate
	for _, c := range live {
		if c.Key == self {
			continue // asking yourself is a deadlock dressed as a feature
		}
		got := strings.ToLower(strings.TrimSpace(c.Label))
		switch {
		case got == want:
			exact = append(exact, c)
		case strings.HasPrefix(got, want):
			prefix = append(prefix, c)
		case strings.Contains(got, want):
			contains = append(contains, c)
		}
	}
	for _, tier := range [][]sessionCandidate{exact, prefix, contains} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0].Key, ""
		default:
			return "", "\"" + name + "\" matches " + strings.Join(labelsOf(tier), ", ") + " — ask for one by its full name"
		}
	}
	if len(live) == 0 {
		return "", "no other sessions are open"
	}
	return "", "no session named \"" + name + "\" — open: " + strings.Join(labelsOf(live), ", ")
}

func labelsOf(cs []sessionCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, "\""+c.Label+"\"")
	}
	return out
}

// ---- the question, as the target sees it ------------------------------------------------------

// askPrompt frames the question for the target session.
//
// Three jobs, and the last is the load-bearing one:
//  1. say who is asking, so the target doesn't read it as its human changing subject;
//  2. ask for an answer from what it already knows, not a fresh investigation — the value here is
//     warm context, and a target that goes off and greps for ten minutes has missed the point;
//  3. tell it NOT to act. The target holds whatever tools it was launched with, so "answer, don't
//     do" is the only brake between a question and an edit. It is a soft brake — a prompt, not an
//     enforcement — which is precisely why it is stated this bluntly.
//
// The marker is last so a truncated capture is detectably truncated rather than silently short.
func askPrompt(fromLabel, question string) string {
	who := strings.TrimSpace(fromLabel)
	if who == "" {
		who = "another session"
	}
	return "[partyline] " + who + " (another of your sessions on this machine) is asking you a question.\n\n" +
		strings.TrimSpace(question) + "\n\n" +
		"Answer from what you already know in THIS conversation — your context is the reason you were asked. " +
		"Don't start an investigation, don't edit files, don't run commands: this is a question, not a task. " +
		"If you don't know, say so plainly.\n" +
		"End your reply with " + askDoneMarker + " on its own line."
}

// askMarkerCue is the phrase that immediately precedes the marker IN OUR OWN PROMPT. It exists so
// the scraper can tell our instruction apart from the target obeying it.
const askMarkerCue = "End your reply with "

// extractAnswer pulls the reply out of the target's scrollback.
//
// THE TRAP, WHICH THIS FAILED FIRST TIME. Our own prompt necessarily CONTAINS the marker — it has to,
// that is how the target learns what to end with. So the naive "find the last marker" finds the one
// in the echoed question and hands back the prompt as though the model had said it: a confident
// answer that is really our own words. A test now pins this (TestEchoedPromptIsNotAnAnswer).
//
// So a marker only terminates an answer if it is NOT the one in our instruction — i.e. not directly
// preceded by askMarkerCue. Everything between the end of the framing and that marker is the reply.
//
// Conservative in one direction on purpose: returning LESS than the model said is recoverable (the
// human can scroll the tab), returning our own question is not.
func extractAnswer(screen string) (answer string, ok bool) {
	// Walk markers from the end, skipping any that is the one we told the target to use.
	end := -1
	for i := strings.LastIndex(screen, askDoneMarker); i >= 0; i = strings.LastIndex(screen[:i], askDoneMarker) {
		if i >= len(askMarkerCue) && strings.HasSuffix(screen[:i], askMarkerCue) {
			continue // this is our instruction, not the target's sign-off
		}
		end = i
		break
	}
	if end < 0 {
		return "", false
	}
	body := screen[:end]
	// Cut everything up to and including our instruction line, so the framing can't leak into the
	// answer even when the target echoed the whole question above its reply.
	if j := strings.LastIndex(body, askMarkerCue); j >= 0 {
		if nl := strings.IndexByte(body[j:], '\n'); nl >= 0 {
			body = body[j+nl+1:]
		}
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	return body, true
}
