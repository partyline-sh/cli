package main

import "testing"

// Another session's screen shown in tool output (here a Claude status bar a codex captured) must
// not make an idle session look busy; the engine's own working line still counts.
func TestEscToInterruptCountsOnlyAtTheBottom(t *testing.T) {
	idle := "• Ran tmux capture-pane -t %16\n" +
		"  └ ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt\n" +
		"• Done.\n  Worked for 8m 34s\n  ↳ Recap: a\n           b\n           c\n" +
		"› Ask Codex to do anything\n  GPT-6.1-Sol medium · ~/dev/compound\n  ← for agents · ? for shortcuts"
	if got := screenActivity(idle).State; got != "idle" {
		t.Errorf("idle codex with an old capture on screen reads as %q", got)
	}
	working := "• Doing things\n  ⏵⏵ bypass permissions on · esc to interrupt\n❯ "
	if got := screenActivity(working).State; got != "working" {
		t.Errorf("a status line at the bottom reads as %q, want working", got)
	}
	codex := "• lots of output\n• Working (14s • esc to interrupt)\n› Ask Codex to do anything"
	if got := screenActivity(codex).State; got != "working" {
		t.Errorf("codex working line reads as %q", got)
	}
}
