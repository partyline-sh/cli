package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ratelimit_note.go — a LOCAL breadcrumb saying "the provider is refusing work on this machine".
//
// The tray shows it, and the tray reads a local snapshot (`ptln state`) rather than calling the API:
// it polls every few seconds, and a network round-trip per poll would be both wasteful and a source
// of failures in the one surface whose job is to be quietly reliable.
//
// crank is the right writer because it's the only thing that ever learns this — the provider's
// refusal arrives mid-stream inside the worker, not on any channel the daemon watches. crank already
// self-reports the pause to the control plane; this is the same fact written where a local reader
// can see it.
//
// Deliberately NOT a queue or a log: one file, last-writer-wins, describing the machine's CURRENT
// state. History belongs in the run record, which already has it.

type rateLimitNote struct {
	At      time.Time `json:"at"`                 // when we hit it
	ResetAt time.Time `json:"reset_at,omitempty"` // zero = none given (entitlement/credits block)
	Note    string    `json:"note,omitempty"`     // the provider's own wording, when it gave us one
	Run     string    `json:"run,omitempty"`      // the run that hit it, for a deep link
}

func rateLimitNotePath() string { return filepath.Join(stateDir(), "rate-limit.json") }

// writeRateLimitNote records the block. Best-effort: a failure here must never affect the run, which
// has already been reported to the control plane by the time we're called.
func writeRateLimitNote(n rateLimitNote) {
	b, err := json.Marshal(n)
	if err != nil {
		return
	}
	_ = os.WriteFile(rateLimitNotePath(), b, 0o600)
}

// clearRateLimitNote removes it — called when a run completes without being blocked, so the tray
// stops warning about a limit that has since cleared.
func clearRateLimitNote() { _ = os.Remove(rateLimitNotePath()) }

// readRateLimitNote returns the current block, or nil when there isn't one worth showing.
//
// STALENESS IS THE WHOLE POINT of reading through a function. A tray that keeps saying "rate limited"
// after the window reopened is worse than one that says nothing — you'd stop believing it, and then
// miss the next real one.
//
//   - a reset time in the past → the window reopened; gone
//   - no reset time (an entitlement block) → held for 12h, because it needs a HUMAN to add credits
//     or enable the model, and it won't clear on its own
func readRateLimitNote() *rateLimitNote {
	b, err := os.ReadFile(rateLimitNotePath())
	if err != nil {
		return nil
	}
	var n rateLimitNote
	if json.Unmarshal(b, &n) != nil {
		return nil
	}
	now := time.Now()
	if !n.ResetAt.IsZero() {
		if now.After(n.ResetAt) {
			return nil // the window reopened
		}
		return &n
	}
	if n.At.IsZero() || now.Sub(n.At) > 12*time.Hour {
		return nil // an entitlement block this old is almost certainly resolved or abandoned
	}
	return &n
}
