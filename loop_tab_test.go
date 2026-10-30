package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	ansix "github.com/charmbracelet/x/ansi"

	uitheme "partyline.sh/partyline/internal/theme"
)

var updateLoopsTab = flag.Bool("update-loops-tab", false, "rewrite UI golden screens")

// Every run state lands in a section, and the right one; a three-step batch shows one step done,
// one in review and one planned. The run states are read from loop_run.go, so a state added there
// without a section fails this test.
func TestLoopsTab(t *testing.T) {
	src, err := os.ReadFile("loop_run.go")
	if err != nil {
		t.Fatal(err)
	}
	block := string(src)[strings.Index(string(src), "// Run states."):]
	block = block[:strings.Index(block, ")")]
	states := map[string]string{}
	for _, m := range regexp.MustCompile(`(loop[A-Z]\w*)\s*=\s*"([a-z_]+)"`).FindAllStringSubmatch(block, -1) {
		if m[1] != "loopRoundTask" { // a task name, not a run state
			states[m[1]] = m[2]
		}
	}
	if len(states) < 10 {
		t.Fatalf("read only %d run states from loop_run.go", len(states))
	}
	want := map[string]tabSection{
		loopAwaiting: tabNeedsYou, loopAwaitYou: tabNeedsYou,
		loopBaseline: tabInReview, loopChecking: tabInReview, loopBatchRev: tabInReview, loopScopeRev: tabInReview, loopPaused: tabInReview,
		loopBuilding: tabInProgress, loopRevising: tabInProgress,
		loopPRReady: tabDone, loopFailed: tabDone, loopStopped: tabDone,
	}
	for name, state := range states {
		sec, ok := tabSectionOf(state)
		if !ok {
			t.Errorf("%s (%q) has no section on the Loops tab", name, state)
			continue
		}
		if w, known := want[state]; known && w != sec {
			t.Errorf("%s lands in %s, want %s", state, tabSectionNames[sec], tabSectionNames[w])
		}
	}

	// A three-step batch: step 1 done, step 2 in review, step 3 planned.
	_, b, r := approvedBatch(t)
	_ = r.set(loopPRReady, "passed", "pr_ready", nil)
	next, err := batchNextStep(r)
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := loadLoopRun(next)
	r2.Round = 1
	_ = r2.set(loopChecking, "round 1: checks, then review", "ready", nil)
	rows := tabRows(time.Now())
	got := map[string]tabSection{}
	for _, row := range rows {
		got[row.Title+"|"+row.Batch] = row.Section
	}
	for key, sec := range map[string]tabSection{"One|step 1 of 3": tabDone, "Two|step 2 of 3": tabInReview, "Three|step 3 of 3": tabPlanned} {
		if g, ok := got[key]; !ok || g != sec {
			t.Errorf("%s: section %v (listed %v), want %s\nrows: %+v", key, g, ok, tabSectionNames[sec], rows)
		}
	}
	if !strings.Contains(tabDetailText(rows[0]), "batch "+b.ID) && !strings.Contains(tabDetailText(rowFor(rows, r2.ID)), "batch "+b.ID) {
		t.Error("a batch step's details do not include the batch summary")
	}
}

func rowFor(rows []tabRow, id string) tabRow {
	for _, r := range rows {
		if r.RunID == id {
			return r
		}
	}
	return tabRow{}
}

// sampleTabRows is a fixed board: one row in each section, for keys and goldens.
func sampleTabRows(time.Time) []tabRow {
	return []tabRow{
		{Section: tabNeedsYou, RunID: "a1b2c3", Repo: "partyline", Title: "Batch of 15 steps", State: loopAwaiting, Detail: "batch of 15 steps waiting for approval", Age: 2 * time.Minute},
		{Section: tabInReview, RunID: "d4e5f6", Repo: "partyline", Title: "the watcher", State: loopChecking, Batch: "step 2 of 15", Detail: "checks done · claude reading · codex done", Age: 8 * time.Minute,
			Chips: []tabChip{{"checks", "done"}, {"claude", "reading"}, {"codex", "done"}}},
		{Section: tabInProgress, RunID: "0a0b0c", Repo: "secfix", Title: "Approve with Touch ID on a Mac", State: loopBuilding, Detail: "round 1 of 6 · round 1: 2 open P1", Age: 4 * time.Minute, Round: 2, OpenP1: 2},
		{Section: tabPlanned, Repo: "partyline", Title: "chat as a popup, with pin-as-tab", Batch: "step 4 of 15", Detail: "batch 4cf83d"},
		{Section: tabDone, RunID: "f70a83", Repo: "touchid", Title: "the Loops tab", State: loopPRReady, Batch: "step 1 of 15", Detail: "no new failures, no open P1", Age: 3 * time.Hour, PR: 1441, CI: "pass"},
	}
}

func fakeTab(t *testing.T) (tabModel, *int) {
	t.Helper()
	opened := 0
	m := newTabModel(tabActions{rows: sampleTabRows, detail: func(r tabRow) string { return "details of " + r.Title },
		now: time.Now, gateRun: func(tabRow) error { opened++; return nil }})
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return nm.(tabModel), &opened
}

func tabKey(m tabModel, k string) tabModel {
	msg := tea.KeyPressMsg{Text: k, Code: rune(k[0])}
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	}
	nm, _ := m.Update(msg)
	return nm.(tabModel)
}

func TestLoopsPopupBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"left", "h", "right", "l", "up", "k", "down", "j", "enter", "a", "s", "esc", "q", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			m, _ := fakeTab(t)
			m.rows = append(m.rows, tabRow{Section: tabNeedsYou, RunID: "second"})
			if key == "up" || key == "k" {
				m.positions[tabNeedsYou] = 1
				m.selectCard()
			}
			msg := launcherKey(key)
			if key == "right" {
				msg = tea.KeyPressMsg{Code: tea.KeyRight}
			}
			next, cmd := m.Update(msg)
			m = next.(tabModel)
			switch key {
			case "left", "h":
				if m.col != tabDone {
					t.Fatal(m.col)
				}
			case "right", "l":
				if m.col != tabPlanned {
					t.Fatal(m.col)
				}
			case "up", "k":
				if m.sel != 0 {
					t.Fatal("up did not move")
				}
			case "down", "j":
				if m.sel != 5 {
					t.Fatal("down did not move within column")
				}
			case "enter":
				if !m.detail {
					t.Fatal("no detail")
				}
				m = tabKey(m, "esc")
				if m.detail {
					t.Fatal("no back")
				}
			case "a", "s":
				if m.after == nil || cmd == nil {
					t.Fatal("no after-exit gate")
				}
			case "esc", "q", "ctrl+c":
				if cmd == nil {
					t.Fatal("no quit")
				}
			}
		})
	}
	m, _ := fakeTab(t)
	m.rows = nil
	for i := 0; i < 20; i++ {
		m.rows = append(m.rows, tabRow{Section: tabNeedsYou, RunID: fmt.Sprint(i), Title: fmt.Sprint(i)})
	}
	for i := 0; i < 19; i++ {
		m = tabKey(m, "j")
	}
	if m.sel != 19 || !strings.Contains(gateANSI.ReplaceAllString(m.View().Content, ""), "‼ 19") {
		t.Fatal("scroll did not follow selection")
	}
	m, _ = fakeTab(t)
	m.w = 80
	m = tabKey(tabKey(m, "l"), "l")
	if start, count := m.visibleColumns(); start != 1 || count != 3 {
		t.Fatal(start, count)
	}
	out := gateANSI.ReplaceAllString(m.View().Content, "")
	for _, name := range []string{"IN REVIEW", "IN PROGRESS", "PLANNED"} {
		if !strings.Contains(out, name) {
			t.Fatal(out)
		}
	}
	for _, name := range []string{"NEEDS YOU (", "DONE ("} {
		if strings.Contains(out, name) {
			t.Fatal(out)
		}
	}
	m.w = 120
	if _, count := m.visibleColumns(); count != 5 {
		t.Fatal(count)
	}
	m, _ = fakeTab(t)
	m = tabKey(tabKey(m, "l"), "l") // past Planned to In progress: an active run
	m = tabKey(m, "a")
	if m.after != nil {
		t.Fatal("approve nonwaiting")
	}
	m = tabKey(m, "s")
	if m.after == nil {
		t.Fatal("stop active")
	}
	t.Run("detail/approve", func(t *testing.T) {
		m, _ := fakeTab(t) // the first card needs you
		m = tabKey(tabKey(m, "enter"), "a")
		if m.after == nil || m.after.RunID != "a1b2c3" {
			t.Fatal("approve from the open card did not hand off to the gate")
		}
	})
	for _, key := range []string{"up", "k", "down", "j", "pgup", "pgdown"} {
		t.Run("detail/"+key, func(t *testing.T) {
			m, _ := fakeTab(t)
			m.act.detail = func(tabRow) string { return strings.Repeat("detail line\n", 100) }
			m = tabKey(m, "enter")
			m.vp.SetYOffset(30)
			before := m.vp.YOffset()
			msg := launcherKey(key)
			switch key {
			case "pgup":
				msg = tea.KeyPressMsg{Code: tea.KeyPgUp}
			case "pgdown":
				msg = tea.KeyPressMsg{Code: tea.KeyPgDown}
			}
			next, _ := m.Update(msg)
			m = next.(tabModel)
			if key == "up" || key == "k" || key == "pgup" {
				if m.vp.YOffset() >= before {
					t.Fatal("did not scroll up")
				}
			} else if m.vp.YOffset() <= before {
				t.Fatal("did not scroll down")
			}
		})
	}

}

// The popup board at launcher sizes and a narrow client, in both themes.
func TestLoopsTabGolden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, theme := range []string{"Midnight", "Daylight"} {
		useTheme(t, theme)
		for _, size := range [][2]int{{144, 45}, {120, 38}, {80, 24}} {
			m, _ := fakeTab(t)
			nm, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			raw := nm.(tabModel).View().Content
			if strings.Count(raw, "\n")+1 > size[1] {
				t.Fatal("board too tall")
			}
			for _, line := range strings.Split(raw, "\n") {
				if ansix.StringWidth(line) > size[0] {
					t.Fatal("board too wide")
				}
			}
			got := gateANSI.ReplaceAllString(raw, "")
			got = launcherGoldenClock.ReplaceAllStringFunc(got, func(s string) string { return strings.Repeat("·", len([]rune(s))) })
			golden := fmt.Sprintf("testdata/loops-popup-%s-%dx%d.golden", theme, size[0], size[1])
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
		}
	}
	useTheme(t, "Midnight")
	dark, _ := fakeTab(t)
	darkBg := dark.View().BackgroundColor // drawn now: the view reads the theme when it renders
	_ = saveBackground(themeBackground{Choice: "light"})
	light, _ := fakeTab(t)
	if darkBg == light.View().BackgroundColor {
		t.Error("Partyline light draws the board on the same background as dark")
	}
	if dg, _ := tabGlyph(uitheme.Default(), sampleTabRows(time.Now())[0]); dg != uitheme.NeedsYou.Glyph() {
		t.Errorf("needs-you glyph %q", dg)
	}
}

// The empty tab's welcome fits the window with the board: no line past the bottom at 80x24.
func TestTabEmptyWelcomeFitsTheWindow(t *testing.T) {
	m, _ := fakeTab(t)
	m.rows = nil
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	out := nm.(tabModel).View().Content
	if n := strings.Count(out, "\n") + 1; n > 24 {
		t.Fatalf("the empty Loops tab is %d lines in a 24-line window", n)
	}
}
