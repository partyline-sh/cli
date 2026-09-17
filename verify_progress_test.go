package main

import (
	"strings"
	"testing"
)

// The live-progress lines are a USER-FACING string, not debug output: they are what a board card
// shows while a gate runs, and the whole reason they exist is that a card which stops changing is
// indistinguishable from a hung one. So they get tested like copy — for being specific, bounded, and
// never claiming something the run did not do.

func TestPipelineEmitIsNilSafe(t *testing.T) {
	// A hand-run `ptln crank` has nowhere to post. Telemetry must never take down a gate.
	pipelineCfg{}.emit("this must not panic %d", 1)
}

func TestPipelineEmitFormatsThroughTheSink(t *testing.T) {
	var got []string
	pc := pipelineCfg{step: func(s string) { got = append(got, s) }}
	pc.emit("🔍 verify — running %d acceptance %s", 3, plural(3, "check", "checks"))
	if len(got) != 1 || got[0] != "🔍 verify — running 3 acceptance checks" {
		t.Fatalf("emit produced %q", got)
	}
	pc.emit("🔍 verify — running %d acceptance %s", 1, plural(1, "check", "checks"))
	if got[1] != "🔍 verify — running 1 acceptance check" {
		t.Errorf("singular not handled: %q", got[1])
	}
}

// A card shows ONE line, so a failure has to lead with something specific. "verify failed" tells a
// reader nothing the colour did not already say.
func TestFirstReasonIsSpecificAndBounded(t *testing.T) {
	long := strings.Repeat("x", 400)
	tests := []struct {
		name string
		in   string
		want func(string) bool
	}{
		{"first line only", "go vet: bad selector\nline two\nline three",
			func(s string) bool { return s == "go vet: bad selector" }},
		{"leading blank lines skipped", "\n\n  npm run build failed\nmore",
			func(s string) bool { return s == "npm run build failed" }},
		{"a compiler dump is truncated, not pasted", long,
			func(s string) bool { return len(s) <= 120 && strings.HasSuffix(s, "…") }},
		{"empty says so rather than showing nothing", "   \n  ",
			func(s string) bool { return s == "rejected without a stated reason" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstReason(tt.in)
			if !tt.want(got) {
				t.Errorf("firstReason(%.40q) = %q", tt.in, got)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("a card line must be single-line, got %q", got)
			}
		})
	}
}

// "Review running" is the state most often mistaken for a hang, so the line has to say WHO is
// judging — knowing a second engine is reading a diff is what makes the wait legible.
func TestReviewerLabelNamesWhoIsJudging(t *testing.T) {
	if got := reviewerLabel("claude", nil); got != "claude" {
		t.Errorf("no lanes should fall back to the build engine, got %q", got)
	}
	// Mirrors runReviewer's own defaulting; if that ever diverges the card starts lying about cost.
	if got := reviewerLabel("", nil); got != "an independent reviewer" {
		t.Errorf("unknown engine = %q, want a readable fallback", got)
	}
	if got := reviewerLabel("claude", []reviewLane{{ID: "primary", Engine: "claude"}, {ID: "second", Engine: "codex"}}); got != "claude + codex" {
		t.Errorf("two lanes = %q, want both named (a second lane is real money)", got)
	}
	// A lane with no engine name must not render an empty or "+"-dangling label.
	got := reviewerLabel("claude", []reviewLane{{ID: "a"}, {ID: "b"}})
	if got == "" || strings.HasPrefix(got, "+") || strings.HasSuffix(got, "+") {
		t.Errorf("unnamed lanes produced a broken label: %q", got)
	}
}
