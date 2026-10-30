package main

import (
	"testing"
	"time"
)

// A tray that keeps saying "rate limited" after the window reopened is worse than one that says
// nothing: you stop believing it, and then miss the next real block. These pin when a note is still
// worth showing.
func TestRateLimitNoteStaleness(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		note rateLimitNote
		want bool
	}{
		{"quota window still closed", rateLimitNote{At: now, ResetAt: now.Add(30 * time.Minute)}, true},
		{"quota window reopened", rateLimitNote{At: now.Add(-2 * time.Hour), ResetAt: now.Add(-time.Minute)}, false},
		// No reset time = an entitlement block. It will NOT clear on its own — someone has to add
		// credits or enable the model — so it's held far longer than a quota window.
		{"fresh entitlement block", rateLimitNote{At: now.Add(-time.Hour), Note: "credits required"}, true},
		{"day-old entitlement block", rateLimitNote{At: now.Add(-13 * time.Hour), Note: "credits required"}, false},
		{"no timestamps at all", rateLimitNote{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			writeRateLimitNote(c.note)
			got := readRateLimitNote() != nil
			if got != c.want {
				t.Errorf("shown = %v, want %v", got, c.want)
			}
		})
	}
}

// Clearing must actually clear: a run that completes without a block has to stop the warning, or
// the tray warns forever about a limit that lifted.
func TestRateLimitNoteClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeRateLimitNote(rateLimitNote{At: time.Now(), ResetAt: time.Now().Add(time.Hour)})
	if readRateLimitNote() == nil {
		t.Fatal("note not readable after write")
	}
	clearRateLimitNote()
	if readRateLimitNote() != nil {
		t.Error("note survived clear — the tray would warn about a limit that has lifted")
	}
	clearRateLimitNote() // clearing an absent note must not error
}

// The provider's own wording and the run id survive the round-trip, so the tray can quote it and
// link to the run that hit it.
func TestRateLimitNoteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeRateLimitNote(rateLimitNote{At: time.Now(), Note: "Usage credits are required for this model.", Run: "run-123"})
	got := readRateLimitNote()
	if got == nil {
		t.Fatal("note not readable")
	}
	if got.Note != "Usage credits are required for this model." || got.Run != "run-123" {
		t.Errorf("round-trip lost data: %+v", got)
	}
}
