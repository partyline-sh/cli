package main

import (
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
)

// peerMenu is the `ctrl-\ p` action (ask_peer P0.d): a MESSAGING modal for read-only Q&A with a
// teammate's agent. It runs inside mx.suspend(), so every `esc` here just returns — which repaints
// the session you came from. That is the whole design: nothing in this modal can cost you a session.
//
// Cancelling is not a failure. Asking takes seconds; answering takes minutes (the peer may have to
// approve it by hand, then a read-only engine turn runs — consult_answer.go). So the foreground wait
// is a courtesy for the fast case, and anything slower is handed to a watcher that banners you when
// the answer lands (peer_watch.go). Answers are UNTRUSTED: injected as a labelled bracketed-paste
// block for the human to review, never auto-submitted.
//
// The foreground ceiling is short on purpose — past a minute, waiting in a modal beats nothing but
// losing your session, and the watcher is strictly better than both.
const peerAskCeiling = 60 * time.Second

func peerMenu(mx peerMenuTarget) {
	if api.LoadToken() == "" {
		cgNote("Peer messages", []string{dim("you're not signed in — run `ptln login` first.")})
		return
	}
	c := api.New()
	// Unread first: with cancel-and-be-told-later as the primary path, the modal's usual job on
	// open is handing you the answer that came back while you were working — or, now, handing you the
	// question a teammate is waiting on you to answer. Both halves of the conversation, one list.
	if msgs := mergePeerMessages(openPeerMessages(), inboundPeerMessages(c)); len(msgs) > 0 &&
		!peerInboxStage(mx, c, msgs) {
		return
	}
	peerAskStage(mx, c)
}

// peerInboxStage lists open messages. Returns true when the user wants the ask-someone-new stage.
// Direction-agnostic: an inbound question (a peer asking US) is a row like any other, and opening one
// leads to answer-or-decline instead of inject.
func peerInboxStage(mx peerMenuTarget, c *api.Client, msgs []peerMessage) bool {
	items := make([]string, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, peerMessageRow(m))
	}
	n, key, ok := cgPicker{
		Title:  "Peer messages",
		Body:   []string{dim("questions waiting on you, replies that landed while you worked, asks still out there")},
		Items:  items,
		Verb:   "open",
		Extras: []cgChoice{{Key: 'n', Label: "ask someone new"}},
	}.run()
	if !ok {
		return false
	}
	if key == 'n' {
		return true
	}
	m := msgs[n]
	if m.Direction == dirInbound {
		answerInboundStage(m) // their question, then answer or decline — via the local daemon
	} else if m.Resolved() {
		deliverPeerAnswer(mx, m)
	} else {
		// Still out. Wait for it again, or WITHDRAW it — the second was impossible until cancel existed,
		// and esc-to-the-watcher was never a withdrawal (peer_cancel.go).
		outboundOpenStage(mx, c, m)
	}
	return false
}

// peerAskStage is compose-and-send: pick a recipient, type the message, send. One modal whose body
// changes; esc at any prompt returns to the session.
func peerAskStage(mx peerMenuTarget, c *api.Client) {
	peers, err := c.ListPeers()
	if err != nil {
		cgNote("Ask a peer", []string{fmt.Sprintf("%s✗ couldn't list peers: %v%s", cgBad, err, cgOff)})
		return
	}
	// One selectable row per advertised project — a consult is always scoped to a label.
	type target struct {
		daemonID, device, label string
		online                  bool
	}
	var ts []target
	for _, p := range peers {
		for _, lbl := range p.Projects {
			ts = append(ts, target{p.DaemonID, p.DeviceLabel, lbl, p.Online})
		}
	}
	if len(ts) == 0 {
		cgNote("Ask a peer", []string{
			dim("no peer advertises a project you can reach."),
			dim("(a peer is a teammate whose daemon advertises a project — ask them to run `ptln daemon`.)")})
		return
	}

	items := make([]string, 0, len(ts))
	for _, t := range ts {
		dot := sgr(cgDim, "○")
		if t.online {
			dot = sgr(cgOK, "●")
		}
		dev := t.device
		if dev == "" {
			dev = "(unnamed)"
		}
		items = append(items, dot+" "+dev+" "+dim("· "+t.label))
	}
	i, _, ok := cgPicker{
		Title: "Ask a peer",
		Body:  []string{dim("read-only feedback from a teammate's agent — who do you want to ask?")},
		Items: items,
		Verb:  "pick a peer",
	}.run()
	if !ok {
		return
	}
	t := ts[i]

	// cgCompose, not cgAsk: the questions people send here are excerpts pasted out of an LLM
	// discussion, and on a single-line field a paste submitted at the first newline and dropped the
	// rest without saying so (menu_compose.go).
	question, ok := cgCompose("Ask "+t.device, []string{
		dim("about " + t.label + " — one message, sent to their agent"),
		dim("they approve it on their end; the answer is read-only and untrusted")}, "")
	if !ok || strings.TrimSpace(question) == "" {
		return
	}

	cgBox("Ask "+t.device, []string{dim("about " + t.label), "", "  " + sgr(cgWire, "☎") + " sending…"})
	id, err := c.AskPeer(t.daemonID, t.label, question)
	if err != nil {
		cgNote("Ask "+t.device, []string{fmt.Sprintf("%s✗ ask failed: %v%s", cgBad, err, cgOff)})
		return
	}
	// Stamp the session that asked. That key is the delivery ADDRESS (peer_deliver.go): an answer
	// landing later goes into this session or into none, never into whichever one is focused then.
	_, _, _, sessKey, _ := mx.ActiveLaunch()
	m := peerMessage{ID: id, Direction: dirOutbound, Peer: t.device, Project: t.label,
		Question: question, Status: taskSubmitted, AskedAt: time.Now(), Session: sessKey}
	putPeerMessage(m) // recorded BEFORE the wait, so a cancel (or a crash) can't lose the handle
	awaitConsult(mx, c, m)
}

// awaitConsult is the waiting stage: a live progress line that READS STDIN, so esc ends it at once.
// On anything but a prompt answer it hands the message to the watcher and says so — the answer is
// still coming, it just arrives as a banner instead of a block.
func awaitConsult(mx peerMenuTarget, c *api.Client, m peerMessage) {
	cgBox("Ask "+m.Peer, []string{dim("about " + m.Project), "", "  " + dim(oneLine(m.Question)),
		"  " + brand.HintBar("WAITING", []brand.Hint{
			{Key: "esc", Label: "close — you'll be told when they answer"},
			{Key: "ctrl-\\ p", Label: "to withdraw it"}}, 0)})
	var res *api.ConsultResult
	out, err := waitJob{
		What:    fmt.Sprintf("asking %s about %s", m.Peer, m.Project),
		Ceiling: peerAskCeiling,
		Poll:    2 * time.Second,
		Check: func() (bool, error) {
			r, gerr := c.GetConsult(m.ID)
			if gerr != nil {
				return false, gerr
			}
			if consultTerminal(r.Status) {
				res = r
				return true, nil
			}
			return false, nil
		},
	}.Run()

	if out == waitDone && res != nil {
		m.Status, m.Answer, m.AnsweredAt = a2aTaskState(res.Status), res.Answer, time.Now()
		if m.Answer == "" {
			m.Answer = strings.TrimSpace(peerStateLabel(m.Status) + detailSuffix(res.Detail))
		}
		putPeerMessage(m)
		deliverPeerAnswer(mx, m)
		return
	}
	if out == waitFailed {
		fmt.Printf("\n  %s✗ couldn't reach the control plane: %v%s\n", cgBad, err, cgOff)
	}
	startPeerWatch(mx, c, m)
	fmt.Printf("\n  %s☎%s still out with %s — %s\n", cgWire, cgOff, m.Peer,
		dim("you'll get a banner when they answer; ctrl-\\ p to read it"))
	time.Sleep(700 * time.Millisecond) // long enough to read; esc already meant "back to my session"
}
