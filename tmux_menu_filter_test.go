package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMenuFilter(t *testing.T) {
	t.Run("editing and preview", func(t *testing.T) {
		m, c := testMenu(t, "%1")
		for _, k := range []string{"/", "n"} {
			m, _ = menuPress(m, k)
		}
		if m.after != nil || !m.filtering || m.filter != "n" {
			t.Fatal("filter letter acted as hotkey")
		}
		if rows := m.tabItems(0); len(rows) != 1 || rows[0] != 1 || m.sel != 1 || c.focused[len(c.focused)-1] != "%2" {
			t.Fatalf("matches or preview: %v %v", rows, c.focused)
		}
		if strings.Contains(m.View().Content, "ACR POS") {
			t.Fatal("non-match visible")
		}
		nm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = nm.(menuModel)
		if m.filter != "" || len(m.tabItems(0)) != 2 {
			t.Fatal("backspace did not restore rows")
		}
		m, _ = menuPress(m, "é")
		nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = nm.(menuModel)
		if m.filter != "" {
			t.Fatal("backspace split rune")
		}
	})
	t.Run("done and hotkeys", func(t *testing.T) {
		for _, key := range []string{"n", "1", "enter"} {
			m, c := testMenu(t, "%1")
			for _, k := range []string{"/", "party", "enter"} {
				m, _ = menuPress(m, k)
			}
			if m.filtering || len(m.tabItems(0)) != 1 || !strings.Contains(m.View().Content, "/ party") {
				t.Fatal("done lost narrowing")
			}
			m, cmd := menuPress(m, key)
			if cmd == nil {
				t.Fatalf("%s did not act", key)
			}
			runAfter(m)
			if len(c.picked) != 1 {
				t.Fatal("pick missing")
			}
			if key == "1" && c.picked[0] != "ACR POS" {
				t.Fatal("hidden digit did not pick hidden row")
			}
			if key == "enter" && c.picked[0] != "partyline" {
				t.Fatal("second enter did not keep match")
			}
		}
	})
	t.Run("escape", func(t *testing.T) {
		for _, done := range []bool{false, true} {
			m, _ := testMenu(t, "%1")
			for _, k := range []string{"/", "party"} {
				m, _ = menuPress(m, k)
			}
			if done {
				m, _ = menuPress(m, "enter")
			}
			m, cmd := menuPress(m, "esc")
			if cmd != nil || m.filtering || m.filter != "" || len(m.tabItems(0)) != 2 {
				t.Fatal("escape did not clear")
			}
			_, cmd = menuPress(m, "esc")
			if cmd == nil {
				t.Fatal("second escape did not close")
			}
		}
	})
	t.Run("commands engine and no match", func(t *testing.T) {
		m, _ := testMenu(t, "%1")
		m.items[0].engine = "codex"
		for _, k := range []string{"tab", "/", "CoDeX"} {
			m, _ = menuPress(m, k)
		}
		if m.tab != 0 || m.sel != 0 || len(m.tabItems(0)) != 1 {
			t.Fatal("engine filter from Commands failed")
		}
		m, _ = menuPress(m, "zzz")
		if m.sel != -1 || !strings.Contains(m.View().Content, "no session matches") {
			t.Fatal("missing empty state")
		}
		m, _ = menuPress(m, "enter")
		m, cmd := menuPress(m, "enter")
		if cmd != nil {
			t.Fatal("empty selection acted")
		}
		m, _ = menuPress(m, "down")
		if m.sel != -1 {
			t.Fatal("empty navigation selected a row")
		}
	})
}

func TestMenuFilterGolden(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		for _, query := range []string{"party", "missing"} {
			m, _ := testMenu(t, "%1")
			m.w, m.h = size[0], size[1]
			m, _ = menuPress(m, "/")
			m, _ = menuPress(m, query)
			got := gateANSI.ReplaceAllString(m.View().Content, "")
			path := fmt.Sprintf("testdata/menu-filter-%s-%dx%d.golden", query, size[0], size[1])
			if *updateLoopsTab {
				if err := os.WriteFile(path, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != got {
				t.Fatalf("%s changed:\n%s", path, got)
			}
		}
	}
}
