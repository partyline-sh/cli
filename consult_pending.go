package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The pending-consult set: which questions from peers are waiting on THIS machine's owner.
//
// THE BUG THIS FIXES. This set used to be a bare in-memory map filled only by live SSE pushes, so a
// daemon restart (self-update, crash, reboot, `ptln daemon restart`) dropped every pending consult on
// the floor. Nothing told the asker: their consult sat `pending` server-side until the 10-minute
// window closed and they got a timeout with no explanation, and the owner never saw the question at
// all. The durable DB row is the truth; this is a CACHE of it, and now a cache that survives a
// process death:
//
//   - every mutation writes the set to disk (0600, alongside the device token)
//   - a fresh process loads it back, so `approve-consult <id>` works immediately after a restart
//     rather than only after the stream happens to reconnect
//   - reconcileConsults re-reads the durable rows from the control plane and merges, so a consult
//     that arrived while the process was DOWN (no stream to push it) is recovered too
//
// PROVENANCE IS THE SECURITY BOUNDARY. An entry only ever enters this set from something the control
// plane addressed to this daemon: the per-daemon SSE stream, or reconcileConsults filtered to our own
// daemon id. Nothing a local caller says can put one here — which is what makes "is this id in the
// set?" a sufficient answer to "is this consult genuinely mine to answer?" (see daemon_control.go).

// pendingConsultTTL bounds how long an unapproved question lingers. Matches the server's consult
// window (CONSULT_TIMEOUT_MS): past it the row is timed_out server-side, so approving it would only
// burn an engine turn on an answer nobody can receive.
const pendingConsultTTL = 10 * time.Minute

func pendingConsultsPath() string {
	return filepath.Join(stateDir(), "daemon", "pending-consults.json")
}

// queuedConsult is one pending question plus when we first saw it — the age the console and the modal
// show ("waiting 4m"), and what the TTL prunes on.
type queuedConsult struct {
	Event  api.ConsultEvent `json:"event"`
	SeenAt time.Time        `json:"seen_at"`
}

// consultQueue is the durable pending set. Safe for concurrent use: the stream goroutine, the
// console, and the local control channel all touch it.
type consultQueue struct {
	mu   sync.Mutex
	path string
	m    map[string]queuedConsult
	// withdrawn: ids the ASKER cancelled, so a local surface's approve can say "withdrawn" instead of
	// the generic refusal. In memory only, TTL-bounded — see consult_withdraw.go for why both.
	withdrawn map[string]time.Time
}

// newConsultQueue loads the set from disk (a missing or corrupt file is an empty set — this is a
// cache, never something to fail daemon startup over) and prunes anything already past the window.
func newConsultQueue(path string) *consultQueue {
	q := &consultQueue{path: path, m: map[string]queuedConsult{}, withdrawn: map[string]time.Time{}}
	if b, err := os.ReadFile(path); err == nil {
		var on struct {
			Consults []queuedConsult `json:"consults"`
		}
		if json.Unmarshal(b, &on) == nil {
			for _, qc := range on.Consults {
				if qc.Event.ConsultID != "" {
					q.m[qc.Event.ConsultID] = qc
				}
			}
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prune(time.Now())
	q.save()
	return q
}

// prune drops entries past the TTL. Caller holds the lock.
func (q *consultQueue) prune(now time.Time) {
	for id, qc := range q.m {
		if now.Sub(qc.SeenAt) > pendingConsultTTL {
			delete(q.m, id)
		}
	}
}

// save persists the set. Caller holds the lock. 0600 — the questions are a teammate's words, not for
// other users on the box. Best-effort: a write failure must never stop the daemon answering.
func (q *consultQueue) save() {
	list := make([]queuedConsult, 0, len(q.m))
	for _, qc := range q.m {
		list = append(list, qc)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SeenAt.Before(list[j].SeenAt) })
	b, err := json.MarshalIndent(struct {
		Consults []queuedConsult `json:"consults"`
	}{list}, "", " ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(q.path, b, 0o600)
}

// Add records a consult addressed to this daemon. Returns false if it was already known — which is
// how a stream reconnect (or a reconcile pass) re-pushing a still-pending consult stays quiet instead
// of announcing the same question twice.
func (q *consultQueue) Add(ev api.ConsultEvent, seenAt time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, seen := q.m[ev.ConsultID]; seen {
		return false
	}
	q.m[ev.ConsultID] = queuedConsult{Event: ev, SeenAt: seenAt}
	q.prune(time.Now())
	q.save()
	return true
}

// Peek reads one pending consult WITHOUT consuming it — the fetch half of "show the question, then
// decide". Keeping this separate from Take is deliberate (see daemon_control.go).
func (q *consultQueue) Peek(id string) (queuedConsult, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	qc, ok := q.m[id]
	return qc, ok
}

// Take removes and returns one pending consult — the decide half. Atomic, so two surfaces racing to
// approve the same question (the console and the modal, say) can't both spawn an answer turn.
func (q *consultQueue) Take(id string) (api.ConsultEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	qc, ok := q.m[id]
	if !ok {
		return api.ConsultEvent{}, false
	}
	delete(q.m, id)
	q.save()
	return qc.Event, true
}

// List returns the pending set oldest first (the order to answer them in).
func (q *consultQueue) List() []queuedConsult {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]queuedConsult, 0, len(q.m))
	for _, qc := range q.m {
		out = append(out, qc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeenAt.Before(out[j].SeenAt) })
	return out
}

func (q *consultQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.m)
}

// readPendingConsultsAt is a STRICTLY READ-ONLY view of the pending set, oldest first, for a caller
// that must not touch the file — `ptln state`, which the tray polls every 4s. newConsultQueue prunes
// and rewrites on load, which is right for the daemon (it owns the file) and wrong for a poller (4s
// writes to the daemon's state, and a racing rewrite while the daemon is mid-save). So this opens,
// decodes, filters the TTL in memory, and stops.
func readPendingConsultsAt(path string, now time.Time) []queuedConsult {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // no daemon has ever run here, or nothing is waiting — both are "nothing to show"
	}
	var on struct {
		Consults []queuedConsult `json:"consults"`
	}
	if json.Unmarshal(b, &on) != nil {
		return nil // a cache we can't read is an empty list, never an error a menu has to render
	}
	out := make([]queuedConsult, 0, len(on.Consults))
	for _, qc := range on.Consults {
		if qc.Event.ConsultID == "" || now.Sub(qc.SeenAt) > pendingConsultTTL {
			continue // past the server's window: approving it would answer nobody
		}
		out = append(out, qc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeenAt.Before(out[j].SeenAt) })
	return out
}

// consultLister is the control-plane read reconcileConsults needs (an *api.Client satisfies it).
type consultLister interface {
	ListConsults(direction string) ([]api.Consult, error)
}

// reconcileConsults repopulates the pending set from the DURABLE rows, so a daemon that was down when
// a question arrived still sees it. Returns how many were recovered.
//
// Every row is filtered to OUR daemon id before it lands: the endpoint already refuses to show a
// consult targeting a machine the caller doesn't own, and this is the second wall — the pending set's
// whole value as a security check is that an entry can only be something the control plane addressed
// to us (consult_pending.go's provenance rule).
//
// Best-effort by design: the list endpoint is USER-authed, so a headless daemon enrolled by device
// token alone has nothing to call it with. Then this is a no-op and the stream's reconnect re-push is
// the recovery path (slower, but it still happens) — a missing account token must not stop the daemon.
func reconcileConsults(q *consultQueue, daemonID string, l consultLister) int {
	if l == nil || daemonID == "" {
		return 0
	}
	cs, err := l.ListConsults("inbound")
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range cs {
		if c.DaemonID != daemonID || c.ConsultID == "" {
			continue // addressed to another of this owner's machines — not ours to answer
		}
		seen := c.CreatedAt
		if seen.IsZero() {
			seen = time.Now()
		}
		if q.Add(api.ConsultEvent{Type: "consult", ConsultID: c.ConsultID,
			ProjectLabel: c.ProjectLabel, Question: c.Question}, seen) {
			n++
		}
	}
	return n
}

// shortDuration renders an age for a console line or an inbox row: "8s", "4m", "2h13m". Bounded and
// unit-suffixed, because "how long has this peer been waiting" is the number that decides whether you
// answer now or decline.
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
