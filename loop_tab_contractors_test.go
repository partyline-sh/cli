package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

func TestLoopsContractorsTab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubContractorTmux(t)
	oldKill := killContractorSession
	killContractorSession = func(int) {}
	t.Cleanup(func() { killContractorSession = oldKill })
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	all := []*contractor{
		{ID: "11111111111111", Loop: "abcdef", Role: "builder", Engine: "codex", Title: "Build exporter", State: "running", Started: now.Add(-time.Minute), Round: 2},
		{ID: "22222222222222", Loop: "abcdef", Role: "reviewer", Engine: "claude", Title: "Review exporter", State: "done", Started: now.Add(-time.Hour), Ended: now.Add(-30 * time.Minute)},
		{ID: "33333333333333", Loop: "abcdef", Role: "arbiter", Engine: "codex", Title: "Decide findings", State: "failed", Seen: true, Ended: now.Add(-time.Minute)},
		{ID: "44444444444444", Loop: "abcdef", State: "done", Seen: true, Ended: now.Add(-25 * time.Hour)},
		{ID: "55555555555555", State: "running"},
		{ID: "66666666666666", Loop: "abcdef", Engine: "claude", Title: "Earlier review", Role: "reviewer", State: "done", Ended: now.Add(-48 * time.Hour)},
	}
	for _, c := range all {
		c.Window = "@live"
		if err := c.save(); err != nil {
			t.Fatal(err)
		}
	}
	output := "live output\n" + strings.Repeat("line\n", 80) + "tail one"
	old := contractorOutput
	contractorOutput = func(c *contractor) string { return c.ID + "\n" + output }
	t.Cleanup(func() { contractorOutput = old })
	m := newTabModel(tabActions{rows: func(time.Time) []tabRow { return nil }, now: func() time.Time { return now }})
	apply := func(msg tea.Msg) { next, _ := m.Update(msg); m = next.(tabModel) }
	apply(tea.WindowSizeMsg{Width: 120, Height: 38})
	apply(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.contractors || len(m.crew) != 4 {
		t.Fatalf("tab/order: %+v", m.crew)
	}
	for i, id := range []string{all[0].ID, all[1].ID, all[5].ID, all[2].ID} {
		if m.crew[i].ID != id {
			t.Fatal("wrong order", i)
		}
	}
	if !strings.Contains(m.View().Content, "tail one") {
		t.Fatal("preview missing tail")
	}
	output += "\ntail two"
	apply(tabTick{})
	if !strings.Contains(m.View().Content, "tail two") {
		t.Fatal("preview did not follow")
	}
	apply(tea.KeyPressMsg{Code: tea.KeyPgUp})
	offset := m.preview.vp.YOffset()
	output += "\ntail three"
	apply(tabTick{})
	if m.preview.tail || m.preview.vp.YOffset() != offset {
		t.Fatal("scroll position lost")
	}
	apply(launcherKey("G"))
	if !m.preview.tail {
		t.Fatal("follow not restored")
	}
	apply(launcherKey("j"))
	apply(launcherKey("enter"))
	if !strings.Contains(m.View().Content, "Review exporter") {
		t.Fatal("full output missing header")
	}
	if !m.full {
		t.Fatal("no full view")
	}
	c, err := loadContractor(all[1].ID)
	if err != nil || !c.Seen {
		t.Fatal("not marked seen", err)
	}
	apply(launcherKey("esc"))
	if m.full || !m.contractors {
		t.Fatal("no return")
	}
	apply(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.contractors {
		t.Fatal("no board")
	}
	apply(tea.KeyPressMsg{Code: tea.KeyTab})
	m.crewSel = 0
	m.preview = nil
	m.refreshCrew()
	for _, theme := range []string{"Midnight", "Daylight"} {
		useTheme(t, theme)
		for _, size := range [][2]int{{80, 24}, {120, 38}, {144, 45}} {
			apply(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			raw := m.View().Content
			if size[0] < 100 && strings.Contains(raw, "tail three") {
				t.Fatal("narrow preview visible")
			}
			if strings.Count(raw, "\n")+1 > size[1] {
				t.Fatal("too tall")
			}
			for _, line := range strings.Split(raw, "\n") {
				if ansix.StringWidth(line) > size[0] {
					t.Fatal("too wide")
				}
			}
			got := ansix.Strip(raw)
			path := fmt.Sprintf("testdata/loops-contractors-%s-%dx%d.golden", theme, size[0], size[1])
			if *updateLoopsTab {
				if err := os.WriteFile(path, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil || string(want) != got {
				t.Fatalf("%s differs: %v", path, err)
			}
		}
	}
}
