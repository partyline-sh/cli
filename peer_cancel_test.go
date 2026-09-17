package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/daemonctl"
)

// `canceled` FINALLY HAS A PRODUCER. The local store has used A2A's TaskState names since it was
// written, and taskCanceled sat there unreachable because nothing could withdraw an ask: esc on a wait
// hands it to the watcher, which is not a withdrawal. These pin the state machine now that cancel
// exists — the translation, the terminal-ness, and the two things that would otherwise keep running:
// the watcher, and the answering machine's pending set.

func TestCancelledIsATerminalStateEverywhere(t *testing.T) {
	if !consultTerminal("canceled") {
		t.Error("a withdrawn consult has stopped moving — the watcher must not keep polling it")
	}
	if got := a2aTaskState("canceled"); got != taskCanceled {
		t.Errorf("a2aTaskState(canceled) = %q, want %q", got, taskCanceled)
	}
	if !(peerMessage{Status: taskCanceled}).Resolved() {
		t.Error("taskCanceled must be Resolved, or the inbox would offer to wait for it again")
	}
	if peerStateLabel(taskCanceled) == taskCanceled {
		t.Error("the screen wants a human word for it, not the identifier")
	}
	// ask_peer's 45s poll and check_consult both go through consultOutcome — a cancelled ask must end
	// the poll promptly instead of running out the ceiling on a question nobody will answer.
	out, done := consultOutcome(&api.ConsultResult{Status: "canceled", Detail: "withdrawn by the asker"})
	if !done {
		t.Fatal("consultOutcome must treat canceled as terminal")
	}
	if !strings.Contains(strings.ToUpper(out), "WITHDRAWN") || !strings.Contains(out, "withdrawn by the asker") {
		t.Errorf("the model has to be told it was withdrawn, and not to re-ask: %q", out)
	}
}

// The watcher STOPS on a cancel and files it as cancelled — not as "no answer", which is what a
// deadline-expiry would have written after ten more minutes of pointless polling.
func TestWatcherStopsOnACancelledAsk(t *testing.T) {
	p := &fakePoller{res: []api.ConsultResult{{Status: "canceled", Detail: "withdrawn by the asker"}}}
	var got peerMessage
	m := peerMessage{ID: "c1", Direction: dirOutbound, Peer: "air", Project: "partyline", AskedAt: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watchPeerMessage(ctx, p, m, time.Millisecond, func(x peerMessage) { got = x }, &fakeBanner{})
	if got.Status != taskCanceled {
		t.Fatalf("filed status %q, want %q", got.Status, taskCanceled)
	}
	if p.count() != 1 {
		t.Errorf("polled %d times — a terminal state on the first poll ends the watch", p.count())
	}
	if b := peerBanner(got); !strings.Contains(b, "cancelled") {
		t.Errorf("the banner must say what happened: %q", b)
	}
}

// ---- the ANSWERING side: the pending set drops it, and says why ------------

func TestWithdrawDropsThePendingConsult(t *testing.T) {
	dir, err := os.MkdirTemp("", "ptlnwd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "pending.json")
	q := newConsultQueue(path)
	ev := api.ConsultEvent{Type: "consult", ConsultID: "c1", ProjectLabel: "partyline", Question: askQ}
	q.Add(ev, time.Now())

	if _, held := q.Withdraw("c1"); !held {
		t.Fatal("Withdraw must report that it WAS holding the question — that is what gates the console line")
	}
	if q.Len() != 0 {
		t.Error("a withdrawn consult must leave the pending set, or approve would burn a read-only turn for nothing")
	}
	if !q.Withdrawn("c1") {
		t.Error("the withdrawal has to be remembered long enough to explain the screen the owner is looking at")
	}
	// The DISK copy too, so a restart doesn't resurrect it.
	if len(readPendingConsultsAt(path, time.Now())) != 0 {
		t.Error("the on-disk set still holds a withdrawn consult — a restart would offer it again")
	}
	// A reconnect re-pushing the same cancel is quiet (held=false), and does not resurrect anything.
	if _, held := q.Withdraw("c1"); held {
		t.Error("a re-pushed cancel must not announce itself twice")
	}
	if q.Withdrawn("c-never-seen") {
		t.Error("an id nobody withdrew is not withdrawn")
	}
}

// approve-consult on a withdrawn id fails CLEANLY, naming the withdrawal. Everything else keeps the one
// deliberately-indistinguishable refusal: an unknown id must not learn anything from the error text.
func TestApproveAfterWithdrawSaysWithdrawn(t *testing.T) {
	f := newFakeDaemon(t)
	ev := api.ConsultEvent{Type: "consult", ConsultID: "c1", ProjectLabel: "partyline", Question: askQ}
	f.q.Add(ev, time.Now())
	if _, err := f.client.GetConsult("c1"); err != nil {
		t.Fatalf("fetching to show should work before the withdrawal: %v", err)
	}
	f.q.Withdraw("c1")

	err := f.client.ApproveConsult("c1", askQ)
	if err == nil {
		t.Fatal("approving a withdrawn consult must fail — nobody is waiting for the answer")
	}
	if !strings.Contains(err.Error(), "withdrawn") {
		t.Errorf("the refusal must say it was withdrawn, not just 'not waiting': %v", err)
	}
	if len(f.answered) != 0 {
		t.Error("no answer turn may start for a withdrawn consult")
	}
	// An id that was never ours still gets the generic refusal — cancel must not become a way to probe
	// which consult ids exist on this machine.
	err = f.client.ApproveConsult("c-unknown", askQ)
	if err == nil || strings.Contains(err.Error(), "withdrawn") {
		t.Errorf("an unknown id must keep the indistinguishable refusal: %v", err)
	}
	// deny-consult on a withdrawn id says the same thing rather than trying to decline it upstream.
	if err := f.client.DenyConsult("c1", "no"); err == nil || !strings.Contains(err.Error(), "withdrawn") {
		t.Errorf("deny on a withdrawn consult: %v", err)
	}
	if len(f.declined) != 0 {
		t.Error("nothing to decline — the asker already took it back")
	}
}

// The digest wall is untouched by any of this: fetching still precedes approving, and a wrong digest is
// still refused. Cancel must not have opened a way to approve unread.
func TestWithdrawDidNotWeakenTheProofOfSurfacing(t *testing.T) {
	f := newFakeDaemon(t)
	f.q.Add(api.ConsultEvent{Type: "consult", ConsultID: "c2", ProjectLabel: "p", Question: askQ}, time.Now())
	if err := f.client.ApproveConsult("c2", "some other text"); err == nil {
		t.Fatal("a mismatched digest must still be refused")
	}
	if err := f.client.ApproveConsult("c2", askQ); err != nil {
		t.Fatalf("the honest path must still work: %v", err)
	}
	if len(f.answered) != 1 {
		t.Errorf("answered = %v", f.answered)
	}
	_ = daemonctl.QuestionDigest // the digest function itself is deliberately untouched
}
