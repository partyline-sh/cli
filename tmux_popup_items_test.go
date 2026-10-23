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

// `|` pairs two LIVE sessions; it must never be a plain split-window.
//
// It was `split-window -h`, which opens a fresh shell beside the current pane. So the obvious key
// for "put these two side by side" was the one key that could not do it, and the only key that
// could was labelled "merge". A reporter hit exactly that: pressed | on one session and got a new
// terminal next to it. The man page described | as pairing two sessions too — that text was written
// for the classic mux, where SplitSetup is a ptymux action and panes are ptymux's to own.
func TestSplitKeyJoinsASessionRatherThanSpawningAShell(t *testing.T) {
	var split, unsplit, shell *tmuxMenuItem
	items := tmuxCommandItems("/usr/bin/ptln")
	for i := range items {
		switch items[i].key {
		case "|":
			split = &items[i]
		case "-":
			unsplit = &items[i]
		case "\\":
			shell = &items[i]
		}
	}
	if split == nil {
		t.Fatal("no | row in the menu")
	}
	if !split.merge {
		t.Error("| does not join a session — it is the key people reach for to pair two sessions")
	}
	for _, a := range split.run {
		if a == "split-window" {
			t.Error("| is a plain split-window again; that opens a shell, not the other session")
		}
	}
	if split.alt != "+" {
		t.Error("+ should still fire the same row, so existing muscle memory keeps working")
	}
	if unsplit == nil || !unsplit.brk {
		t.Error("no - row to take a session back out of a split")
	}
	if shell == nil {
		t.Error(`the plain shell pane lost its key; it moved to \ when | was reassigned`)
	}
}

// A shared window holding a session and a plain shell must not render two identical rows.
//
// Reported from a live menu: window 7 held a claude session (%7, @ptln_key set) and a stray zsh
// (%16, no spec) left over from the old `|` binding. Both rows read "ACR POS" under index 7,
// because a spec-less pane inherited the window's name, so there was no way to tell the agent
// from the shell without opening one.
func TestSpecLessPaneInASharedWindowIsNamedForWhatItIs(t *testing.T) {
	rows := []string{
		"%7\t7\tACR POS\t10\t2\tsome-spec\t0\tclaude.exe",
		"%16\t7\tACR POS\t00\t2\t\t0\tzsh",
	}
	kept, perWindow := parseSessionPanes(rows)
	if len(kept) != 2 {
		t.Fatalf("want 2 panes, got %d", len(kept))
	}
	if perWindow["7"] != 2 {
		t.Fatalf("window 7 should count 2 session panes, got %d", perWindow["7"])
	}
	if kept[1].cmd != "zsh" {
		t.Errorf("pane_current_command not carried through: %q", kept[1].cmd)
	}
	if got := shellPaneLabel(kept[1].cmd); got != "shell" {
		t.Errorf("a zsh pane should read as a shell, got %q", got)
	}
	// A pane running something else keeps its own name rather than being flattened to "shell".
	if got := shellPaneLabel("htop"); got != "htop" {
		t.Errorf("want htop, got %q", got)
	}
	if got := shellPaneLabel(""); got != "shell" {
		t.Errorf("an unknown command should still read as a shell, got %q", got)
	}
}
