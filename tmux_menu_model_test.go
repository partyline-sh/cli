package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"partyline.sh/partyline/internal/brand"
)

// sampleMenu is a menu with two sessions and the commands, as tmuxMenuItems composes it.
func sampleMenu() []tmuxMenuItem {
	items := []tmuxMenuItem{
		{label: "ACR POS", key: "1", num: "1", paneID: "%1", status: "waiting"},
		{label: "partyline", key: "2", num: "2", paneID: "%2", status: "active"},
	}
	return append(items, tmuxCommandItems("ptln")...)
}

type menuCalls struct {
	focused []string
	picked  []string
}

func testMenu(t *testing.T, origin string) (menuModel, *menuCalls) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	c := &menuCalls{}
	m := newMenuModel(sampleMenu(), origin, menuActions{
		focus:  func(id string) { c.focused = append(c.focused, id) },
		goBack: func(id string) { c.focused = append(c.focused, id) },
		pick:   func(it tmuxMenuItem) error { c.picked = append(c.picked, it.label); return nil },
	})
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 72, Height: 40})
	return nm.(menuModel), c
}

func menuPress(m menuModel, k string) (menuModel, tea.Cmd) {
	named := map[string]tea.KeyPressMsg{
		"up": {Code: tea.KeyUp}, "down": {Code: tea.KeyDown}, "left": {Code: tea.KeyLeft}, "right": {Code: tea.KeyRight},
		"enter": {Code: tea.KeyEnter}, "esc": {Code: tea.KeyEscape}, "ctrl+c": {Code: 'c', Mod: tea.ModCtrl},
		"tab": {Code: tea.KeyTab}, "shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
		"ctrl+\\": {Code: '\\', Mod: tea.ModCtrl},
	}
	msg, ok := named[k]
	if !ok {
		msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
	}
	nm, cmd := m.Update(msg)
	return nm.(menuModel), cmd
}

// run is what the menu does once it closes: the pick, or going back.
func runAfter(m menuModel) {
	if m.after != nil {
		_ = m.after()
	}
}

// Every menu key keeps its action: arrows move and preview, ⏎ and each row's hotkey pick, an armed
// confirm needs its key twice and any other key stands it down, esc, ctrl-c and ctrl-\ go back to
// where the menu opened.
func TestMenuPopup(t *testing.T) {
	m, c := testMenu(t, "%2")
	if m.items[m.sel].paneID != "%2" {
		t.Fatalf("the selection does not start on the origin: %q", m.items[m.sel].label)
	}
	for _, k := range []string{"up", "left"} {
		m.sel = 1
		m, _ = menuPress(m, k)
		if m.items[m.sel].paneID != "%1" {
			t.Errorf("%s did not move up", k)
		}
	}
	for _, k := range []string{"down", "right"} {
		m.sel = 0
		m, _ = menuPress(m, k)
		if m.items[m.sel].paneID != "%2" {
			t.Errorf("%s did not move down", k)
		}
	}
	if len(c.focused) != 4 || c.focused[0] != "%1" || c.focused[2] != "%2" {
		t.Errorf("moving onto session rows previewed %v", c.focused)
	}
	// Moving wraps and skips separators.
	m.sel = 0
	m, _ = menuPress(m, "up")
	if m.items[m.sel].sep || m.sel != 1 {
		t.Errorf("up from the top went to %d (%q)", m.sel, m.items[m.sel].label)
	}

	// Every command row's hotkey picks it; ⏎ picks the selection.
	for _, it := range tmuxCommandItems("ptln") {
		if it.sep || it.key == "" || it.confirm {
			continue
		}
		for _, k := range []string{it.key, it.alt} {
			if k == "" {
				continue
			}
			m, c := testMenu(t, "%2")
			m, cmd := menuPress(m, k)
			if cmd == nil || cmd() != tea.Quit() {
				t.Errorf("%q did not close the menu", k)
				continue
			}
			runAfter(m)
			if len(c.picked) != 1 || c.picked[0] != it.label {
				t.Errorf("%q picked %v, want %q", k, c.picked, it.label)
			}
		}
	}
	m, c = testMenu(t, "%2")
	m, _ = menuPress(m, "1")
	runAfter(m)
	if len(c.picked) != 1 || c.picked[0] != "ACR POS" {
		t.Errorf("a session's digit picked %v", c.picked)
	}
	m, c = testMenu(t, "%2")
	m, _ = menuPress(m, "enter")
	runAfter(m)
	if len(c.picked) != 1 || c.picked[0] != "partyline" {
		t.Errorf("enter picked %v", c.picked)
	}

	// Armed confirms: x and Q need their key twice; another key stands them down.
	for _, k := range []string{"x", "Q"} {
		m, c := testMenu(t, "%2")
		m, cmd := menuPress(m, k)
		if cmd != nil || m.armed < 0 || !strings.Contains(gateANSI.ReplaceAllString(m.View().Content, ""), "sure? press "+brand.ShiftKey(k)+" again") {
			t.Errorf("%s did not arm", k)
		}
		m, _ = menuPress(m, "z")
		if m.armed != -1 {
			t.Errorf("another key did not stand %s down", k)
		}
		m, _ = menuPress(m, k)
		m, cmd = menuPress(m, k)
		runAfter(m)
		if cmd == nil || len(c.picked) != 1 {
			t.Errorf("%s twice did not act: %v", k, c.picked)
		}
	}

	// esc, ctrl-c and ctrl-\ go back to the origin and pick nothing.
	for _, k := range []string{"esc", "ctrl+c", "ctrl+\\"} {
		m, c := testMenu(t, "%2")
		m.sel = 0
		m, cmd := menuPress(m, k)
		runAfter(m)
		if cmd == nil || len(c.picked) != 0 || len(c.focused) == 0 || c.focused[len(c.focused)-1] != "%2" {
			t.Errorf("%s: picked %v, focused %v", k, c.picked, c.focused)
		}
	}

	// A refused pick says why and acts on nothing.
	m, c = testMenu(t, "%2")
	m.act.refuse = func(it tmuxMenuItem) string {
		if it.confirm {
			return "action refused"
		}
		return ""
	}
	m, _ = menuPress(m, "x")
	if m.armed != -1 || !strings.Contains(m.View().Content, "action refused") {
		t.Errorf("a refused confirm armed or said nothing")
	}
}

// Previews are applied in the order they were asked for: arrowing quickly ends on the last row.
func TestMenuPreviewsLandInOrder(t *testing.T) {
	var mu sync.Mutex
	var got []string
	p := newMenuPreviewer(func(id string) {
		time.Sleep(time.Millisecond) // a slow tmux call
		mu.Lock()
		got = append(got, id)
		mu.Unlock()
	})
	for i := 0; i < 20; i++ {
		p.focus(fmt.Sprintf("%%%d", i))
	}
	p.close()
	if len(got) == 0 || got[len(got)-1] != "%19" {
		t.Fatalf("the last preview applied was %v, want %%19 last", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] && len(got[i]) <= len(got[i-1]) {
			t.Errorf("previews applied out of order: %v", got)
			break
		}
	}
}

// The menu at 80x24, 120x38 and 60x20, plain text.
func TestMenuPopupGolden(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 38}, {60, 20}} {
		m, _ := testMenu(t, "%2")
		nm, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		got := gateANSI.ReplaceAllString(nm.(menuModel).View().Content, "")
		golden := fmt.Sprintf("testdata/menu-popup-%dx%d.golden", size[0], size[1])
		if *updateLoopsTab {
			_ = os.WriteFile(golden, []byte(got), 0o644)
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (generate with -update-loops-tab)", err)
		}
		if got != string(want) {
			t.Errorf("%s changed:\n%s", golden, got)
		}
		if n := strings.Count(got, "\n") + 1; n > size[1] {
			t.Errorf("%dx%d: %d lines, taller than the popup", size[0], size[1], n)
		}
	}
}

// An arrow stands an armed confirm down: x, an arrow onto another session, x arms again rather than
// closing that session; Q, an arrow, Q does not quit.
func TestMenuArrowsDisarm(t *testing.T) {
	for _, k := range []string{"x", "Q"} {
		for _, arrow := range []string{"up", "down", "left", "right"} {
			m, c := testMenu(t, "%2")
			m, _ = menuPress(m, k)
			m, _ = menuPress(m, arrow)
			if m.armed != -1 {
				t.Errorf("%s then %s left it armed", k, arrow)
			}
			m, cmd := menuPress(m, k)
			runAfter(m)
			if cmd != nil || len(c.picked) != 0 {
				t.Errorf("%s, %s, %s acted: %v", k, arrow, k, c.picked)
			}
		}
	}
}

// The menu uses every row tmuxMenuHeight gives it: items, a blank line, the summary and the hint.
func TestMenuFillsItsPopup(t *testing.T) {
	m, _ := testMenu(t, "%2")
	h := tmuxMenuHeight(m.items, 0) - 2 // the popup's border takes two of the rows it is sized to
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 72, Height: h})
	out := gateANSI.ReplaceAllString(nm.(menuModel).View().Content, "")
	if strings.Contains(out, "more") {
		t.Errorf("a popup sized for every item still cut the list:\n%s", out)
	}
	if n := strings.Count(out, "\n") + 1; n > h {
		t.Errorf("%d lines in a %d-line popup", n, h)
	}
	// The shortest popups a small client gets, with the selection at the top, middle and bottom.
	for h := 3; h <= 8; h++ {
		for _, sel := range []int{0, len(m.items) / 2, len(m.items) - 1} {
			mm := m
			mm.sel = sel
			nm, _ := mm.Update(tea.WindowSizeMsg{Width: 72, Height: h})
			out := gateANSI.ReplaceAllString(nm.(menuModel).View().Content, "")
			// From 6 rows the list, both markers, the summary and the hint fit; below that the
			// list still shows one row rather than every row.
			if n := strings.Count(out, "\n") + 1; n > max(h, 7) {
				t.Errorf("height %d, selection %d: %d lines", h, sel, n)
			}
		}
	}
}

func TestMenuTabs(t *testing.T) {
	m, c := testMenu(t, "%2")
	if m.tab != 0 || m.items[m.sel].paneID != "%2" {
		t.Fatal("origin not selected on Sessions")
	}
	m, _ = menuPress(m, "tab")
	if m.tab != 1 || !strings.Contains(m.View().Content, "for #2 partyline") {
		t.Fatal("Commands missing selected session")
	}
	plain := gateANSI.ReplaceAllString(m.View().Content, "")
	last := -1
	for _, heading := range []string{"start and open", "the session's agent", "panes", "leave"} {
		at := strings.Index(plain, heading)
		if at <= last {
			t.Fatalf("heading %q missing or out of order", heading)
		}
		last = at
	}
	for range len(m.items) * 2 {
		m, _ = menuPress(m, "down")
		if m.items[m.sel].sep {
			t.Fatal("selected heading")
		}
	}
	if len(c.focused) != 0 {
		t.Fatal("Commands previewed a session")
	}
	m, _ = menuPress(m, "shift+tab")
	if m.tab != 0 || m.items[m.sel].paneID != "%2" {
		t.Fatal("lost Sessions selection")
	}
	command, calls := testMenu(t, "%2")
	command, _ = menuPress(command, "tab")
	want := command.items[command.sel].label
	command, cmd := menuPress(command, "enter")
	runAfter(command)
	if cmd == nil || len(calls.picked) != 1 || calls.picked[0] != want {
		t.Fatal("Enter did not run selected command")
	}
	for _, tab := range []int{0, 1} {
		for _, it := range sampleMenu() {
			if it.sep || it.key == "" {
				continue
			}
			for _, key := range []string{it.key, it.alt} {
				if key == "" {
					continue
				}
				mm, calls := testMenu(t, "%2")
				if tab == 1 {
					mm, _ = menuPress(mm, "tab")
				}
				mm, cmd := menuPress(mm, key)
				if it.confirm {
					if cmd != nil {
						t.Fatalf("%s acted without confirmation", key)
					}
					mm, cmd = menuPress(mm, key)
				}
				runAfter(mm)
				if cmd == nil || len(calls.picked) != 1 || calls.picked[0] != it.label {
					t.Fatalf("tab %d key %s: %v", tab, key, calls.picked)
				}
			}
		}
	}
	m, c = testMenu(t, "%2")
	m, _ = menuPress(m, "up")
	m, _ = menuPress(m, "tab")
	m, _ = menuPress(m, "x")
	if !strings.Contains(m.View().Content, "for #1 ACR POS") || m.items[m.sessionSel].paneID != "%1" {
		t.Fatal("confirmation lost target")
	}
	m, _ = menuPress(m, "x")
	runAfter(m)
	if len(c.picked) != 1 {
		t.Fatal("close did not act")
	}
	empty := newMenuModel(tmuxCommandItems("ptln"), "", menuActions{})
	if empty.tab != 1 {
		t.Fatal("empty menu did not open Commands")
	}
	empty, _ = menuPress(empty, "tab")
	if !strings.Contains(empty.View().Content, "no sessions yet") {
		t.Fatal("missing empty state")
	}
	empty, emptyCmd := menuPress(empty, "enter")
	if emptyCmd != nil || empty.after != nil {
		t.Fatal("Enter in empty Sessions ran a command")
	}
	for _, tab := range []int{0, 1} {
		mm, _ := testMenu(t, "%2")
		if tab == 1 {
			mm, _ = menuPress(mm, "tab")
		}
		mm.h = tmuxMenuHeight(mm.items, 0) - 2
		view := mm.View().Content
		if strings.Contains(view, "more") || strings.Count(view, "\n")+1 > mm.h {
			t.Fatal("popup does not fit taller tab")
		}
	}
}

func TestMenuTabsCloseSelectedSession(t *testing.T) {
	tmuxTestServer(t, "ptln-test-menu-tabs-close")
	a := openTagged(t, Spec{Label: "selected", Key: "tabs-a", Argv: []string{"sleep", "300"}})
	b := openTagged(t, Spec{Label: "origin", Key: "tabs-b", Argv: []string{"sleep", "300"}})
	if err := tmuxFocus(b); err != nil {
		t.Fatal(err)
	}
	items := append([]tmuxMenuItem{{label: "selected", num: "1", paneID: a}, {label: "origin", num: "2", paneID: b}}, tmuxCommandItems("ptln")...)
	m := newMenuModel(items, b, menuActions{
		focus: func(id string) {
			if err := tmuxFocus(id); err != nil {
				t.Fatal(err)
			}
		},
		pick: func(it tmuxMenuItem) error { return menuPick(it, b) },
	})
	m, _ = menuPress(m, "up")
	m, _ = menuPress(m, "tab")
	m, _ = menuPress(m, "x")
	if !strings.Contains(m.View().Content, "for #1 selected") {
		t.Fatal("close does not name target")
	}
	if _, ok := paneSpec(a); !ok {
		t.Fatal("first press closed selected pane")
	}
	m, cmd := menuPress(m, "x")
	if cmd == nil || m.after == nil {
		t.Fatal("second press did not close menu")
	}
	if err := m.after(); err != nil {
		t.Fatal(err)
	}
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatal(err)
	}
	panes := strings.Fields(string(out))
	if len(panes) != 1 || panes[0] != b {
		t.Fatalf("remaining panes %v, wanted origin %s", panes, b)
	}
}
