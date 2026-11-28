package main

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func dockCommand(t *testing.T, args ...string) string {
	t.Helper()
	b, err := tmuxCmd(args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}
func dockFixture(t *testing.T, socket string, width string) string {
	t.Helper()
	tmuxTestServer(t, socket)
	pane := openTagged(t, Spec{Label: "agent", Key: "original-agent", Argv: []string{"sleep", "300"}})
	dockCommand(t, "resize-window", "-t", windowOf(t, pane), "-x", width, "-y", "30")
	return pane
}
func TestChatDockIsOptInAndIdempotent(t *testing.T) {
	pane := dockFixture(t, "ptln-test-chat-dock", "180")
	win := windowOf(t, pane)
	before := dockCommand(t, "display-message", "-p", "-t", pane, "#{window_layout}")
	created, err := dockChatPane(pane, []string{"sleep", "300"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := dockChatPane(pane, []string{"sleep", "300"})
	if err != nil || again != created {
		t.Fatalf("duplicate dock: %s %v", again, err)
	}
	if count := len(strings.Fields(dockCommand(t, "list-panes", "-t", win, "-F", "#{pane_id}"))); count != 2 {
		t.Fatalf("created %d panes", count)
	}
	if sp, ok := paneSpec(pane); !ok || sp.Key != "original-agent" {
		t.Fatal("agent identity changed")
	}
	if got := dockCommand(t, "show-option", "-wqv", "-t", win, chatDockBaseLayout); got != before {
		t.Fatal("pre-dock layout lost")
	}
	if got := tmuxMenuItems(); len(got) == 0 {
		t.Fatal("session menu lost its agent")
	}
	rows := tmuxMenuItems()
	sessions := 0
	for _, r := range rows {
		if r.paneID != "" {
			sessions++
			if r.paneID == created {
				t.Fatal("chat masquerades as an agent")
			}
		}
	}
	if sessions != 1 {
		t.Fatalf("session rows: %d", sessions)
	}
}
func TestChatDockGuardsPhoneAndSmallPanes(t *testing.T) {
	pane := dockFixture(t, "ptln-test-chat-dock-guards", "80")
	if _, err := dockChatPane(pane, []string{"sleep", "300"}); err == nil {
		t.Fatal("small pane docked")
	}
	dockCommand(t, "resize-window", "-t", windowOf(t, pane), "-x", "180", "-y", "30")
	dockCommand(t, "set-option", "-w", "-t", windowOf(t, pane), remoteResizeRestore, "inherited 180x30")
	if _, err := dockChatPane(pane, []string{"sleep", "300"}); err == nil || !strings.Contains(err.Error(), "Phone layout") {
		t.Fatal("phone layout not protected")
	}
	if got := dockCommand(t, "list-panes", "-t", windowOf(t, pane), "-F", "#{pane_id}"); got != pane {
		t.Fatal("refusal changed the panes")
	}
	if _, err := dockChatPane("%1;bad", []string{"sleep", "300"}); err == nil {
		t.Fatal("invalid target accepted")
	}
}
func TestChatDockWorkspaceKeepsAgentLayout(t *testing.T) {
	a := dockFixture(t, "ptln-test-chat-dock-resume", "320")
	b := openTagged(t, Spec{Label: "second", Key: "second-agent", Argv: []string{"sleep", "300"}})
	dockCommand(t, "join-pane", "-h", "-s", b, "-t", a)
	base := dockCommand(t, "display-message", "-p", "-t", a, "#{window_layout}")
	indexA := dockCommand(t, "display-message", "-p", "-t", a, "#{pane_index}")
	indexB := dockCommand(t, "display-message", "-p", "-t", b, "#{pane_index}")
	if _, err := dockChatPane(a, []string{"sleep", "300"}); err != nil {
		t.Fatal(err)
	}
	if dockCommand(t, "display-message", "-p", "-t", a, "#{pane_index}") != indexA || dockCommand(t, "display-message", "-p", "-t", b, "#{pane_index}") != indexB {
		t.Fatal("docking renumbered existing agent mentions")
	}
	saved := tmuxWorkspaceSpecs()
	if len(saved) != 2 {
		t.Fatalf("saved %d sessions, including dock?", len(saved))
	}
	if saved[0].Layout != base || saved[0].Group == "" || saved[0].Group != saved[1].Group {
		t.Fatal("docking replaced the agent restore layout")
	}
	tmuxKillTestServer(t)
	if _, err := tmuxMerge(saved); err != nil {
		t.Fatal(err)
	}
	resumed := tmuxWorkspaceSpecs()
	if len(resumed) != 2 || resumed[0].Group != resumed[1].Group {
		t.Fatalf("resume lost agent grouping: %+v", resumed)
	}
}

func TestChatDockKeepsWatchedPaneAndNativeResize(t *testing.T) {
	pane := dockFixture(t, "ptln-test-chat-dock-watched", "180")
	t.Setenv("TERM", "xterm-256color")
	attachPrivateTmuxClient(t)
	if !paneWatched(pane) {
		t.Fatal("attached agent is not watched")
	}
	created, err := dockChatPane(pane, []string{"sleep", "300"})
	if err != nil {
		t.Fatal(err)
	}
	if paneWatched(pane) || !paneWatched(created) {
		t.Fatal("docking did not transfer focus to chat")
	}
	before := dockCommand(t, "display-message", "-p", "-t", created, "#{pane_width}")
	dockCommand(t, "resize-pane", "-t", created, "-L", "5")
	if after := dockCommand(t, "display-message", "-p", "-t", created, "#{pane_width}"); after == before {
		t.Fatal("native pane resize did not resize chat")
	}
	dockCommand(t, "select-pane", "-t", pane)
	if !paneWatched(pane) || paneWatched(created) {
		t.Fatal("returning to the agent did not restore watched state")
	}
	dockCommand(t, "kill-pane", "-t", created)
	if sp, ok := paneSpec(pane); !ok || sp.Key != "original-agent" {
		t.Fatal("closing chat changed the agent identity")
	}
}
func TestChatDockShortcutPreservesDraftOnRefusal(t *testing.T) {
	m, _ := testChatPopup(t)
	m = chatType(m, "draft stays")
	m.dock = func() error { return errors.New("not enough space") }
	msg := tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	n, cmd := m.Update(msg)
	m = n.(chatPopup)
	if cmd != nil || m.in.ed.String() != "draft stays" || m.in.hint != "not enough space" {
		t.Fatal("dock refusal lost draft or closed popup")
	}
	called := 0
	m.dock = func() error { called++; return nil }
	n, cmd = m.Update(msg)
	m = n.(chatPopup)
	if called != 1 || cmd == nil || cmd() != tea.Quit() || m.in.ed.String() != "draft stays" {
		t.Fatal("dock did not close cleanly after success")
	}
	m.pinned = true
	_, cmd = m.Update(msg)
	if called != 1 || cmd != nil {
		t.Fatal("pinned chat split another pane")
	}
}
