package main

// THE SCOPE GATE: what is allowed to become a fact.
//
// The previous store's facts were specific and well written and still not trusted, because the
// thread had no idea what belonged in it. Filed next to real interface contracts, in the same
// project's memory, were: "the Mac mini's 228 GB disk keeps filling up", "is port 5900 reachable
// from the internet over IPv6", "both the laptop and the Mac mini have Screen Sharing on", and
// "receipt #81 is staged with the wrong coding and must not be pushed". Home network state, host
// ops, and a work item. None of them wrong; none of them about the repo; none of them true for
// long.
//
// The test every fact has to pass:
//
//	would this still be true and useful to someone who opens this repo in three months
//	and was not here?
//
// That cannot be checked mechanically, so this does not pretend to. It catches the shapes that
// reliably fail it and says why, in the words of the thing it found, and otherwise gets out of the
// way. A gate that blocks good facts is worse than no gate, so every rule here is aimed at a
// pattern observed in the real data rather than at a category someone imagined.

import (
	"fmt"
	"regexp"
	"strings"
)

// gateVerdict is the result of screening one candidate fact.
type gateVerdict struct {
	Refuse bool
	Reason string // why, phrased so an agent can fix the fact rather than retry it verbatim
	Hint   string // what to do with it instead
}

// scopeRule is one observed failure shape.
type scopeRule struct {
	name   string
	match  *regexp.Regexp
	reason string
	hint   string
}

// transientShapes are the patterns that failed the three-month test in the real store.
//
// Each carries the example that put it here. They are deliberately narrow: "disk" alone would
// refuse "the ISAM files grow unbounded and fill the store server's disk", which is a real and
// durable constraint, so the rule needs the transient framing too ("keeps filling up", "needs N GB
// free").
var transientShapes = []scopeRule{
	{
		name:   "host-state",
		match:  regexp.MustCompile(`(?i)\b(disk|storage|free space)\b[^.]{0,60}\b(keeps? (filling|running out)|is (nearly )?full|needs? \d+\s*(gb|mb|tb)\b)`),
		reason: "this records how full a machine is right now, which stops being true as soon as something is deleted",
		hint:   "if the durable fact is that something grows without bound, say that instead",
	},
	{
		name:   "machine-config",
		match:  regexp.MustCompile(`(?i)\b(the (mac ?mini|laptop|desktop)|my (mac|machine|laptop))\b`),
		reason: "this is about a particular machine, not about this repo",
		hint:   "machine setup belongs in that machine's own notes, not in the repo's memory",
	},
	{
		name:   "work-item-status",
		match:  regexp.MustCompile(`(?i)\b(pr #?\d+|ticket \d+|helpdesk \d+|receipt #?\d+)\b[^.]{0,80}\b(is|was|has been|sits|staged|open|merged|on staging|in review|pending)\b`),
		reason: "this is the current state of a work item, which the tracker already owns and which changes without anyone updating this",
		hint:   "record what the change established, not where it is in the queue",
	},
	{
		name:   "in-flight",
		match:  regexp.MustCompile(`(?i)^\s*(currently|right now|at the moment|for now|today)\b|\b(is currently|are currently) (being|broken|failing|down|blocked)\b`),
		reason: "this describes a moment rather than a property of the code",
		hint:   "if it is a decision that holds, drop the time words and state the rule",
	},
	{
		name:   "session-chatter",
		match:  regexp.MustCompile(`(?i)\b(i (just|will|am going to)|let'?s|we should probably|next step is to|todo\b|fixme\b)`),
		reason: "this is a note to yourself inside one session, not something a later reader needs",
		hint:   "if it is work to do, file it as work; if it is a thing that is true, state the thing",
	},
}

// minFactBody catches entries with no content at all — "x", "ok", "we use gRPC". It is deliberately
// LOW, because length is a crude proxy for substance and the cost of a false refusal is high. The
// first draft used 40 and refused "gRPC, not REST — the links are flaky", which is a real decision
// with its rationale in 36 characters. A gate that blocks good facts is worse than no gate, so this
// only rejects what could not be a fact at any length.
const minFactBody = 25

// screenFact applies the gate. kind is the declared kind; body is the fact.
func screenFact(kind, body string) gateVerdict {
	b := strings.TrimSpace(body)
	if b == "" {
		return gateVerdict{Refuse: true, Reason: "a fact needs a body", Hint: "one sentence stating the thing that is true"}
	}
	if len([]rune(b)) < minFactBody {
		return gateVerdict{
			Refuse: true,
			Reason: fmt.Sprintf("too short to carry its own context (%d characters)", len([]rune(b))),
			Hint:   "say where it applies and what it rules out, so it still reads in three months",
		}
	}
	// A question is exempt from the transient shapes: "is port 5900 reachable" is a bad FACT but
	// an open question about unresolved work is exactly what `question` is for, and refusing those
	// pushes agents into inventing answers — the failure this whole system exists to prevent.
	if kind == "question" {
		return gateVerdict{}
	}
	for _, r := range transientShapes {
		if r.match.MatchString(b) {
			return gateVerdict{Refuse: true, Reason: r.reason, Hint: r.hint}
		}
	}
	return gateVerdict{}
}

// gateError renders a refusal for a CLI or tool caller. It quotes nothing back: the caller has the
// body, and repeating it makes the actual reason harder to find.
func (v gateVerdict) gateError() error {
	if !v.Refuse {
		return nil
	}
	msg := "not a durable fact: " + v.Reason
	if v.Hint != "" {
		msg += "\n  " + v.Hint
	}
	msg += "\n  the test: would this still be true and useful to someone who opens this repo in three months?"
	return fmt.Errorf("%s", msg)
}
