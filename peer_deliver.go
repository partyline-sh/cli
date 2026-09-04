package main

import (
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// peer_deliver.go — putting a peer's answer back into the agent that asked for it, without a human
// having to go and find it.
//
// WHAT THE GAP WAS. The mechanism has been here all along: peer_inbox.go pastes an answer into a
// session as a bracketed-paste block, deliberately WITHOUT a trailing newline, so the human presses
// Enter and nothing reaches the agent that a person didn't send. But the watcher (peer_watch.go) only
// ever set a banner — it injected nothing — so collecting an answer meant opening `ctrl-\ p` and
// picking the row. Two human actions where the flow is supposed to have none.
//
// SO THERE ARE TWO PRIVILEGES HERE, NOT ONE, AND THEY ARE MILES APART:
//
//	STAGE   put the labelled block in the asking session's prompt, unsubmitted. It cannot become a
//	        turn without a human keystroke, so the blast radius is "text appeared in your prompt".
//	        Strictly better than a banner: the answer is already there when you look.
//	SUBMIT  press Enter for them. Now a teammate's text is a PROMPT in an agent that, unlike the
//	        answering side, has no read-only enforcement whatsoever — it has whatever tools that
//	        session was launched with, which for a normal coding session is write and shell.
//
// The read-only guarantee everyone reasons about (consult_answer.go, P0.0) protects the ANSWERING
// machine. It gives the ASKING machine nothing. An answer is untrusted text from another person's
// agent, and submitting it unattended is handing that text to a tool-bearing agent with nobody
// reading it first. So SUBMIT is a separate per-project setting from consult auto-answer — the same
// split, for the same reason, as launchPolicy vs consultPolicy — and it DEFAULTS OFF.
//
// DEFAULTING IT OFF OVERRIDES THE "no humans involved" REQUIREMENT ON THIS ONE AXIS, deliberately.
// Auto-ANSWER can default on because read-only is enforced by the engine, the question is bounded,
// and the spend is capped. None of that is true here: there is no bound on what a submitted prompt
// can cause, and the only thing standing between a compromised teammate account and an unattended
// write agent would be this default. Staging keeps the flow one keystroke from zero-touch and keeps
// that keystroke a human's. If the owner wants it on, it is a one-line default change in
// consultDeliverPolicy.

// deliverMode is what to do with an answer that has landed.
type deliverMode int

const (
	deliverBanner deliverMode = iota // the pre-existing behaviour: tell them, inject nothing
	deliverStage                     // paste the labelled block, unsubmitted (the default)
	deliverSubmit                    // paste and press Enter (opt-in, and only when it's safe to)
)

// consultDeliverPolicy resolves the ASKING side's delivery policy for a session working in dir.
//
// Keyed on the local project the asking session is IN — not the peer's project label, which names a
// checkout on someone else's machine and says nothing about what may be typed into my agent. A
// session whose dir isn't a registered project has no setting to read, and the answer for "may
// untrusted text be submitted here" when nobody has said yes is no.
func consultDeliverPolicy(dir string) deliverMode {
	if dir == "" {
		return deliverStage
	}
	for _, p := range loadDaemonRegistry().Projects {
		if p.Path == dir && p.Deliver == "submit" {
			return deliverSubmit
		}
	}
	return deliverStage
}

// peerDeliverTarget is the mux surface the deliverer needs. An interface so the decision logic can be
// driven by a fake in a test — there is no way to spin up a real PTY session in one.
type peerDeliverTarget interface {
	SessionByKey(key string) (sess sessIO, label, dir string, ok bool)
	UnsubmittedInput(key string) (int, bool)
	SessionStatus(key string) string
	SetBanner(string)
}

// deliverToAskingSession is the whole delivery decision for one landed answer. It returns the mode it
// actually used and the banner it set, so a test can assert both without a terminal.
//
// The write itself is done by `paste`, injected for the same reason. It REPORTS whether it actually
// pressed Enter: submitting is a two-step write with a gap in the middle (see pasteBlock), and the
// safety re-check inside that gap can veto the Enter after we've already decided to send it. The
// banner has to say what happened, not what we intended.
func deliverToAskingSession(mx peerDeliverTarget, m peerMessage, paste func(sess sessIO, block string, submit bool, confirm func() string) bool) (deliverMode, string) {
	// Only a completed answer is ever injected. A decline, a timeout or a failure is a banner: there
	// is no peer text to hand over, and pasting "no answer" into a prompt is noise.
	if m.Status != taskCompleted || strings.TrimSpace(m.Answer) == "" {
		b := peerBanner(m)
		mx.SetBanner(b)
		return deliverBanner, b
	}
	// ONE ADDRESS, NEVER A GUESS. No recorded session means the ask came from outside a mux (a bare
	// MCP client, `ptln daemon`), and the answer is collected with check_consult instead. Falling back
	// to "the focused session" here would deliver a teammate's text into whichever agent the human
	// happened to be looking at, which is a different agent's context and possibly a different repo.
	sess, label, dir, live := mx.SessionByKey(m.Session)
	if !live || sess == nil {
		// The asking session has exited (or belongs to another mux). The answer is already in the
		// store, so say where it is rather than dropping it.
		b := fmt.Sprintf("☎ %s answered — the session that asked is gone; ctrl-\\ p to read it", m.Peer)
		mx.SetBanner(b)
		return deliverBanner, b
	}

	block := peerAnswerBlock(m.Peer, m.Project, m.Answer)
	mode := consultDeliverPolicy(dir)
	if mode == deliverSubmit {
		if why := unsafeToSubmit(mx, m.Session); why != "" {
			// Opted in, but not right now. Degrade to staging rather than risk it — a stage that
			// needed a keystroke is a minor disappointment; a submit that concatenated with a
			// half-typed prompt is a corrupted instruction to a tool-bearing agent.
			mode = deliverStage
			paste(sess, block, false, nil)
			b := fmt.Sprintf("☎ %s answered — staged in %s (%s); press ⏎ to send it", m.Peer, label, why)
			mx.SetBanner(b)
			return mode, b
		}
		// The framing is IDENTICAL to the staged path — the same block, byte for byte. The labelled
		// fence is what makes the agent treat the answer as data rather than instruction, so the path
		// that needs it MOST is the one where no human reads it first. Only the submit flag differs.
		//
		// confirm re-runs the safety check INSIDE pasteBlock's gap. unsafeToSubmit above was true a
		// moment ago; the Enter goes out ~a second later, and a human who started typing in between
		// would have their half-written line submitted along with the peer's text. Checking once, up
		// front, would have moved that hazard rather than removed it.
		if paste(sess, block, true, func() string { return unsafeToSubmit(mx, m.Session) }) {
			b := fmt.Sprintf("☎ %s answered — delivered to %s", m.Peer, label)
			mx.SetBanner(b)
			return deliverSubmit, b
		}
		b := fmt.Sprintf("☎ %s answered — staged in %s (you typed while it landed); press ⏎ to send it", m.Peer, label)
		mx.SetBanner(b)
		return deliverStage, b
	}
	paste(sess, block, false, nil)
	b := fmt.Sprintf("☎ %s answered — staged in %s; press ⏎ to send it", m.Peer, label)
	mx.SetBanner(b)
	return deliverStage, b
}

// unsafeToSubmit returns a short human reason NOT to press Enter, or "" if there's no reason we can
// see. Read the sign carefully: this is not a proof of safety, it is a list of the dangers we can
// actually observe. See the report and child.unsubmitted — an engine can hold text in its input box
// with no stdin traffic at all, so "" means "no evidence of a problem", never "provably empty".
func unsafeToSubmit(mx peerDeliverTarget, key string) string {
	if n, known := mx.UnsubmittedInput(key); !known {
		return "can't tell if you're typing"
	} else if n > 0 {
		// The decisive one. Pasting here would concatenate with the human's half-written prompt and
		// then SEND the pair — their partial instruction plus a teammate's untrusted text, as one turn.
		return "you have something typed"
	}
	if st := mx.SessionStatus(key); st != "waiting" {
		// "active" means mid-turn; "" means we couldn't tell (a non-claude/codex engine, or no store
		// yet). Both refuse: an unknown state is not an idle one.
		return "the agent is still working"
	}
	return ""
}

// pasteBlock is the real injection: a bracketed-paste block, so the child treats it as ONE pasted
// unit rather than a stream of keystrokes to interpret. Identical to what the modal has always done
// (peer_inbox.go) — deliberately the same bytes, so there is one way an answer enters a session.
//
// THE SUBMIT CR GOES OUTSIDE THE WRAPPER, and that is not a detail. Inside \x1b[200~…\x1b[201~ a
// newline is CONTENT — that is the entire reason this mechanism is safe, and why peerAnswerBlock's own
// trailing newline has never submitted anything. A CR appended inside the fence would be a literal
// newline in the prompt and the "auto-submit" would silently do nothing; only a CR after the closing
// \x1b[201~ is a keypress. Getting this backwards fails silently in the safe direction, which is
// exactly the kind of bug that ships.
// Putting the submit CR outside the closing \x1b[201~ is NECESSARY BUT NOT SUFFICIENT. claude's TUI
// reads stdin in chunks, and a CR arriving in the SAME read() as the fence terminator is absorbed into
// the pasted text instead of acting as Enter. Measured against real engines in real PTYs
// (peer_deliver_realpty_manual_test.go): appended to the paste write, claude submitted 2 of 5 runs.
//
// A fixed sleep between the two writes is NOT enough either — 750ms measured 5/5 once and 4/5 on the
// next run. Same delay, different outcome: a sleep makes coalescing unlikely, not impossible, and an
// intermittent miss is worse than a consistent one (the original CR-inside-the-fence bug never
// submitted at all, so it was obvious; a 4-in-5 delivery rate looks fine every time you spot-check it).
//
// So wait for EVIDENCE instead of guessing: poll the session's own screen until the child has visibly
// consumed the paste, then send the CR. codex and opencode submit either way; this is for claude.
const (
	pasteLandTimeout = 3 * time.Second       // give up waiting and send anyway rather than never submitting
	pasteLandPoll    = 40 * time.Millisecond // screen snapshots are cheap; the child redraws far slower
	pasteLandGrace   = 120 * time.Millisecond
)

// waitForPasteToLand blocks until the child's screen shows the pasted block, or the timeout expires.
// The needle is derived from the block itself rather than hardcoded, so it can't drift out of sync with
// peerAnswerBlock's wording. "[Pasted" is the second needle: opencode collapses a multi-line paste to a
// `[Pasted ~N lines]` placeholder, so its own text never appears on screen.
//
// On timeout it returns anyway. Not submitting at all is the worse failure: the answer would sit staged
// while the banner claimed it was delivered, and the caller can't tell the difference from here.
func waitForPasteToLand(sess sessIO, block string) {
	needle := strings.TrimSpace(block)
	if i := strings.IndexByte(needle, '\n'); i > 0 {
		needle = needle[:i]
	}
	if len(needle) > 24 {
		needle = needle[:24]
	}
	deadline := time.Now().Add(pasteLandTimeout)
	for time.Now().Before(deadline) {
		scr := string(sess.Snapshot())
		if (needle != "" && strings.Contains(scr, needle)) || strings.Contains(scr, "[Pasted") {
			time.Sleep(pasteLandGrace) // let the child finish drawing before the keypress
			return
		}
		time.Sleep(pasteLandPoll)
	}
}

// pasteBlock reports whether it actually pressed Enter. confirm is re-consulted after the paste has
// landed and may veto the Enter (a human who started typing while it landed) — a veto leaves the answer
// staged, the same safe state as deliverStage, and the caller must describe it that way.
func pasteBlock(sess sessIO, block string, submit bool, confirm func() string) bool {
	sess.WriteInput([]byte("\x1b[200~" + block + "\x1b[201~"))
	if !submit {
		return false
	}
	waitForPasteToLand(sess, block)
	if confirm != nil {
		if why := confirm(); why != "" {
			return false // typed while it landed — staged beats submitting their half-line with it
		}
	}
	sess.WriteInput([]byte("\r"))
	return true
}

// closePeerAskLocally files a consult's terminal result in the local store and marks it DELIVERED,
// for the case where the asking agent already has the answer in a tool result (ask_peer's fast path,
// or check_consult). Marking it delivered is what stops a mux watcher from also pasting it — the
// agent holding it twice reads as the peer having answered twice.
func closePeerAskLocally(id string, res *api.ConsultResult) {
	for _, m := range openPeerMessages() {
		if m.ID != id {
			continue
		}
		m.Status, m.Answer, m.AnsweredAt, m.Delivered = a2aTaskState(res.Status), res.Answer, time.Now(), true
		if strings.TrimSpace(m.Answer) == "" {
			m.Answer = strings.TrimSpace(peerStateLabel(m.Status) + detailSuffix(res.Detail))
		}
		putPeerMessage(m)
		return
	}
}
