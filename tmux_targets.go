package main

// tmuxTargets reads live sessions and displays session-menu banners.

import (
	"strings"
)

type tmuxTargets struct{}

func (tmuxTargets) SetBanner(s string) {
	_ = tmuxCmd("display-message", s).Run()
}

func (tmuxTargets) LiveSessions() []LiveSession {
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{@ptln_key}\t#{window_name}\t#{@ptln_spec}").Output()
	if err != nil {
		return nil
	}
	var live []LiveSession
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 || f[0] == "" || isFixtureKey(f[0]) {
			continue
		}
		label := f[1]
		if sp, ok := decodePaneSpec(f[2]); ok && sp.Label != "" {
			label = sp.Label
		}
		live = append(live, LiveSession{Key: f[0], Label: label})
	}
	return live
}

// tmuxPane is sessIO over one pane.
type tmuxPane struct{ id string }

// WriteInput sends raw bytes — send-keys -l passes them uninterpreted, so the bracketed-paste
// fence and the submit CR arrive exactly as the built-in mux would have written them.
func (p tmuxPane) WriteInput(b []byte) {
	_ = tmuxCmd("send-keys", "-t", p.id, "-l", "--", string(b)).Run()
}

func (p tmuxPane) Snapshot() []byte {
	out, err := tmuxCmd("capture-pane", "-p", "-t", p.id).Output()
	if err != nil {
		return nil
	}
	return out
}

func (p tmuxPane) SnapshotHistory(maxLines, _ int) []byte {
	out, err := tmuxCmd("capture-pane", "-p", "-t", p.id, "-S", "-"+itoa(maxLines)).Output()
	if err != nil {
		return nil
	}
	return out
}
