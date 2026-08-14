package main

import (
	"time"

	"partyline.sh/partyline/internal/api"
)

// WITHDRAWAL — the answering side of cancel.
//
// The asker can now take a question back (POST /consult/[id]/cancel), and the target daemon learns
// about it off its own SSE stream like every other daemon-directed event. Two things have to happen
// here, and only the first is obvious:
//
//  1. DROP IT from the pending set, or the daemon goes on holding a question nobody is waiting for —
//     and worse, keeps offering its owner an approve that will burn a read-only engine turn producing
//     an answer the control plane will refuse (recordConsultAnswer only transitions pending/delivered).
//
//  2. REMEMBER that it was withdrawn, briefly. Just deleting the entry makes `approve-consult <id>`
//     fail with the deliberately-indistinguishable "no consult is waiting on this machine", which is
//     the right answer for an id that was never ours and the WRONG answer here: the owner is looking at
//     a tray row or a console line that named this question a second ago, and "withdrawn by the asker"
//     is the only reply that explains the screen. It is safe to say, because the only way an id gets in
//     here is a cancel the control plane addressed to THIS daemon — the same provenance rule that makes
//     the pending set a sufficient authorization check (consult_pending.go).
//
// WHAT WE DELIBERATELY DON'T DO: kill an answer turn already in flight. A read-only engine turn is a
// child process mid-work; SIGTERMing it to save a few seconds of compute trades a clean failure for a
// half-written answer and a killed process group. It finishes, POSTs, and the control plane discards it
// (the status guard on recordConsultAnswer sees `canceled`, matches nothing, 409). The answer is thrown
// away by the one component that can do it without a race.
//
// The tombstone is IN MEMORY only. It exists to explain a screen the owner is looking at right now; a
// daemon that restarts in between has no screen to explain, and the withdrawn entry is already off the
// disk set (Withdraw saves), so the generic refusal is then the correct one.

// withdrawnTTL bounds the tombstone. Same window as pendingConsultTTL: past it the question would have
// timed out anyway, so there is no screen left that naming it could explain.
const withdrawnTTL = pendingConsultTTL

// Withdraw drops a consult the asker cancelled and records the withdrawal. Returns the event when it
// was actually holding it — the caller prints a line only then, so a reconnect re-push stays quiet.
func (q *consultQueue) Withdraw(id string) (api.ConsultEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.withdrawn == nil {
		q.withdrawn = map[string]time.Time{}
	}
	now := time.Now()
	for k, at := range q.withdrawn {
		if now.Sub(at) > withdrawnTTL {
			delete(q.withdrawn, k)
		}
	}
	q.withdrawn[id] = now
	qc, held := q.m[id]
	if !held {
		return api.ConsultEvent{}, false
	}
	delete(q.m, id)
	q.save()
	return qc.Event, true
}

// Withdrawn reports whether this id was cancelled by its asker, recently enough to say so.
func (q *consultQueue) Withdrawn(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	at, ok := q.withdrawn[id]
	return ok && time.Since(at) <= withdrawnTTL
}
