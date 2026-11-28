package main

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// launcherActionFixture highlights a resumable session and isolates external operations.
func launcherActionFixture(t *testing.T) launcherPopup {
	m := launcherFixture(t)
	oldScan, oldDelete, oldFacts, oldBrowser := launcherMenuScan, launcherMenuDelete, launcherMenuFacts, launcherMenuBrowser
	t.Cleanup(func() {
		launcherMenuScan, launcherMenuDelete, launcherMenuFacts, launcherMenuBrowser = oldScan, oldDelete, oldFacts, oldBrowser
	})
	launcherMenuScan = func() []aiSession { return m.menu.all }
	launcherMenuDelete = func(aiSession, map[string]sessMeta) (string, error) { return "trash", nil }
	launcherMenuFacts = func(aiSession, []aiSession) deleteFacts { return deleteFacts{} }
	launcherMenuBrowser = func(string) {}
	for i := range m.menu.all {
		m.menu.all[i].storePath = "fixture.jsonl"
	}
	m.menu.applyFilter()
	m.menu.toggleCollapse()
	m.menu.move(1)
	return m
}

// TestLauncherActions compares every launcher key with the shared handler and records host requests.
func TestLauncherActions(t *testing.T) {
	keys := []string{"r", "x", "p", "s", " ", "a", "R", "o", "|", "d", "t", "n", "N", "ctrl+n", "D", "?", "q", "ctrl+c", "enter", "esc"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			m := launcherActionFixture(t)
			browser := false
			launcherMenuBrowser = func(string) { browser = true }
			action := ""
			var cmd tea.Cmd
			switch key {
			case "o":
				action = "terminal"
			case "|":
				action = "split"
			case "n":
				action = "new"
			case "N":
				action = "quick-new"
			case "ctrl+n":
				action = "quick-bypass"
			}
			if key == "ctrl+n" || key == "ctrl+c" {
				code := rune('n')
				if key == "ctrl+c" {
					code = 'c'
				}
				next, c := m.Update(tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl})
				m, cmd = next.(launcherPopup), c
			} else {
				m, cmd = launcherPress(m, key)
			}
			if m.action != action {
				t.Fatalf("action %q, want %q", m.action, action)
			}
			a := m.menu
			switch key {
			case "D":
				if !browser {
					t.Fatal("browser")
				}
			case "r":
				if !a.renaming {
					t.Fatal("rename")
				}
			case "x":
				if a.confirmDelete == nil {
					t.Fatal("delete confirm")
				}
			case "p":
				if !a.meta["24"].Pinned {
					t.Fatal("pin")
				}
			case "s":
				if a.sort != sortOldest {
					t.Fatal("sort")
				}
			case " ":
				if len(a.marked) != 1 {
					t.Fatal("mark")
				}
			case "a":
				if !a.showAll {
					t.Fatal("all")
				}
			case "R":
				if a.flash != "✓ refreshed" {
					t.Fatal("refresh")
				}
			case "?":
				if !a.help {
					t.Fatal("help")
				}
			case "enter":
				if !a.picking {
					t.Fatal("picker")
				}
			case "esc":
				if !m.back {
					t.Fatal("back")
				}
			case "q", "ctrl+c":
				// Quit closes the popup; the sessions stay running in tmux.
				if cmd == nil || cmd() != tea.Quit() {
					t.Fatal("quit did not close the popup")
				}
			case "d":
				if cmd == nil {
					t.Fatal("d did not run the diff pager")
				}
			case "t":
				if !strings.HasPrefix(a.flash, "✓ theme: ") {
					t.Fatalf("t did not cycle the theme: %q", a.flash)
				}
			}
		})
	}
	for _, state := range []string{"rename", "delete", "picker", "custom", "danger", "help", "filter"} {
		for _, key := range []string{"up", "down", "left", "right", "h", "l", "enter", "esc", "q", "y", "backspace", "z"} {
			t.Run(state+"/"+key, func(t *testing.T) {
				m := launcherActionFixture(t)
				a := m.menu
				switch state {
				case "rename":
					a.renaming = true
					a.renameBuf = "name"
				case "delete":
					cp := *a.selected()
					a.confirmDelete = &cp
				case "picker", "custom", "danger":
					a.picking = true
					a.pickModes = permissionModes("claude")
					a.pickCustom = state == "custom"
					a.pickText = "--verbose"
					if state == "danger" {
						for i, p := range a.pickModes {
							if p.danger {
								a.pickIdx = i
								break
							}
						}
					}
				case "help":
					a.help = true
				case "filter":
					a.filter = true
				}
				// Delete confirmation uses a temporary HOME and no real session store.
				cp := aiMenu{all: a.all, view: a.view, rows: a.rows, meta: map[string]sessMeta{}, w: a.w, h: a.h, cursor: a.cursor, renaming: a.renaming, renameBuf: a.renameBuf, confirmDelete: a.confirmDelete, picking: a.picking, pickModes: a.pickModes, pickIdx: a.pickIdx, pickCustom: a.pickCustom, pickText: a.pickText, help: a.help, filter: a.filter, query: a.query}
				cp.meta = map[string]sessMeta{}
				b := launcherKeyBytes(launcherTestKey(key))
				done, chosen := cp.handleKey(b)
				m, cmd := launcherPress(m, key)
				if a.renaming != cp.renaming || a.renameBuf != cp.renameBuf || a.picking != cp.picking || a.pickIdx != cp.pickIdx || a.pickArmed != cp.pickArmed || a.pickCustom != cp.pickCustom || a.pickText != cp.pickText || a.help != cp.help || (a.confirmDelete == nil) != (cp.confirmDelete == nil) {
					t.Fatalf("state differs for %s", key)
				}
				if chosen != nil {
					if m.action != "open" || !reflect.DeepEqual(m.picked.resumeArgv, chosen.resumeArgv) {
						t.Fatal("chosen flags")
					}
				}
				if done && cmd == nil {
					t.Fatal("missing exit")
				}
			})
		}
	}
	t.Run("host actions", testLauncherHostActions)
	t.Run("pager", func(t *testing.T) {
		m := launcherActionFixture(t)
		old := launcherExec
		t.Cleanup(func() { launcherExec = old })
		called := false
		launcherExec = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
			called = true
			if cmd.Args[0] != "sh" {
				t.Fatal(cmd.Args)
			}
			return func() tea.Msg { return cb(nil) }
		}
		m, cmd := launcherPress(m, "d")
		if !called || cmd == nil || m.action != "" {
			t.Fatal("pager did not suspend")
		}
	})
}

// launcherTestKey supplies named messages for direct legacy comparisons.
func launcherTestKey(k string) tea.KeyPressMsg {
	named := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "enter": tea.KeyEnter, "esc": tea.KeyEscape, "backspace": tea.KeyBackspace}
	if c, ok := named[k]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

// esc in the detail pane leaves the pane; a second esc goes back.
func TestLauncherActionsEscLeavesTheDetailFirst(t *testing.T) {
	m := launcherActionFixture(t)
	m, _ = launcherPress(m, "tab")
	if !m.menu.focusR {
		t.Fatal("tab did not focus the detail")
	}
	m, cmd := launcherPress(m, "esc")
	if m.menu.focusR || m.back || cmd != nil {
		t.Fatalf("first esc: focus %v back %v", m.menu.focusR, m.back)
	}
	if m, _ = launcherPress(m, "esc"); !m.back {
		t.Fatal("second esc did not go back")
	}
}

// A cancelled recovery reopens the launcher; a restored session opens with meta read afresh.
func TestLauncherActionsRecoverCancelReopens(t *testing.T) {
	oldRecover, oldReopen := launcherRecover, launcherReopen
	t.Cleanup(func() { launcherRecover, launcherReopen = oldRecover, oldReopen })
	reopened := false
	launcherReopen = func() error { reopened = true; return nil }
	launcherRecover = func(aiSession, string, func(aiSession)) {} // cancelled: never resumes
	m := launcherActionFixture(t)
	s := m.menu.view[0]
	m.picked, m.action = &s, "recover"
	if err := m.finish(); err != nil || !reopened {
		t.Fatalf("cancelled recovery: err %v, reopened %v", err, reopened)
	}
}

// A flash is shown as text: an escape sequence in a session title cannot restyle the popup.
func TestLauncherActionsFlashIsText(t *testing.T) {
	m := launcherActionFixture(t)
	m.menu.flash = "✗ \x1b[31mred\x1b[0m title\x07"
	out := m.View().Content
	if strings.Contains(out, "\x1b[31m") || strings.Contains(out, "\x07") {
		t.Fatalf("the flash's escapes reached the screen: %q", out)
	}
	if !strings.Contains(gateANSI.ReplaceAllString(out, ""), "red title") {
		t.Fatal("the flash's text is missing")
	}
}
