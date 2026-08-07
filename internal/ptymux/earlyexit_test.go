package ptymux

import (
	"strings"
	"testing"
)

// The bug this fixes, in full: `ptln llms resume <id>` on a session Claude still considers live
// prints "Session … is currently running as a background agent (bg). Use `claude agents` to find
// and attach to it" and exits in under a second. The mux removed the tab, discarded the output, and
// showed nothing. The human saw a tab blink shut and concluded an hour of work had been destroyed.
//
// earlyExitReason is where that message is recovered, so this is where it gets tested — against
// real captured terminal output, not a paraphrase.

// The actual bytes, escapes and all, from the failing resume.
const realResumeFailure = "\x1b[?25l\x1b[>4;2m\x1b[>1u\x1b[<u" +
	"Session 71c186df-c22e-4b01-9d22-87ed70402460 is currently running as a background agent (bg). " +
	"Use `claude agents` to find and attach to it, or add --fork-session to branch off a copy.\n" +
	"\x1b[>4m\x1b[<u\x1b]0;done\x07"

func TestEarlyExitReasonRecoversTheRealMessage(t *testing.T) {
	got := earlyExitReason([]byte(realResumeFailure))
	if !strings.Contains(got, "running as a background agent") {
		t.Fatalf("lost the only explanation the human would ever get:\n%q", got)
	}
	// Escapes must not survive into the status bar, or the message reads as gibberish.
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequences leaked into the banner:\n%q", got)
	}
	// The actionable half matters as much as the diagnosis.
	if !strings.Contains(got, "claude agents") {
		t.Errorf("dropped the instruction that resolves it:\n%q", got)
	}
}

func TestEarlyExitReasonPicksTheLastRealLine(t *testing.T) {
	// Boot noise first, the reason last — searching forwards would return the version banner and
	// bury the thing that actually explains the exit.
	screen := "partyline 0.31.5\n\nloading…\n\nfatal: could not read config: permission denied\n"
	got := earlyExitReason([]byte(screen))
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("returned boot noise instead of the failure: %q", got)
	}
}

func TestEarlyExitReasonIgnoresNoise(t *testing.T) {
	cases := []struct{ name, screen string }{
		// A bare prompt is not an explanation; banner-ing it would be worse than silence because it
		// looks like the system said something.
		{"shell prompt", "$ \n"},
		{"agent prompt", "> \n"},
		{"blank", "\n\n   \n\t\n"},
		{"only escapes", "\x1b[2J\x1b[H\x1b[?25l"},
		{"too short to be a sentence", "ok\nx\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := earlyExitReason([]byte(c.screen)); got != "" {
				t.Errorf("banner-ed noise: %q", got)
			}
		})
	}
}

func TestEarlyExitReasonIsBounded(t *testing.T) {
	// A runaway line must not blow out the status bar — it renders on one row.
	got := earlyExitReason([]byte(strings.Repeat("x", 5000)))
	if len(got) > 180 {
		t.Fatalf("unbounded: %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated without saying so")
	}
}
