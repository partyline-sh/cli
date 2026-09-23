//go:build darwin && tray

package main

import (
	"fyne.io/systray"

	"partyline.sh/partyline/internal/traypeer"
)

// peer_rows.go — the peer-messaging section of the menu: what a teammate's agent is asking, what this
// machine answered on its own, and what came back for you.
//
// THIS FILE IS NOW ONLY WIRING. Every decision — which rows are visible, what each says, which
// notification a poll earns, how a question is wrapped — lives in internal/traypeer, which has no
// systray import and therefore has tests that actually RUN under plain `go test ./...`. This package
// can't: it needs cgo and a GUI. What stays here is systray construction and the click loop.
//
// THE TRAY STILL HOLDS NOTHING. No token, no socket, no HTTP call — the rules haven't moved. Approve
// and Decline shell `ptln peer approve|decline <id>`, exactly as every other action here shells the
// CLI. `ptln` owns the token and the daemon socket; the tray owns a menu.
//
// WHY A SUBMENU WITH THE QUESTION IN IT, rather than a row that opens a terminal. Approving a consult
// requires echoing a digest of the question text that was DISPLAYED (daemonctl.QuestionDigest), so
// that approving something nobody read isn't an available shortcut. A counts-only row would leave two
// options: open a terminal (which is not "approve and do nothing else"), or weaken the digest check.
// So the question goes IN the submenu — reading it there IS the surfacing the digest attests to, and
// then one click is honest. This is the only content the tray renders, and it renders it to protect a
// security check.
//
// PRE-ALLOCATED, like the session rows. systray can't grow a menu after start, so every row and every
// submenu item exists from the first paint and is shown, retitled, or hidden per poll.

// newPeerSection builds the block. Call it where it should appear in the menu — systray order is
// creation order.
func newPeerSection() *traypeer.Section {
	hdr := systray.AddMenuItem("", "questions from teammates' agents")
	hdr.Disable()
	hdr.Hide()

	rows := make([]*traypeer.Row, traypeer.MaxRows)
	for i := range rows {
		item := systray.AddMenuItem("", "a teammate's agent is waiting on you")
		qLines := make([]traypeer.Item, traypeer.QLines)
		for j := range qLines {
			line := item.AddSubMenuItem("", "")
			line.Disable()
			qLines[j] = line
		}
		approve := item.AddSubMenuItem("Approve — answer read-only", "one read-only turn on this machine's checkout")
		decline := item.AddSubMenuItem("Decline", "free the asker now instead of at the timeout")
		item.Hide()
		row := &traypeer.Row{Item: item, QLines: qLines}
		watchRow(row, approve, decline)
		rows[i] = row
	}

	more := systray.AddMenuItem("", "")
	more.Disable()
	more.Hide()
	// Observability, not a control: the auto-answer flow is designed to need nobody, so this line is
	// the only place it's visible at all. Disabled — there is nothing to decide.
	auto := systray.AddMenuItem("", "read-only answers this machine gave today")
	auto.Disable()
	auto.Hide()
	repl := systray.AddMenuItem("", "replies to your asks")
	repl.Disable()
	repl.Hide()

	return traypeer.NewSection(traypeer.Config{
		Hdr: hdr, More: more, Auto: auto, Repl: repl, Rows: rows,
		// nativeNotify + quiet rather than notify(): same policy, but the Section applies the
		// tray-quiet gate itself so the rule is covered by a test that runs. See notify() in main.go.
		Post:  nativeNotify,
		Quiet: quiet,
	})
}

// watchRow runs for the life of the process, one goroutine per row. A row's id changes under it as the
// queue moves, so the id is read AT CLICK TIME — never captured when the row was built, which would
// approve a question that had since been replaced by another.
func watchRow(row *traypeer.Row, approve, decline *systray.MenuItem) {
	go func() {
		for {
			select {
			case <-approve.ClickedCh:
				if id := row.CurrentID(); id != "" {
					// `ptln peer approve` re-fetches the question from the daemon and passes it as the
					// digest, so the daemon still gets to refuse if the text it holds isn't the text that
					// was shown. The tray asserts nothing.
					ptln("peer", "approve", id)
				}
			case <-decline.ClickedCh:
				if id := row.CurrentID(); id != "" {
					ptln("peer", "decline", id)
				}
			}
		}
	}()
}
