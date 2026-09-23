package main

import (
	"strings"
	"testing"
)

// THE MENU IS A POPUP, so no menu item may open one directly. display-popup needs a current
// client, and a plain tmux subprocess launched from inside a popup does not have it — the error
// is "no current client", and the menu turns it into a status-line flash nobody reads, so the
// item simply appears to do nothing. Every popup goes out through run-shell -b, which lets the
// menu close and hands the work back to the server with a client in hand.
func TestNoMenuItemOpensAPopupDirectly(t *testing.T) {
	for _, it := range tmuxMenuItems() {
		if len(it.run) == 0 {
			continue
		}
		if it.run[0] == "display-popup" {
			t.Errorf("%q opens a popup from inside the menu, which silently does nothing: %v", it.label, it.run)
		}
		joined := strings.Join(it.run, " ")
		if strings.Contains(joined, "--session-popup") && it.run[0] != "run-shell" {
			t.Errorf("%q reaches a popup without run-shell: %v", it.label, it.run)
		}
	}
}

// Starting a session must ASK. Both entries used to launch whatever quickNewEngine() returned —
// claude unless PARTYLINE_ENGINE was exported before tmux started — so the menu decided silently
// and picking another engine meant leaving the menu, setting an env var and relaunching.
func TestStartingASessionAsksWhichEngine(t *testing.T) {
	want := map[string]bool{"n": false, "N": false}
	for _, it := range tmuxMenuItems() {
		if _, ok := want[it.key]; !ok {
			continue
		}
		joined := strings.Join(it.run, " ")
		if strings.Contains(joined, "--new") && !strings.Contains(joined, "--session-popup") {
			t.Errorf("%q launches without asking: %v", it.label, it.run)
		}
		if strings.Contains(joined, "--session-popup") {
			want[it.key] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("menu key %q does not open a picker", k)
		}
	}
}

// Every popup the menu can reach must have a title, or it opens as an unnamed box.
func TestEveryReachablePopupIsTitled(t *testing.T) {
	for _, it := range tmuxMenuItems() {
		joined := strings.Join(it.run, " ")
		i := strings.Index(joined, "--session-popup ")
		if i < 0 {
			continue
		}
		which := strings.Fields(joined[i+len("--session-popup "):])[0]
		if sessionPopupTitles[which] == "" {
			t.Errorf("%q opens popup %q, which has no title", it.label, which)
		}
	}
}
