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
func TestDragReleaseCopiesAndLeavesCopyMode(t *testing.T) {
	conf := tmuxConf()
	for _, line := range strings.Split(conf, "\n") {
		if !strings.Contains(line, "MouseDragEnd1Pane") {
			continue
		}
		if strings.Contains(line, "no-clear") {
			t.Errorf("drag-release keeps copy-mode open, which swallows the next paste: %s", line)
		}
		if !strings.Contains(line, "and-cancel") {
			t.Errorf("drag-release does not leave copy-mode: %s", line)
		}
		if !strings.Contains(line, "--copied") {
			t.Errorf("drag-release does not confirm the copy: %s", line)
		}
	}
}
