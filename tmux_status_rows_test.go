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
