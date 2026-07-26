package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
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

func peerMenu(mx *ptymux.Mux) {
	if api.LoadToken() == "" {
		cgBox("Peer messages", []string{dim("you're not signed in — run `ptln login` first.")})
		pause(stdin())
		return
	}
	c := api.New()
	// Unread first: with cancel-and-be-told-later as the primary path, the modal's usual job on
	// open is handing you the answer that came back while you were working.
	if msgs := openPeerMessages(); len(msgs) > 0 && !peerInboxStage(mx, c, msgs) {
		return
	}
	peerAskStage(mx, c)
}

// peerInboxStage lists open messages. Returns true when the user wants the ask-someone-new stage.
// The heading and rows are direction-agnostic: inbound questions (a peer asking US) belong in this
// same list when that half exists, and will render through the same row.
func peerInboxStage(mx *ptymux.Mux, c *api.Client, msgs []peerMessage) bool {
	rows := []string{dim("replies that landed while you were working, and asks still out there"), ""}
	for i, m := range msgs {
		rows = append(rows, peerMessageRow(i+1, m))
	}
	rows = append(rows, "", cgRow("n", "ask someone new", ""),
		"  "+brand.HintBar("PEER MESSAGES", []brand.Hint{
			{Key: "1-9", Label: "open"}, {Key: "n", Label: "ask someone new"},
			{Key: "q · esc", Label: "back to your session"}}, 0))
	cgBox("Peer messages", rows)

	s, ok := Input("open which number (or n)", "")
	if !ok {
		return false
	}
	if strings.EqualFold(s, "n") {
		return true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(msgs) {
		return false
	}
	m := msgs[n-1]
	if m.Resolved() {
		deliverPeerAnswer(mx, m)
	} else {
		awaitConsult(mx, c, m) // still out — re-enter the wait rather than making them guess
	}
	return false
}

// peerAskStage is compose-and-send: pick a recipient, type the message, send. One modal whose body
// changes; esc at any prompt returns to the session.
func peerAskStage(mx *ptymux.Mux, c *api.Client) {
	peers, err := c.ListPeers()
	if err != nil {
		cgBox("Ask a peer", []string{fmt.Sprintf("%s✗ couldn't list peers: %v%s", cgBad, err, cgOff)})
		pause(stdin())
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
		cgBox("Ask a peer", []string{
			dim("no peer advertises a project you can reach."),
			dim("(a peer is a teammate whose daemon advertises a project — ask them to run `ptln daemon`.)")})
		pause(stdin())
		return
	}

	cgBox("Ask a peer", []string{dim("read-only feedback from a teammate's agent — who do you want to ask?"),
		"  " + brand.HintBar("ASK PEER", []brand.Hint{
			{Key: "1-9", Label: "pick a peer"}, {Key: "q · esc", Label: "back to your session"}}, 0)})
	i, ok := Pick("number", ts, func(t target) string {
		dot := sgr(cgDim, "○")
		if t.online {
			dot = sgr(cgOK, "●")
		}
		dev := t.device
		if dev == "" {
			dev = "(unnamed)"
		}
		return dot + " " + dev + " " + dim("· "+t.label)
	})
	if !ok {
		return
	}
	t := ts[i]

	cgBox("Ask "+t.device, []string{
		dim("about " + t.label + " — one message, sent to their agent"),
		dim("they approve it on their end; the answer is read-only and untrusted"),
		"  " + brand.HintBar("COMPOSE", []brand.Hint{
			{Key: "⏎", Label: "send"}, {Key: "q · esc", Label: "back to your session"}}, 0)})
	question, ok := Input("your question", "")
	if !ok || question == "" {
		return
	}

	cgBox("Ask "+t.device, []string{dim("about " + t.label), "", "  " + sgr(cgWire, "☎") + " sending…"})
	id, err := c.AskPeer(t.daemonID, t.label, question)
	if err != nil {
		fmt.Printf("\n  %s✗ ask failed: %v%s\n", cgBad, err, cgOff)
		pause(stdin())
		return
	}
	m := peerMessage{ID: id, Direction: dirOutbound, Peer: t.device, Project: t.label,
		Question: question, Status: msgWaiting, AskedAt: time.Now()}
	putPeerMessage(m) // recorded BEFORE the wait, so a cancel (or a crash) can't lose the handle
	awaitConsult(mx, c, m)
}

// awaitConsult is the waiting stage: a live progress line that READS STDIN, so esc ends it at once.
// On anything but a prompt answer it hands the message to the watcher and says so — the answer is
// still coming, it just arrives as a banner instead of a block.
func awaitConsult(mx *ptymux.Mux, c *api.Client, m peerMessage) {
	cgBox("Ask "+m.Peer, []string{dim("about " + m.Project), "", "  " + dim(oneLine(m.Question)),
		"  " + brand.HintBar("WAITING", []brand.Hint{
			{Key: "esc", Label: "close — you'll be told when they answer"}}, 0)})
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
		m.Status, m.Answer, m.AnsweredAt = res.Status, res.Answer, time.Now()
		if m.Answer == "" {
			m.Answer = strings.TrimSpace(res.Status + detailSuffix(res.Detail))
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
