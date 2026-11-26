package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The toast must be RIGHT-ALIGNED and live in the ribbon. display-message cannot be positioned —
// it takes the whole message line, so it covered either the tab row (navigation someone is
// reading) or the memory row. Neither is a place for "the clipboard write worked".
func TestTheCopyToastIsRightAlignedInTheRibbon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	markCopied()
	got := copyToast()
	if got == "" {
		t.Fatal("no toast immediately after a copy")
	}
	if !strings.Contains(got, "align=right") {
		t.Errorf("the toast is not right-aligned, so it would sit on top of the project name: %q", got)
	}
	if !strings.Contains(got, "copied") {
		t.Errorf("the toast does not say what happened: %q", got)
	}
}

// It clears itself. Nothing sends a second event to take it down, so a toast that did not age out
// would sit in the ribbon until the next copy — permanently claiming the right-hand side.
func TestTheCopyToastAgesOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	markCopied()
	old := time.Now().Add(-copyToastFor - time.Second)
	if err := os.Chtimes(copyToastPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if got := copyToast(); got != "" {
		t.Errorf("a stale toast is still showing: %q", got)
	}
}

// No marker at all is the ordinary state — most of the time nobody has just copied anything.
func TestNoToastWithoutACopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := copyToast(); got != "" {
		t.Errorf("a toast appeared without a copy: %q", got)
	}
}

// The drag must COPY and LEAVE copy-mode. Staying in copy-mode is what kept the highlight lit and
// swallowed the next paste, so the obvious flow — select, copy, paste into the prompt — did
// nothing until you clicked or pressed Escape, with nothing on screen saying why.
// Drag-release copies WITHOUT leaving copy-mode, and a plain click leaves it.
//
// This binding has now been wrong three ways, so the contract is written down rather than
// remembered:
//
//	cancel on PRESS   every drag starts with a press, so selecting in scrollback snapped the
//	                  view to the tail on contact.
//	cancel on RELEASE fixed that and broke reading: releasing the mouse after a selection threw
//	                  away the highlight AND the reading position, mid-stream.
//	never cancel      leaves the pane routing input to the scrollback viewer, where a bracketed
//	                  paste lands as `[200~...01~` garbage.
//
// The seam is that tmux fires MouseUp1 only for a press that did NOT become a drag. So a drag
// keeps your place and your highlight, and a click — the gesture before pasting — goes live.
func TestDragKeepsCopyModeAndClickLeavesIt(t *testing.T) {
	conf := tmuxConf()
	var sawDrag, sawUp bool
	for _, line := range strings.Split(conf, "\n") {
		if strings.Contains(line, "MouseDragEnd1Pane") {
			sawDrag = true
			if strings.Contains(line, "and-cancel") {
				t.Errorf("drag-release cancels copy-mode, which snaps the pane to the tail: %s", line)
			}
			if !strings.Contains(line, "no-clear") {
				t.Errorf("drag-release must keep the selection and the position: %s", line)
			}
			if !strings.Contains(line, "--copied") {
				t.Errorf("drag-release does not confirm the copy: %s", line)
			}
		}
		if strings.Contains(line, "MouseUp1Pane") {
			sawUp = true
			if !strings.Contains(line, "cancel") {
				t.Errorf("a click must leave copy-mode, or a paste lands in the scrollback viewer: %s", line)
			}
		}
	}
	if !sawDrag {
		t.Error("no MouseDragEnd1Pane binding at all")
	}
	if !sawUp {
		t.Error("no MouseUp1Pane binding — without it nothing takes a reader back to live for a paste")
	}
}
