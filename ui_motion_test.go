package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func motionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("NO_COLOR", "")
	t.Setenv("PARTYLINE_STILL", "")
}
func TestMotionPreferences(t *testing.T) {
	motionEnv(t)
	if p := loadMotionPrefs(); p != (motionPrefs{}) {
		t.Fatalf("defaults: %+v", p)
	}
	if err := saveMotionPrefs(motionPrefs{Still: true, ASCII: true}); err != nil {
		t.Fatal(err)
	}
	if p := loadMotionPrefs(); !p.Still || !p.ASCII {
		t.Fatal("saved preferences lost")
	}
	if err := saveMotionPrefs(motionPrefs{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PARTYLINE_STILL", " 1 ")
	t.Setenv("NO_COLOR", "1")
	if p := loadMotionPrefs(); !p.Still || !p.ASCII {
		t.Fatal("process preferences ignored")
	}
	if p := readMotionPrefs(); p != (motionPrefs{}) {
		t.Fatal("process preferences leaked onto disk")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("LC_ALL", "C")
	if !loadMotionPrefs().ASCII {
		t.Fatal("C locale lacks ASCII signals")
	}
}
func TestMotionStopsAndResumes(t *testing.T) {
	for _, m := range []surfaceMotion{{watching: true}, {watching: true, prefs: motionPrefs{Still: true}, active: true}, {watching: true, hidden: true, active: true}, {watching: true, blurred: true, active: true}} {
		m.handle(uiMotionFrame{})
		if m.frame != 0 || m.schedule(m.active) != nil || m.pending {
			t.Fatalf("static state animates: %+v", m)
		}
	}
	m := surfaceMotion{watching: true}
	if m.schedule(true) == nil || !m.pending {
		t.Fatal("working timer not started")
	}
	if m.schedule(true) != nil {
		t.Fatal("duplicate timer scheduled")
	}
	m.handle(uiMotionFrame{})
	if m.frame != 1 {
		t.Fatal("working frame did not advance")
	}
	m.handle(uiMotionWatch{visible: false})
	m.handle(uiMotionFrame{})
	if m.frame != 1 || m.pending {
		t.Fatal("hidden pane advanced")
	}
	m.handle(uiMotionWatch{visible: true})
	m.watching = true
	if m.schedule(true) == nil {
		t.Fatal("visible pane failed to resume")
	}
}
func TestMotionSettleIsFinite(t *testing.T) {
	m := surfaceMotion{watching: true, settle: 6}
	for i := 0; i < 6; i++ {
		if m.schedule(false) == nil {
			t.Fatal("settle ended early")
		}
		m.handle(uiMotionFrame{})
	}
	if m.settle != 0 || m.schedule(false) != nil {
		t.Fatal("settled state keeps ticking")
	}
}
func TestMotionFramesPreserveCommandsAndDoNotScan(t *testing.T) {
	motionEnv(t)
	m := newMenuModel([]tmuxMenuItem{{label: "codex", status: "active", paneID: "%1"}}, "", menuActions{})
	n, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = n.(menuModel)
	if cmd == nil || !m.motion.pending {
		t.Fatal("scheduled flag lost on model return")
	}
	n, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("quit action was wrapped or lost")
	}
	launcher := launcherPopup{menu: &aiMenu{view: []aiSession{{Status: "active"}}}, scan: func() []aiSession { t.Fatal("frame scanned sessions"); return nil }}
	n, _ = launcher.Update(uiMotionFrame{})
	if n.(launcherPopup).menu.motion.frame != 1 {
		t.Fatal("launcher did not animate")
	}
}
func TestChatSignalDoesNotRewriteMessagesOrDraft(t *testing.T) {
	m := chatPopup{motion: surfaceMotion{frame: 3}}
	if got := m.statusSignal("⠋ codex · working", 0); !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, " codex · working") {
		t.Fatal(got)
	}
	if got := m.statusSignal("· codex · idle", 0); got != "· codex · idle" {
		t.Fatal("idle signal changed meaning")
	}
}
func TestThemeSavesMotionFlags(t *testing.T) {
	motionEnv(t)
	themeMain([]string{"--background", "dark", "--motion", "off", "--glyphs", "ascii"})
	if p := readMotionPrefs(); !p.Still || !p.ASCII {
		t.Fatal("theme did not save presentation flags")
	}
}

func TestIdleMotionDoesNotArmWatch(t *testing.T) {
	m := surfaceMotion{}
	if m.schedule(false) != nil || m.watching || m.pending {
		t.Fatal("idle surface armed a watch or frame")
	}
	m.prefs.Still = true
	if m.schedule(true) != nil || m.watching {
		t.Fatal("reduced motion armed visibility polling")
	}
}
func TestHiddenLauncherPollDoesNotScan(t *testing.T) {
	motionEnv(t)
	reads := 0
	m := launcherPopup{menu: &aiMenu{}, scan: func() []aiSession { reads++; return nil }}
	n, cmd := m.Update(uiDataPoll{visible: false})
	m = n.(launcherPopup)
	if reads != 0 || !m.menu.motion.hidden || cmd == nil {
		t.Fatal("hidden poll scanned or failed to schedule visibility retry")
	}
	_, cmd = m.Update(uiDataPoll{visible: true})
	_ = cmd()
	if reads != 1 {
		t.Fatal("visible poll did not use existing scanner")
	}
}

func TestMotionPreservesImmediatePaletteCycling(t *testing.T) {
	motionEnv(t)
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-motion-theme-no-server")
	m := launcherFixture(t)
	m.menu.motion = newSurfaceMotion()
	before := m.View().BackgroundColor
	next, _ := m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if next.(launcherPopup).View().BackgroundColor == before {
		t.Fatal("cached frame delayed the existing palette shortcut")
	}
}
