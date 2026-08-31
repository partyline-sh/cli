package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

func ev(id, label, q string) api.ConsultEvent {
	return api.ConsultEvent{Type: "consult", ConsultID: id, ProjectLabel: label, Question: q}
}

// THE DURABILITY BUG. The pending set used to be an in-memory map, so a daemon restart dropped every
// peer's question: the asker waited out the 10-minute window and got a timeout with no explanation,
// and the owner never even saw the question. A second consultQueue over the same path is exactly what
// a restarted daemon does, so this is the regression that has to fail if the set stops being durable.
func TestConsultQueueSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending-consults.json")
	q := newConsultQueue(path)
	if !q.Add(ev("c1", "partyline", "does the limiter retry 429s?"), time.Now()) {
		t.Fatal("first Add must report a new consult")
	}
	if q.Add(ev("c1", "partyline", "does the limiter retry 429s?"), time.Now()) {
		t.Error("a stream reconnect re-pushing the same consult must not announce it twice")
	}

	restarted := newConsultQueue(path) // ← the daemon died and came back
	qc, ok := restarted.Peek("c1")
	if !ok {
		t.Fatal("a restart lost the pending consult — the asker would time out with no explanation")
	}
	if qc.Event.Question != "does the limiter retry 429s?" || qc.Event.ProjectLabel != "partyline" {
		t.Errorf("restart mangled the consult: %+v", qc.Event)
	}
	if qc.SeenAt.IsZero() {
		t.Error("SeenAt must persist — it's how long the peer has been waiting")
	}

	// And approving after the restart works, which is the whole point.
	if _, ok := restarted.Take("c1"); !ok {
		t.Fatal("Take after a restart must find the consult")
	}
	if _, ok := newConsultQueue(path).Peek("c1"); ok {
		t.Error("an answered consult must not come back on the next restart")
	}
}

// The file holds a teammate's words and lives next to the device token: 0600, no wider.
func TestConsultQueueFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending-consults.json")
	newConsultQueue(path).Add(ev("c1", "p", "q"), time.Now())
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("pending consults file mode = %o, want 600", perm)
	}
}

// Past the server's consult window the row is timed_out server-side, so a stale entry must not
// survive a restart to burn an engine turn on an answer nobody can receive.
func TestConsultQueuePrunesPastTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending-consults.json")
	q := newConsultQueue(path)
	q.Add(ev("stale", "p", "old"), time.Now().Add(-pendingConsultTTL-time.Minute))
	q.Add(ev("fresh", "p", "new"), time.Now())
	got := newConsultQueue(path).List()
	if len(got) != 1 || got[0].Event.ConsultID != "fresh" {
		t.Fatalf("want only the fresh consult, got %d: %+v", len(got), got)
	}
}

// Take is the decide half and must be atomic: two surfaces (the console and the modal) racing on one
// id can't both spawn a read-only turn — the peer would get two answers and pay for two.
func TestConsultQueueTakeIsOnce(t *testing.T) {
	q := newConsultQueue(filepath.Join(t.TempDir(), "p.json"))
	q.Add(ev("c1", "p", "q"), time.Now())
	if _, ok := q.Take("c1"); !ok {
		t.Fatal("first Take must win")
	}
	if _, ok := q.Take("c1"); ok {
		t.Error("second Take must lose — one consult, one answer")
	}
}

type fakeLister struct {
	out []api.Consult
	err error
}

func (f fakeLister) ListConsults(string) ([]api.Consult, error) { return f.out, f.err }

// The other half of the durability fix: a question that arrived while the daemon was DOWN was never
// pushed to anyone, so only a read of the durable rows recovers it. And the reconcile must keep the
// provenance rule — a row addressed to another machine never enters this daemon's pending set, because
// "is this id in the set?" is what authorizes an approve.
func TestReconcileConsultsRecoversOnlyOurOwn(t *testing.T) {
	q := newConsultQueue(filepath.Join(t.TempDir(), "p.json"))
	l := fakeLister{out: []api.Consult{
		{ConsultID: "mine", DaemonID: "d-me", ProjectLabel: "partyline", Question: "safe to merge?",
			CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ConsultID: "not-mine", DaemonID: "d-other-machine", ProjectLabel: "acr-cloud", Question: "?"},
	}}
	if n := reconcileConsults(q, "d-me", l); n != 1 {
		t.Fatalf("recovered %d, want 1", n)
	}
	if _, ok := q.Peek("mine"); !ok {
		t.Error("a consult that arrived while we were down must be recovered")
	}
	if _, ok := q.Peek("not-mine"); ok {
		t.Error("a consult addressed to another machine must never enter this daemon's pending set")
	}
	// Idempotent: a second pass (or a stream push of the same row) recovers nothing new.
	if n := reconcileConsults(q, "d-me", l); n != 0 {
		t.Errorf("second reconcile recovered %d, want 0", n)
	}
}

// A daemon with no account token (device-token-only enrolment) or an offline control plane must not
// fail startup — the stream's reconnect re-push is the fallback.
func TestReconcileConsultsToleratesNoAccess(t *testing.T) {
	q := newConsultQueue(filepath.Join(t.TempDir(), "p.json"))
	if n := reconcileConsults(q, "d-me", fakeLister{err: errors.New("unauthenticated")}); n != 0 {
		t.Errorf("recovered %d on an auth failure, want 0", n)
	}
	if n := reconcileConsults(q, "", fakeLister{}); n != 0 {
		t.Errorf("recovered %d with no daemon id, want 0", n)
	}
}

func TestShortDuration(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{8 * time.Second, "8s"},
		{4 * time.Minute, "4m"},
		{2*time.Hour + 13*time.Minute, "2h13m"},
	} {
		if got := shortDuration(c.d); got != c.want {
			t.Errorf("shortDuration(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
