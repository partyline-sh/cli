package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/daemonctl"
)

// The answering side of the `ctrl-\ p` modal: a peer's question, and the two things you can do with
// it. The other half of the conversation ask_peer already had — the outbound ask (peer_menu.go) and
// the answer coming back (peer_watch.go) — now with the inbound question in the same list.
//
// WHY THIS DOESN'T ANSWER THE QUESTION ITSELF. Answering is the daemon's job, structurally:
//   - the answer route is DEVICE-token authed and scoped to target_daemon; the mux holds an account
//     token, which that route does not accept (and shouldn't — only the machine a question was
//     addressed to may answer it)
//   - the answer is one read-only engine turn against THAT machine's own checkout, resolved through
//     the daemon's local registry
// So the modal shows the question and hands a decision to the local daemon over the local control
// channel (internal/daemonctl — unix socket, 0600, no secret on the wire). The daemon re-validates
// that the id is genuinely addressed to it before acting; the modal's claim is never trusted.

// inboundAnswerTarget reports whether this machine can answer m, and if not, what to tell the human.
// Cross-machine answering is not attempted: the checkout and the device token live on the other box,
// so the only honest thing to do is name it.
func inboundAnswerTarget(m peerMessage, localDaemonID string) (bool, string) {
	where := m.Device
	if where == "" {
		where = "the machine it was addressed to"
	}
	switch {
	case m.Daemon == "":
		return false, "this question doesn't name a machine — read it in the web app"
	case localDaemonID == "":
		return false, "no daemon is enrolled on this machine — answer this on " + where
	case m.Daemon != localDaemonID:
		return false, "answer this on " + where + " — only that machine has the checkout to answer from"
	}
	return true, ""
}

// answerInboundStage shows one inbound question — who asked, which project, how long they've been
// waiting, the question itself — and then offers answer or decline. Fetch and decide are separate
// calls on purpose: you cannot approve a question this modal hasn't shown you (the daemon requires a
// digest of the text that was displayed).
func answerInboundStage(m peerMessage) {
	head := []string{
		fmt.Sprintf("%s◆%s %s asks %s", cgWire, cgOff, m.Peer, dim("· "+m.Project)),
		dim("waiting " + shortDuration(time.Since(m.AskedAt)) + " · read-only feedback on this project"),
		"",
	}

	if ok, note := inboundAnswerTarget(m, loadDaemonDevice().DaemonID); !ok {
		cgNote("Peer messages", append(append(head, questionLines(m.Question, 68, 8)...),
			"", fmt.Sprintf("  %s%s%s", cgWire, note, cgOff)))
		return
	}

	// Fetch the daemon's own copy before showing it: that is the text the answer will be produced
	// against, and approving echoes a digest of it.
	ctl := daemonctl.Local()
	q, err := ctl.GetConsult(m.ID)
	if err != nil {
		cgNote("Peer messages", append(append(head, questionLines(m.Question, 68, 8)...),
			"", fmt.Sprintf("  %s%s%s", cgBad, inboundFetchNote(err), cgOff)))
		return
	}

	// The two decisions are LIST ROWS, so 1/2 works and so do the mnemonic a/d — one keystroke either
	// way, and the number that selects a row is drawn by the same code that drew the row.
	body := append(append(head, questionLines(q.Question, 68, 12)...), "",
		dim("  answering runs ONE read-only turn on this machine's checkout — it can't write or run anything"))
	n, key, ok := cgPicker{
		Title: "Peer messages",
		Body:  body,
		Items: []string{sgr(cgKey, "a") + "  answer read-only", sgr(cgKey, "d") + "  decline"},
		Verb:  "choose",
		Extras: []cgChoice{{Key: 'a', Label: "answer read-only"},
			{Key: 'd', Label: "decline"}},
	}.run()
	if !ok {
		return
	}
	switch {
	case key == 'a' || n == 0:
		if err := ctl.ApproveConsult(m.ID, q.Question); err != nil {
			cgNote("Peer messages", []string{fmt.Sprintf("%s✗ couldn't start the answer: %v%s", cgBad, err, cgOff)})
			return
		}
		cgNote("Peer messages", []string{
			fmt.Sprintf("%s✓%s answering %s %s", cgOK, cgOff, m.Peer, dim("· "+m.Project)),
			"", dim("one read-only turn is running here; the answer posts back when it finishes."),
			dim("nothing is injected into your session — this is their answer, not yours.")})
	case key == 'd' || n == 1:
		if err := ctl.DenyConsult(m.ID, "declined by owner"); err != nil {
			cgNote("Peer messages", []string{fmt.Sprintf("%s✗ couldn't decline: %v%s", cgBad, err, cgOff)})
			return
		}
		cgNote("Peer messages", []string{
			fmt.Sprintf("%s✓%s declined %s's question %s", cgOK, cgOff, m.Peer, dim("· "+m.Project)),
			"", dim("they're freed now rather than waiting out the window.")})
	}
}

// inboundFetchNote turns a control-channel failure into something actionable. "No daemon" is the
// common one and it is not an error in the user's world — it's a machine that isn't accepting work.
func inboundFetchNote(err error) string {
	if errors.Is(err, daemonctl.ErrNoDaemon) {
		return "no daemon is running here — start `ptln daemon` to answer from this machine"
	}
	return "this machine's daemon hasn't picked up that question: " + err.Error()
}

// questionLines word-wraps a peer's question for the box, bounded in both directions. The question is
// DATA from someone else, so it is only ever displayed — never injected, never executed.
func questionLines(q string, width, maxLines int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(q) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			out = append(out, "  "+dim(line))
			if len(out) == maxLines {
				return append(out, "  "+dim("…"))
			}
			line = w
		}
	}
	if line != "" {
		out = append(out, "  "+dim(line))
	}
	if len(out) == 0 {
		out = []string{"  " + dim("(empty question)")}
	}
	return out
}
