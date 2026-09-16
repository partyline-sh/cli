package main

import (
	"context"
	"fmt"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/daemonctl"
)

// The daemon's end of the local control channel (internal/daemonctl). This is how a peer's question
// gets answered from somewhere other than this process's own stdin REPL — the `ctrl-\ p` modal today,
// ptln-tray next — while the ANSWERING still happens here.
//
// It has to happen here. Two independent reasons, neither of which a UI can work around:
//   - AUTH: POST /consult/[id]/answer is device-token authed and the write is scoped to
//     target_daemon. Only the daemon holds that token; the mux holds an account token, which the
//     answer route does not accept (and shouldn't — a consult must be answerable only by the machine
//     it was addressed to).
//   - LOCAL CHECKOUT: the answer is one read-only engine turn against THIS machine's own checkout,
//     resolved from the daemon's local registry (answerConsult → projectByLabel). The daemon is the
//     final authority on what it will answer; the control plane never names a path.
//
// So the contract is deliberately thin: a caller names a consult id and an action. Everything else —
// resolving the label, spawning the read-only turn, posting back — is answerConsult, unchanged and
// unduplicated.
//
// VALIDATION IS HERE, NOT IN THE CALLER. With more than one client, a rule enforced in one client's
// code path is a rule the other can skip. So every request is re-checked against the daemon's own
// pending set: an id that isn't in it is refused, full stop. That set can only be filled by something
// the control plane addressed to THIS daemon (a per-daemon stream push, or a reconcile filtered to our
// own daemon id — consult_pending.go), so a local caller cannot invent a consult, redirect one, or
// name a teammate's.

// consultActions is what the handler is allowed to DO once a request passes validation. Injected so a
// test can drive the real handler and the real socket against a fake daemon.
type consultActions struct {
	Approve func(api.ConsultEvent)       // answer it read-only (answerConsult, off-thread)
	Deny    func(api.ConsultEvent) error // decline it to the control plane
}

// startDaemonControl brings the control socket up for the life of ctx. Non-fatal: a daemon that can't
// bind the socket still does everything else (the console REPL is untouched), you just can't answer
// from another surface on this box.
func startDaemonControl(ctx context.Context, d daemonDevice, consults *consultQueue) func() {
	handle := consultControlHandler(d.DaemonID, consults, consultActions{
		Approve: func(ev api.ConsultEvent) { go answerConsult(d, ev) },
		Deny: func(ev api.ConsultEvent) error {
			return api.DeclineConsult(d.Base, d.Token, ev.ConsultID, "declined by owner")
		},
	})
	stop, err := daemonctl.Serve(daemonctl.SocketPath(), handle)
	if err != nil {
		fmt.Printf("⚠ local control channel unavailable (%v) — approve consults from this console\n", err)
		return func() {}
	}
	go func() {
		<-ctx.Done()
		stop()
	}()
	return stop
}

// consultControlHandler is the validating server. Pure of I/O beyond `act`, so the round trip is
// testable end-to-end over a real socket.
func consultControlHandler(daemonID string, consults *consultQueue, act consultActions) func(daemonctl.Request) daemonctl.Response {
	return func(req daemonctl.Request) daemonctl.Response {
		switch req.Op {
		case daemonctl.OpPing:
			// Which daemon is this? Lets a caller tell "no daemon here" apart from "that question was
			// addressed to another machine". The id is already public in `ptln state`; the token is not,
			// and never crosses this wire.
			return daemonctl.Response{OK: true, DaemonID: daemonID}

		case daemonctl.OpConsults:
			out := make([]daemonctl.Consult, 0, consults.Len())
			for _, qc := range consults.List() {
				out = append(out, asCtlConsult(qc))
			}
			return daemonctl.Response{OK: true, DaemonID: daemonID, Consults: out}

		case daemonctl.OpConsult:
			qc, ok := consults.Peek(req.ID) // Peek, not Take: fetching to SHOW must not consume
			if !ok {
				return withdrawnOrNotWaiting(consults, req.ID)
			}
			c := asCtlConsult(qc)
			return daemonctl.Response{OK: true, DaemonID: daemonID, Consult: &c}

		case daemonctl.OpApproveConsult:
			// Proof-of-surfacing before anything else: the caller must echo a digest of the question
			// text it displayed. A menu item that approves without ever showing the question can't
			// produce this, which is the point — approving unread should not be the easy path.
			qc, ok := consults.Peek(req.ID)
			if !ok {
				return withdrawnOrNotWaiting(consults, req.ID)
			}
			if req.Shown != daemonctl.QuestionDigest(qc.Event.Question) {
				return daemonctl.Response{Error: "show the question before approving it (fetch it first — the digest didn't match)"}
			}
			ev, ok := consults.Take(req.ID) // atomic: the console and a UI can't both spawn a turn
			if !ok {
				return notWaiting(req.ID)
			}
			if act.Approve != nil {
				act.Approve(ev)
			}
			fmt.Printf("\n✓ answering consult %s read-only (approved from a local surface)…\n> ", ev.ConsultID)
			return daemonctl.Response{OK: true, DaemonID: daemonID, Started: true}

		case daemonctl.OpDenyConsult:
			ev, ok := consults.Take(req.ID)
			if !ok {
				return withdrawnOrNotWaiting(consults, req.ID)
			}
			if act.Deny != nil {
				if err := act.Deny(ev); err != nil {
					// The asker isn't freed, and the entry is gone from the pending set — say so rather
					// than reporting a success the asker will never see.
					return daemonctl.Response{Error: "couldn't tell the control plane: " + err.Error()}
				}
			}
			fmt.Printf("\n✓ declined consult %s (from a local surface)\n> ", ev.ConsultID)
			return daemonctl.Response{OK: true, DaemonID: daemonID}
		}
		return daemonctl.Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

// notWaiting is the one refusal for "that id isn't mine to act on" — unknown, already decided, or
// addressed to a different machine all look the same from outside, on purpose.
func notWaiting(id string) daemonctl.Response {
	return daemonctl.Response{Error: fmt.Sprintf("no consult %s is waiting on this machine", id)}
}

// withdrawnOrNotWaiting is notWaiting with the ONE case it is worth distinguishing: the asker withdrew
// this question while the owner was looking at it. That is safe to name (an id only reaches the
// withdrawn set from a cancel the control plane addressed to THIS daemon — same provenance rule as the
// pending set), and it is the only reply that explains a tray row or console line that named the
// question a second ago. Everything else stays indistinguishable.
func withdrawnOrNotWaiting(consults *consultQueue, id string) daemonctl.Response {
	if consults.Withdrawn(id) {
		return daemonctl.Response{Error: fmt.Sprintf("consult %s was withdrawn by the asker — nothing to answer", id)}
	}
	return notWaiting(id)
}

func asCtlConsult(qc queuedConsult) daemonctl.Consult {
	return daemonctl.Consult{
		ID:         qc.Event.ConsultID,
		Project:    qc.Event.ProjectLabel,
		Question:   qc.Event.Question,
		WaitingSec: int(time.Since(qc.SeenAt) / time.Second),
	}
}
