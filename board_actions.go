package main

import "partyline.sh/partyline/internal/api"

// board_actions.go — WHICH moves the terminal board offers on a card, and what each one means.
//
// This mirrors actionsFor() in web/src/components/run-actions.tsx, deliberately and visibly. The
// server decides what is PERMITTED (every run endpoint status-guards itself and answers 409 when a
// card has already moved); this decides what is OFFERED, which is a UI question and has to agree
// with the web or the same card would grow different moves in two places. boardActionParityTest
// pins the two matrices together so a change on one side fails the build on this one.
//
// The rules that are easy to get wrong, and why they are what they are:
//   - A FAILED run never offers Accept. You cannot "ship" a run that died, and offering it is what
//     let a rate-limited run be marked shipped.
//   - Continue means different endpoints from different states: `retry` from failed (pick up where
//     it stopped, keep finished tasks), `resume` from a pause or a stall (resume in place).
//   - On a run that still looks ALIVE, the recovery moves are offered but muted and forced: the
//     server guards against clobbering a live crank, and a human clicking through is the override.
//   - Discard is the destructive one: it archives the card OFF the board. Restart throws away the
//     attempt but keeps the card. They are not synonyms and the copy must not blur them.

// boardAction is one offered move.
type boardAction struct {
	Key   string // stable identity ("accept", "continue", "requeue"…) — what the hint bar keys off
	Label string // what the operator reads
	Path  string // the run endpoint segment it POSTs to
	Hint  string // one line: what this does, in the terms the operator thinks in

	Force   bool // append ?force=1 — a human overriding the server's still-active guard
	Danger  bool // destructive: needs a confirm before it fires
	Muted   bool // offered, but not the obvious move (the run still looks alive)
	Confirm string
}

// The moves themselves. Defined once so the state matrix below reads as a matrix and not as prose.
var (
	actAccept = boardAction{Key: "accept", Label: "Accept", Path: "accept",
		Hint: "marks this reviewed and done — moves the card to Accepted. Changes the board, not the code."}

	actRestart = boardAction{Key: "restart", Label: "Restart", Path: "restart", Danger: true,
		Hint:    "throws this attempt away and rebuilds from scratch in a fresh worktree.",
		Confirm: "Restart from scratch? All current progress is discarded."}

	actBacklog = boardAction{Key: "requeue", Label: "To backlog", Path: "requeue",
		Hint: "stops the run and parks the card in Backlog, unstarted. Nothing is deleted."}

	actDiscard = boardAction{Key: "discard", Label: "Discard", Path: "discard", Danger: true,
		Hint:    "archives this run OFF the board. The branch and commits stay in git; the card is gone.",
		Confirm: "Discard this run? It leaves the board entirely."}

	actStart = boardAction{Key: "start", Label: "Start", Path: "start",
		Hint: "sends this run to its machine and starts building now."}

	actPause = boardAction{Key: "pause", Label: "Pause", Path: "pause",
		Hint: "holds the live run mid-flight. Resume picks the same process back up."}

	actCancel = boardAction{Key: "cancel", Label: "Cancel", Path: "cancel", Danger: true,
		Hint:    "kills the run and removes the card from the board. Committed work stays in git.",
		Confirm: "Cancel this run? Work in progress on the machine is abandoned."}
)

// contAction is Continue, whose endpoint depends on where the run is stuck.
func contAction(path, hint string) boardAction {
	return boardAction{Key: "continue", Label: "Continue", Path: path, Hint: hint}
}

// boardActions returns the moves to offer for a card, most-useful first. The first entry is the
// primary: it is what ⏎ fires on a tile and what the hint bar names.
//
// An UNSCHEDULED card (a work item with no run yet) is a different verb entirely — starting it
// PROMOTES the item, creating the run. That path needs a machine and a project, so it is handled
// by the promote flow rather than a plain POST, and appears here as the "promote" key.
func boardActions(c api.BoardCard) []boardAction {
	if c.Unscheduled {
		return []boardAction{
			{Key: "promote", Label: "Start", Path: "", Hint: "promotes this planned item to a run on a machine and starts it."},
			{Key: "delete", Label: "Delete", Path: "", Danger: true, Hint: "removes this planned item from the board.",
				Confirm: "Delete this planned item?"},
		}
	}

	switch c.Status {
	case "queued":
		return []boardAction{actStart, actBacklogRank(), actDiscard}

	case "failed":
		// No Accept, by design — see the file header.
		return []boardAction{
			contAction("retry", "picks up where it stopped, keeping finished tasks. Retries only what failed."),
			actRestart, actBacklog, actDiscard,
		}

	case "needs_approval":
		// force on Continue: a run paused for a predicted quota reset refuses an unattended resume
		// until the time passes, but a person asking again is new information and always overrides.
		//
		// The web narrows this set by pause REASON (presentPause), using a field the board payload
		// does not carry. Rather than infer a reason from the status message — a second, weaker copy
		// of a server-side decision — the terminal offers the full set, which is exactly what the
		// web does when a daemon reports no reason. Showing one move too many is recoverable;
		// hiding the one somebody needed is not.
		resume := contAction("resume", "resumes the paused task in place, keeping all work done so far.")
		resume.Force = true
		return []boardAction{actAccept, resume, actRestart, actBacklog, actDiscard}

	case "done":
		// A card already in Accepted is FINISHED. Its status is still "done" (acceptance is recorded
		// as accepted_at, not as a new status), so keying off status alone offered the full Review
		// matrix on it: Accept again as the primary, and To-backlog with no confirmation, which
		// un-ships accepted work from a two-key sequence.
		if c.Column == api.ColAccepted {
			unship := actBacklog
			unship.Danger = true
			unship.Confirm = "Send accepted work back to the backlog? It leaves the Accepted column."
			return []boardAction{unship}
		}
		// Signing off on a grade that has not landed is the one move that should not be a keystroke
		// away, so Accept is withheld while the reviewer is still running — the same rule the web
		// board applies (omit={c.reviewing ? ["accept"] : []}).
		if c.Reviewing || c.ReviewWaiting {
			return []boardAction{actRestart, actBacklog}
		}
		return []boardAction{actAccept, actRestart, actBacklog}

	case "killed":
		return []boardAction{actRestart}

	case "paused":
		// Held mid-flight by /pause (SIGSTOP). The process is alive, not stopped — so Resume, not
		// Continue, and no retry.
		resume := boardAction{Key: "resume", Label: "Resume", Path: "unpause",
			Hint: "releases the held run — the same process picks up where it froze."}
		return []boardAction{resume, actRestart, actCancel}

	case "accepted", "running":
		return buildingActions(c)
	}
	return nil // declined, or a status this build does not know — offer nothing rather than guess
}

// buildingActions is the Building column's matrix, which turns on whether the run still looks alive.
func buildingActions(c api.BoardCard) []boardAction {
	backlogStop := actBacklog
	if !c.Stalled {
		backlogStop.Danger = true
		backlogStop.Confirm = "Send this live run back to the backlog? It stops on its machine."
	}

	if c.Stalled {
		// Detectably stalled — the daemon is gone or silent, so the server agrees nothing is
		// progressing and the recovery moves are plain, unforced offers.
		return []boardAction{
			contAction("resume", "picks up where it stopped, keeping finished tasks and committed work."),
			actRestart, backlogStop, actCancel,
		}
	}

	// Looks active. Same recovery moves, but muted and forced behind a firmer confirm, so a
	// mis-keypress cannot clobber a genuinely-live crank. Pause leads: holding a healthy run is the
	// safe common move. Only `running` has a live crank process to freeze — an `accepted` run has
	// not spawned one yet.
	var out []boardAction
	if c.Status == "running" {
		out = append(out, actPause)
	}
	forceCont := contAction("resume", "this run still looks alive — forcing a continue may interrupt work in progress.")
	forceCont.Force, forceCont.Muted, forceCont.Danger = true, true, true
	forceCont.Confirm = "Force continue? This run still looks active on a live machine."

	forceRestart := actRestart
	forceRestart.Force, forceRestart.Muted = true, true
	forceRestart.Hint = "this run still looks alive — forcing a restart abandons it AND discards everything built so far."
	forceRestart.Confirm = "Force restart? This discards all progress on a run that still looks active."

	return append(out, forceCont, forceRestart, backlogStop, actCancel)
}

// actBacklogRank is To-backlog for a card that is ALREADY in the backlog: a queued run has nowhere
// to be sent back to, so the slot carries Discard's neighbour instead of a no-op move. Kept as a
// function so the queued row reads like the others.
func actBacklogRank() boardAction {
	return boardAction{Key: "rank", Label: "Reorder", Path: "",
		Hint: "moves this card up or down the queue — shift-↑/↓ on the tile."}
}

// The Reorder row is a SIGNPOST, not a button: it names the gesture that does the thing. Firing it
// from the menu would need a second in-menu mode for something one keystroke already does, so the
// menu entry says where the gesture lives and the handler says so too.

// primaryAction is what ⏎ fires on a tile: the first offered move, or nothing when a card has no
// moves at all (an Accepted card is finished — the board should not invent something to do to it).
func primaryAction(c api.BoardCard) (boardAction, bool) {
	acts := boardActions(c)
	if len(acts) == 0 {
		return boardAction{}, false
	}
	return acts[0], true
}
