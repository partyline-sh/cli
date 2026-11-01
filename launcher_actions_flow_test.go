package main

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestLauncherActionsFlows covers multi-key requests through Update and terminal-restored dispatch.
func TestLauncherActionsFlows(t *testing.T) {
	oldMerge, oldFocus, oldOpen, oldQuick, oldNew, oldSplit := launcherMerge, launcherFocus, launcherOpen, launcherQuickNew, launcherNewMenu, launcherSplit
	oldRecover := launcherRecover
	t.Cleanup(func() {
		launcherMerge, launcherFocus, launcherOpen, launcherQuickNew, launcherNewMenu, launcherSplit = oldMerge, oldFocus, oldOpen, oldQuick, oldNew, oldSplit
		launcherRecover = oldRecover
	})
	calls := 0
	launcherMerge = func(specs []Spec) (string, error) {
		calls++
		if len(specs) == 0 || specs[0].Key == "" || len(specs[0].Argv) == 0 {
			t.Fatal("missing saved spec")
		}
		return "%1", nil
	}
	launcherFocus = func(string) error { return nil }
	launcherOpen = func(aiSession, map[string]sessMeta) error { calls++; return nil }
	launcherQuickNew = func(bool) error { calls++; return nil }
	launcherNewMenu = func() { calls++ }
	launcherSplit = func([]aiSession, map[string]sessMeta) error { calls++; return nil }
	launcherRecover = func(s aiSession, _ string, f func(aiSession)) { f(s) }
	for _, tc := range []struct {
		name   string
		keys   []string
		action string
	}{
		{"open", []string{"enter", "enter"}, "open"},
		{"terminal", []string{"o"}, "terminal"},
		{"new", []string{"n"}, "new"},
		{"quick", []string{"N"}, "quick-new"},
		{"marked", []string{" ", " ", "enter"}, "open-many"},
		{"split", []string{" ", " ", "|"}, "split"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := launcherActionFixture(t)
			calls = 0
			var cmd tea.Cmd
			for _, k := range tc.keys {
				m, cmd = launcherPress(m, k)
			}
			if calls != 0 || m.action != tc.action || cmd == nil {
				t.Fatalf("before exit: calls %d action %q", calls, m.action)
			}
			if err := m.finish(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("after exit: %d calls", calls)
			}
		})
	}
	t.Run("danger requires two enters", func(t *testing.T) {
		m := launcherActionFixture(t)
		m, _ = launcherPress(m, "enter")
		for i, mode := range m.menu.pickModes {
			if mode.danger {
				m.menu.pickIdx = i
				break
			}
		}
		m, cmd := launcherPress(m, "enter")
		if cmd != nil || !m.menu.pickArmed {
			t.Fatal("first enter must arm")
		}
		want := m.menu.resumeWith(m.menu.pickModes[m.menu.pickIdx].flags)
		m, cmd = launcherPress(m, "enter")
		if cmd == nil || !reflect.DeepEqual(m.picked.resumeArgv, want.resumeArgv) {
			t.Fatal("second enter must open")
		}
	})
	t.Run("rename saves", func(t *testing.T) {
		m := launcherActionFixture(t)
		id := m.menu.selected().ID
		for _, k := range []string{"r", "new title", "enter"} {
			m, _ = launcherPress(m, k)
		}
		if m.menu.meta[id].Name != "new title" || m.menu.renaming {
			t.Fatal("rename not saved")
		}
	})
	t.Run("recover", func(t *testing.T) {
		m := launcherActionFixture(t)
		m.menu.query = "missing"
		m.menu.applyFilter()
		m.menu.move(1)
		m, cmd := launcherPress(m, "enter")
		if cmd == nil || m.action != "recover" {
			t.Fatal("recover request")
		}
		calls = 0
		if err := m.finish(); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatal("recovery did not focus")
		}
	})
}

// TestLauncherActionsSplit records pane joins and workspace persistence for existing windows.
func TestLauncherActionsSplit(t *testing.T) {
	m := launcherActionFixture(t)
	oldMerge, oldFocus, oldRun, oldWindow, oldSave := launcherMerge, launcherFocus, launcherTmuxRun, launcherWindow, launcherSave
	t.Cleanup(func() {
		launcherMerge, launcherFocus, launcherTmuxRun, launcherWindow, launcherSave = oldMerge, oldFocus, oldRun, oldWindow, oldSave
	})
	next := 0
	launcherMerge = func([]Spec) (string, error) {
		next++
		if next == 1 {
			return "%1", nil
		}
		return "%2", nil
	}
	launcherWindow = func(p string) (string, error) {
		if p == "%1" {
			return "@1", nil
		}
		return "@2", nil
	}
	var commands [][]string
	launcherTmuxRun = func(args ...string) error { commands = append(commands, args); return nil }
	saved, focused := false, false
	launcherSave = func() { saved = true }
	launcherFocus = func(p string) error { focused = p == "%1"; return nil }
	if err := launcherSplitSessions(m.menu.view[:2], m.menu.meta); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || !reflect.DeepEqual(commands[0], []string{"join-pane", "-h", "-s", "%2", "-t", "%1"}) || commands[1][0] != "rename-window" || !saved || !focused {
		t.Fatalf("split commands %v save %v focus %v", commands, saved, focused)
	}
}
