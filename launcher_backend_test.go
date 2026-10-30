package main

import (
	"strings"
	"testing"
)

func TestLauncherEmptyUsesTmuxEntry(t *testing.T) {
	t.Setenv("TMUX", "")
	if requireTmux() != nil {
		t.Skip("usable tmux not installed")
	}
	var events []string
	oldBoot, oldPrep, oldAttach, oldWake := newBootDisplay, tmuxBarePrepare, tmuxBareAttach, tmuxBareWake
	t.Cleanup(func() {
		newBootDisplay, tmuxBarePrepare, tmuxBareAttach, tmuxBareWake = oldBoot, oldPrep, oldAttach, oldWake
	})
	newBootDisplay = func() bootReporter { return fixtureBoot{&events} }
	tmuxBareWake = func() { events = append(events, "wake") }
	tmuxBarePrepare = func() error { events = append(events, "ensure"); return nil }
	tmuxBareAttach = func() error { events = append(events, "attach"); return nil }
	if err := runLLMSApp(nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "wake,paint,ensure,leave-alt,attach" {
		t.Fatal(got)
	}
}
