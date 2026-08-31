package main

import (
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptymux"
)

// WITHDRAWING AN ASK — the asker's side of cancel.
//
// WHY THIS IS NOT `ptln peer decline`. decline is the ANSWERING machine saying "I won't answer this",
// and it goes over the local unix socket to that machine's daemon. cancel is the ASKING account saying
// "never mind", and it goes to the control plane with the account token — a different actor, a different
// transport, a different authorization. The server scopes it by from_user exactly as it scopes reading
// the handle, so you can only ever withdraw a question you asked; a target machine cannot cancel a
// question addressed to it, and a foreign id is indistinguishable from one that never existed.
//
// WHY IT IS NOT MERELY COSMETIC. Before this, esc on a wait handed the ask to a background watcher —
// which is the right default (the answer is worth having later) but is NOT a withdrawal. Nothing
// withdrew a consult, so a question asked by mistake stayed answerable for the full 10-minute window and
// the peer's machine could still spend a read-only engine turn, on their tokens, answering it.

// cancelPeerAsk withdraws one outbound ask and files the result locally. Returns a line to show the
// human and whether the consult is now terminal. ONE implementation, so `ptln peer cancel` and the
// ctrl-\ p inbox cannot disagree about what happened or about what the local store then says.
func cancelPeerAsk(c *api.Client, m peerMessage) (string, bool) {
	res, err := c.CancelConsult(m.ID)
	if err != nil {
		// The server's one refusal for "not yours / never existed", passed through verbatim. Guessing which
		// it was is exactly the disclosure the endpoint refuses to make.
		return fmt.Sprintf("couldn't cancel %s: %v", m.ID, err), false
	}
	m.Status, m.AnsweredAt = a2aTaskState(res.Status), time.Now()
	if !res.Cancelled {
		// Already terminal. Not an error: the asker asked to take back something that had already come
		// back, and the useful reply is what happened instead. The answer, if there is one, is still on
		// its way through the ordinary path — this changed nothing.
		putPeerMessage(m)
		return fmt.Sprintf("%s was already %s — nothing to withdraw", m.ID, peerStateLabel(m.Status)), true
	}
	if m.Answer == "" {
		m.Answer = "withdrawn by you"
	}
	putPeerMessage(m)
	return fmt.Sprintf("withdrawn — %s won't be answered", m.ID), true
}

// outboundOpenStage is what opening a STILL-OPEN ask of yours does in the ctrl-\ p inbox: wait for it
// again, or withdraw it. It is a picker rather than an extra letter key on the inbox list because an
// extra key there returns no row (cgPicker's contract) — "cancel" has to name WHICH ask, and the only
// honest way to do that is to open the one you picked.
func outboundOpenStage(mx *ptymux.Mux, c *api.Client, m peerMessage) {
	i, _, ok := cgPicker{
		Title: "Ask " + m.Peer,
		Body: append([]string{dim("about " + m.Project + " · " + peerStateLabel(m.Status)), ""},
			questionLines(m.Question, 68, 8)...),
		Items: []string{"wait for it again", "withdraw this question"},
		Verb:  "choose",
	}.run()
	if !ok {
		return // esc: unchanged, still out, the watcher still has it
	}
	if i == 0 {
		awaitConsult(mx, c, m)
		return
	}
	line, _ := cancelPeerAsk(c, m)
	cgNote("Peer messages", []string{
		fmt.Sprintf("%s%s%s %s", cgOK, m.Peer, cgOff, dim("· "+m.Project)),
		"", "  " + dim(line)})
}

// peerCancel is `ptln peer cancel <id>`.
func peerCancel(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln peer cancel <consult-id>"))
	}
	if api.LoadToken() == "" {
		fatal(fmt.Errorf("you're not signed in — run `ptln login` first (cancel goes to the control plane with your account token, not to a daemon)"))
	}
	id := args[0]
	// Whatever the local store knows about the ask, so the line can name the peer. Absent is fine: the id
	// is the handle, and the server is the authority on whether it is yours.
	m := peerMessage{ID: id, Direction: dirOutbound, AskedAt: time.Now()}
	for _, x := range openPeerMessages() {
		if x.ID == id {
			m = x
			break
		}
	}
	line, ok := cancelPeerAsk(api.New(), m)
	if !ok {
		fatal(fmt.Errorf("%s", line))
	}
	who := ""
	if m.Peer != "" {
		who = " (" + strings.TrimSpace(m.Peer+" · "+m.Project) + ")"
	}
	fmt.Printf("✓ %s%s\n", line, who)
}
