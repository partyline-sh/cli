package main

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// launcherFixture supplies two projects, including a missing directory.
func launcherFixture(t *testing.T) launcherPopup {
	t.Helper()
	oldDetails := detailCache
	detailCache = map[string]*aiDetail{}
	t.Cleanup(func() { detailCache = oldDetails })
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	all := []aiSession{}
	for i := 0; i < 25; i++ {
		all = append(all, aiSession{ID: fmt.Sprint(i), Tool: "claude", Cwd: dir, Title: fmt.Sprintf("session %02d", i), LastActive: time.Date(2020, 1, 1, 0, 0, i, 0, time.UTC), resumeArgv: []string{"claude", "--resume", fmt.Sprint(i)}})
	}
	all = append(all, aiSession{ID: "gone", Tool: "codex", Cwd: dir + "/gone", Title: "missing project", resumeArgv: []string{"codex", "resume", "gone"}})
	a := &aiMenu{all: all, meta: map[string]sessMeta{}, w: 80, h: 24}
	a.applyFilter()
	return launcherPopup{menu: a, scan: func() []aiSession { return all }}
}

// launcherPress feeds the same named keys used by Bubble Tea.
func launcherPress(m launcherPopup, k string) (launcherPopup, tea.Cmd) {
	named := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "backspace": tea.KeyBackspace, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown}
	msg := tea.KeyPressMsg{}
	if code, ok := named[k]; ok {
		msg.Code = code
	} else {
		msg.Code = []rune(k)[0]
		msg.Text = k
	}
	next, cmd := m.Update(msg)
	return next.(launcherPopup), cmd
}

// TestLauncherList compares navigation, focus and search directly with the shared legacy handler.
func TestLauncherList(t *testing.T) {
	keys := map[string]string{"up": "\x1b[A", "down": "\x1b[B", "left": "\x1b[D", "right": "\x1b[C", "pgup": "\x1b[5~", "pgdown": "\x1b[6~", "j": "j", "k": "k", "g": "g", "G": "G", "tab": "\t", "l": "l", "h": "h", "/": "/", "backspace": "\x7f", "enter": "\r", "esc": "\x1b", "z": "z"}
	for key, b := range keys {
		for _, state := range []string{"header", "expanded", "list", "detail", "search"} {
			t.Run(key+"/"+state, func(t *testing.T) {
				m := launcherFixture(t)
				a := m.menu
				if state != "header" {
					a.toggleCollapse()
					if state != "expanded" {
						a.move(2)
					}
				}
				if state == "detail" {
					a.focusR = true
					a.dLen = 100
					a.dTop = 3
				}
				if state == "search" {
					a.filter = true
					a.query = "session"
					a.applyFilter()
				}
				cp := aiMenu{all: a.all, view: a.view, rows: a.rows, meta: a.meta, w: a.w, h: a.h, cursor: a.cursor, top: a.top, focusR: a.focusR, dTop: a.dTop, dLen: a.dLen, filter: a.filter, query: a.query}
				cp.collapsed = map[string]bool{}
				for k, v := range a.collapsed {
					cp.collapsed[k] = v
				}
				cp.rows = append([]treeRow{}, a.rows...)
				cp.view = append([]aiSession{}, a.view...)
				if key == "enter" && !a.filter && a.selected() != nil {
					m, cmd := launcherPress(m, key)
					if cmd != nil || !m.menu.picking {
						t.Fatal("permission picker not opened")
					}
					return
				}
				if key == "esc" && !a.filter && a.focusR {
					// handleKey has no esc for the detail pane; the popup's esc leaves it.
					m, cmd := launcherPress(m, key)
					if cmd != nil || m.back || m.menu.focusR {
						t.Fatal("esc in the detail pane did not just leave it")
					}
					return
				}
				if key == "esc" && !a.filter && !a.focusR {
					m, cmd := launcherPress(m, key)
					if cmd == nil || !m.back {
						t.Fatal("escape did not return")
					}
					return
				}
				_, _ = cp.handleKey([]byte(b))
				m, _ = launcherPress(m, key)
				got, want := m.menu, &cp
				if got.cursor != want.cursor || got.focusR != want.focusR || got.dTop != want.dTop || got.query != want.query || got.filter != want.filter || !reflect.DeepEqual(got.rows, want.rows) || !reflect.DeepEqual(got.collapsed, want.collapsed) && len(got.collapsed)+len(want.collapsed) > 0 {
					t.Fatalf("%s differs: cursor %d/%d focus %v/%v detail %d/%d filter %q/%q", key, got.cursor, want.cursor, got.focusR, want.focusR, got.dTop, want.dTop, got.query, want.query)
				}
			})
		}
	}
	t.Run("gone", func(t *testing.T) {
		m := launcherFixture(t)
		m.menu.query = "missing"
		m.menu.applyFilter()
		m.menu.move(1)
		m, cmd := launcherPress(m, "enter")
		if cmd == nil || m.picked == nil || m.picked.ID != "gone" {
			t.Fatal("missing session not picked for recovery")
		}
	})
}

// TestLauncherCache verifies immediate cached construction and lossless private scan fields.
func TestLauncherCache(t *testing.T) {
	m := launcherFixture(t)
	_ = os.MkdirAll(stateDir(), 0o700)
	m.menu.all[0].storePath = "transcript"
	m.menu.all[0].root = "other-home"
	saveLauncherCache(m.menu.all, map[string]bool{m.menu.all[0].ID: true})
	calls := 0
	scan := func() []aiSession { calls++; return m.menu.all }
	cached := newLauncherPopup(scan)
	if calls != 0 || !cached.menu.live[m.menu.all[0].ID] || !reflect.DeepEqual(cached.menu.all, m.menu.all) {
		t.Fatal("cache failed or scanned before paint")
	}
	msg := cached.Init()()
	cached.Update(msg)
	if calls != 1 {
		t.Fatal("no asynchronous refresh")
	}
	_ = os.Remove(launcherCachePath())
	newLauncherPopup(scan)
	if calls != 2 {
		t.Fatal("cache miss did not scan")
	}
}

// launcherGoldenClock matches clock-dependent text: a row's age ("2492d"), the preview's "2492d ago" and "2020-01-01 00:00".
var launcherGoldenClock = regexp.MustCompile(`\b\d+[smhdw]( ago)?\b|\d{4}-\d{2}-\d{2} \d{2}:\d{2}`)

// TestLauncherListGolden records the bounded list in both Partyline variants.
func TestLauncherListGolden(t *testing.T) {
	for _, name := range []string{"Midnight", "Daylight"} {
		for _, size := range [][2]int{{80, 24}, {120, 38}, {60, 20}} {
			t.Run(fmt.Sprintf("%s/%dx%d", name, size[0], size[1]), func(t *testing.T) {
				m := launcherFixture(t)
				useTheme(t, name)
				// Stable project names avoid temporary paths in snapshots.
				for i := range m.menu.all {
					m.menu.all[i].Cwd = "/projects/alpha"
					if m.menu.all[i].ID == "gone" {
						m.menu.all[i].Cwd = "/projects/beta"
					}
				}
				m.menu.applyFilter()
				m.menu.toggleCollapse()
				m.menu.w, m.menu.h = size[0], size[1]
				if size[0] == 120 {
					m.menu.move(1) // one size with a session highlighted, so its preview is recorded
				}
				raw := m.View().Content
				got := gateANSI.ReplaceAllString(raw, "")
				// The preview gives ages and times relative to now; the golden must not change daily.
				got = launcherGoldenClock.ReplaceAllStringFunc(got, func(s string) string { return strings.Repeat("·", len([]rune(s))) })
				path := fmt.Sprintf("testdata/launcher-popup-%dx%d-%s.golden", size[0], size[1], strings.ToLower(name))
				if *updateLoopsTab {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Fatalf("%s changed:\n%s", path, got)
				}
				if strings.Count(got, "\n")+1 > size[1] {
					t.Fatal("too tall")
				}
				st := gateStyle{tmuxTheme()}
				for _, entry := range []struct{ colour, code string }{{st.th.Raised, "48"}, {st.th.Focus, "38"}} {
					var r, g, b int
					fmt.Sscanf(strings.TrimPrefix(entry.colour, "#"), "%02x%02x%02x", &r, &g, &b)
					if !strings.Contains(raw, fmt.Sprintf("%s;2;%d;%d;%d", entry.code, r, g, b)) {
						t.Fatalf("missing theme colour %s", entry.colour)
					}
				}
			})
		}
	}
}
