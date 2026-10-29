package main

import (
	"strings"
	"testing"
)

// `status` in tmux is the ROW COUNT, and `on` means exactly one row. Writing `set -g status on`
// after `set -g status 2` therefore collapsed the bar back to a single row: the memory row was
// configured as status-format[1], never drawn, and the whole ribbon looked like it had not
// shipped. It survived review because both lines read as "turn the status bar on".
func TestTheConfDoesNotCollapseTheStatusBarToOneRow(t *testing.T) {
	conf := tmuxConf()

	if !strings.Contains(conf, "set -g status 2") {
		t.Fatal("the conf no longer asks for two status rows")
	}
	if !strings.Contains(conf, "set -g status-format[1]") {
		t.Fatal("the second row is not configured")
	}
	for _, line := range strings.Split(conf, "\n") {
		if strings.TrimSpace(line) == "set -g status on" {
			t.Error("`set -g status on` sets ONE row and undoes `status 2` — the memory row stops being drawn")
		}
	}

	// Order matters as much as presence: a later row-count wins.
	rows := strings.Index(conf, "set -g status 2")
	for _, after := range []string{"set -g status off", "set -g status 1"} {
		if i := strings.Index(conf, after); i > rows {
			t.Errorf("%q appears after `status 2` and would override it", after)
		}
	}
}

// A pane in copy-mode swallows input: keystrokes go to the scrollback viewer, not the agent. So
// entering copy-mode must always be able to END by itself. `-e` exits on reaching the bottom, the
// way the wheel binding does; without it a pane entered from the menu stayed in copy-mode until
// someone happened to press Esc, and pasting into the agent silently did nothing while copying
// OUT kept working — which reads as "paste is broken", not "this pane is scrolled up".
func TestEnteringCopyModeCanAlwaysEndByItself(t *testing.T) {
	for _, it := range tmuxMenuItems() {
		if len(it.run) == 0 || it.run[0] != "copy-mode" {
			continue
		}
		var hasE bool
		for _, a := range it.run {
			if a == "-e" {
				hasE = true
			}
		}
		if !hasE {
			t.Errorf("menu item %q enters copy-mode without -e, so the pane stays there and swallows input: %v", it.label, it.run)
		}
	}
}
