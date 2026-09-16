package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The local hand-off between an agent's ask_session call and the mux that has to carry it out.
//
// Same shape and the same reasons as peer_store.go: cg-mcp runs in a different process from the mux,
// a file is the only thing both can see, and a cache of in-flight state must never fail a menu or a
// tool call over a corrupt read. Errors are swallowed into empty/no-op deliberately.
//
// Kept SEPARATE from peer-messages.json rather than folded in. A peer consult has a server-side id,
// a consent state, a budget and a device; a local ask has none of those and has a target session name
// instead. One file for both would mean every field on either had to be optional on the other, and
// the pruning windows differ by two orders of magnitude (10 minutes vs days).

var askStoreMu sync.Mutex

func askStorePath() string { return filepath.Join(stateDir(), "session-asks.json") }

func loadAsksAt(path string) []sessionAsk {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []sessionAsk
	if json.Unmarshal(b, &out) != nil {
		return nil // a corrupt cache is an empty one, never an error surfaced to an agent
	}
	return out
}

func saveAsksAt(path string, asks []sessionAsk) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(asks, "", "  ")
	if err != nil {
		return err
	}
	// 0600: a question and its answer are conversation content.
	return os.WriteFile(path, b, 0o600)
}

// pruneAsks drops what nobody can act on any more: anything terminal and older than the collection
// window, plus open asks whose TTL has passed (marked failed rather than deleted, so the asking side
// gets a reason instead of a record that silently vanished).
func pruneAsks(asks []sessionAsk, now time.Time) []sessionAsk {
	const keepDone = 30 * time.Minute
	out := make([]sessionAsk, 0, len(asks))
	for _, a := range asks {
		if a.expired(now) {
			a.Status = askFailed
			a.Reason = "timed out — the session didn't finish answering within " + askTTL.String()
			a.DoneAt = now
		}
		if a.done() && !a.DoneAt.IsZero() && now.Sub(a.DoneAt) > keepDone {
			continue
		}
		out = append(out, a)
	}
	return out
}

// putAsk inserts or replaces by id, newest first.
func putAsk(a sessionAsk) {
	// Stamp an unset AskedAt rather than trusting the caller. A zero timestamp reads as "asked at the
	// epoch", which pruneAsks correctly treats as long expired — so a record written without one
	// would vanish on the very next read, and the asking agent would poll a question that was never
	// really stored. Cheap to make impossible.
	if a.AskedAt.IsZero() {
		a.AskedAt = time.Now()
	}
	askStoreMu.Lock()
	defer askStoreMu.Unlock()
	path := askStorePath()
	asks := loadAsksAt(path)
	replaced := false
	for i := range asks {
		if asks[i].ID == a.ID {
			asks[i] = a
			replaced = true
			break
		}
	}
	if !replaced {
		asks = append([]sessionAsk{a}, asks...)
	}
	_ = saveAsksAt(path, pruneAsks(asks, time.Now()))
}

// getAsk reads one ask by id. The asking side polls this waiting for a terminal status.
func getAsk(id string) (sessionAsk, bool) {
	askStoreMu.Lock()
	defer askStoreMu.Unlock()
	for _, a := range loadAsksAt(askStorePath()) {
		if a.ID == id {
			return a, true
		}
	}
	return sessionAsk{}, false
}

// openAsks is what the mux poller consumes: every ask still waiting to be delivered or answered.
// Pruning on read is what turns an abandoned ask into a failed one with a reason.
func openAsks() []sessionAsk {
	askStoreMu.Lock()
	defer askStoreMu.Unlock()
	path := askStorePath()
	asks := pruneAsks(loadAsksAt(path), time.Now())
	_ = saveAsksAt(path, asks)
	out := make([]sessionAsk, 0, len(asks))
	for _, a := range asks {
		if !a.done() {
			out = append(out, a)
		}
	}
	return out
}

// ---- the session roster ------------------------------------------------------------------------
//
// list_sessions runs in cg-mcp, which cannot see the mux's children. So the mux PUBLISHES its roster
// here on the same loop that picks up asks, and the tool reads it.
//
// Deliberately a snapshot, not a live query: a name that went stale in the last couple of seconds
// costs one clear "no session named …" from resolveSessionName, which happens against the REAL child
// list in the mux. The roster only has to be good enough to address by.

func rosterPath() string { return filepath.Join(stateDir(), "session-roster.json") }

// publishSessions writes the current roster. Best-effort: a failed write means list_sessions is
// briefly stale, which is not worth failing a mux loop over.
func publishSessions(live []sessionCandidate) {
	askStoreMu.Lock()
	defer askStoreMu.Unlock()
	if b, err := json.MarshalIndent(live, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(rosterPath()), 0o700)
		_ = os.WriteFile(rosterPath(), b, 0o600)
	}
}

// publishedSessions reads the roster. Empty on any problem — "no sessions listed" is a survivable
// answer, an error surfaced to an agent mid-task is not.
func publishedSessions() []sessionCandidate {
	askStoreMu.Lock()
	defer askStoreMu.Unlock()
	b, err := os.ReadFile(rosterPath())
	if err != nil {
		return nil
	}
	var out []sessionCandidate
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}
