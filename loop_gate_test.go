package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

type gateCalls struct{ approved, rejected, decided, stopped []string }

// fakeGate is a popup over real runs on disk with its decisions recorded instead of acted on.
func fakeGate(t *testing.T, runs []*loopRun, allowed error) (gateModel, *gateCalls) {
	t.Helper()
	c := &gateCalls{}
	left := append([]*loopRun(nil), runs...)
	drop := func(r *loopRun) {
		for i, x := range left {
			if x == r {
				left = append(left[:i], left[i+1:]...)
				return
			}
		}
	}
	act := gateActions{
		list:    func() []*loopRun { return left },
		approve: func(r *loopRun) error { c.approved = append(c.approved, r.ID); drop(r); return nil },
		reject:  func(r *loopRun, n string) error { c.rejected = append(c.rejected, r.ID+":"+n); drop(r); return nil },
		stop:    func(r *loopRun) error { c.stopped = append(c.stopped, r.ID); drop(r); return nil },
		decide: func(r *loopRun, finding, d string) error {
			c.decided = append(c.decided, r.ID+":"+finding+":"+d)
			drop(r)
			return nil
		},
		allowed: func() error { return allowed },
		tab:     func(*loopRun) string { return "tab 12 claude" },
		code:    func() string { return "42" },
	}
	m := newGateModel(act)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	// The popup opens on its list; the tests below are about an item's own screen, so they start
	// there. TestGateWaitingList covers the list.
	m = nm.(gateModel)
	if len(m.runs) > 0 {
		m.list = false
	}
	return m, c
}

func gateKeys(m gateModel, keys ...string) gateModel {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "pgdown":
			msg = tea.KeyPressMsg{Code: tea.KeyPgDown}
		case "pgup":
			msg = tea.KeyPressMsg{Code: tea.KeyPgUp}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		nm, _ := m.Update(msg)
		m = nm.(gateModel)
	}
	return m
}

var gateANSI = regexp.MustCompile(`\x1b\[[0-9;:?]*[a-zA-Z]`)

func plain(m gateModel) string { return gateANSI.ReplaceAllString(m.View().Content, "") }

func waitingRun(t *testing.T, state string) *loopRun {
	t.Helper()
	r := testLoopRun(t, t.TempDir())
	r.Checks = []string{"make check-go"}
	r.Review = "claude,codex"
	_ = r.set(state, "", "test", nil)
	return r
}

// Every key in the brief does what it says, and nothing else decides.
func TestGateKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, b := waitingRun(t, loopAwaiting), waitingRun(t, loopAwaiting)

	m, c := fakeGate(t, []*loopRun{a, b}, nil)
	m = gateKeys(m, "x", "j", "k") // unbound and movement keys decide nothing
	if len(c.approved)+len(c.rejected) != 0 {
		t.Fatal("a non-decision key decided")
	}
	m = gateKeys(m, "a", "4", "2")
	if len(c.approved) != 1 || c.approved[0] != a.ID || !strings.Contains(m.result, "approved") {
		t.Errorf("a: approved %v, result %q", c.approved, m.result)
	}
	// A decision says so, large, and that one more waits; esc goes back to the list at once.
	if v := plain(m); m.mode != gateDone || !strings.Contains(v, "✓ Approved") || !strings.Contains(v, "1 more waiting") {
		t.Errorf("after approving:\n%s", v)
	}
	m = gateKeys(m, "esc")
	m = gateKeys(m, "r", "t", "o", "o", " ", "b", "i", "g", "enter", "4", "2") // rejecting takes the code too
	if len(c.rejected) != 1 || c.rejected[0] != b.ID+":too big" {
		t.Errorf("r: rejected %v", c.rejected)
	}
	if v := plain(m); !strings.Contains(v, "✓ Rejected") || !strings.Contains(v, "closes in 3 seconds") {
		t.Errorf("after both decisions:\n%s", v)
	}
	// With nothing left, the close tick (or esc) ends the popup; a stale tick does nothing.
	if _, cmd := m.Update(gateDoneClose{n: m.doneN - 1}); cmd != nil {
		t.Error("an earlier decision's close tick acted")
	}
	if _, cmd := m.Update(gateDoneClose{n: m.doneN}); cmd == nil || cmd() != tea.Quit() {
		t.Error("the close tick did not close the popup")
	}

	// r then esc cancels without rejecting; v shows the brief and esc returns.
	m, c = fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	m = gateKeys(m, "r", "n", "o", "esc", "v")
	if len(c.rejected) != 0 || m.mode != gateDetails || !strings.Contains(plain(m), "CSV export") {
		t.Errorf("r esc / v: rejected %v, mode %d\n%s", c.rejected, m.mode, plain(m))
	}
	if m = gateKeys(m, "esc"); m.mode != gateSummary {
		t.Error("esc did not leave the details")
	}

	// A dispute takes f or d, and a and r do nothing to it.
	d := waitingRun(t, loopAwaitYou)
	writeLoopJSON(d.ID, "dispute.json", loopTracked{ID: "F1-1", Engine: "codex", Dispute: "the test covers it",
		loopFinding: loopFinding{Priority: "P1", File: "export.go", Line: 9, Quote: "w.Write(row)", Claim: "error ignored", Fix: "check it"}})
	m, c = fakeGate(t, []*loopRun{d}, nil)
	for _, want := range []string{"F1-1", "export.go:9", "w.Write(row)", "error ignored", "check it", "the test covers it"} {
		if !strings.Contains(plain(m), want) {
			t.Errorf("dispute summary lacks %q:\n%s", want, plain(m))
		}
	}
	if a := gateKeys(m, "a"); !strings.Contains(a.result, "d dismisses") {
		t.Errorf("a on a dispute did not say how to rule it: %q", a.result)
	}
	m = gateKeys(m, "a", "r", "d", "4", "2")
	if len(c.approved)+len(c.rejected) != 0 || len(c.decided) != 1 || c.decided[0] != d.ID+":F1-1:dismiss" {
		t.Errorf("dispute keys: %+v", c)
	}

	// f decides "fix" with the finding the person was shown, after the code like d: either ruling
	// closes the dispute.
	d2 := waitingRun(t, loopAwaitYou)
	writeLoopJSON(d2.ID, "dispute.json", loopTracked{ID: "F2-3", loopFinding: loopFinding{Priority: "P1", File: "a.go", Line: 1, Quote: "x", Claim: "y"}})
	m, c = fakeGate(t, []*loopRun{d2}, nil)
	if gateKeys(m, "f"); len(c.decided) != 0 {
		t.Errorf("f decided without the code: %+v", c.decided)
	}
	if gateKeys(m, "f", "4", "2"); len(c.decided) != 1 || c.decided[0] != d2.ID+":F2-3:fix" {
		t.Errorf("f: %+v", c.decided)
	}

	// esc on an item's screen returns to the list; esc on the list, or q anywhere, closes the popup.
	m, _ = fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	if m = gateKeys(m, "esc"); !m.list {
		t.Error("esc on an item did not return to the list")
	}
	for _, k := range []string{"esc", "q"} {
		m, _ = fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
		m.list = k == "esc"
		msg := tea.KeyPressMsg{Code: tea.KeyEscape}
		if k == "q" {
			msg = tea.KeyPressMsg{Code: 'q', Text: "q"}
		}
		if _, cmd := m.Update(msg); cmd == nil || cmd() != tea.Quit() {
			t.Errorf("%s did not close the popup", k)
		}
	}
}

// The popup opens on a list of everything waiting, then active runs; keys act on the selected
// row; ⏎ opens it and esc returns.
func TestGateWaitingList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, b, d := waitingRun(t, loopAwaiting), waitingRun(t, loopAwaiting), waitingRun(t, loopAwaitYou)
	running := waitingRun(t, loopBuilding)
	m, c := fakeGate(t, []*loopRun{a, b, d, running}, nil)
	m.list, m.sel = true, 0
	out := plain(m)
	if !strings.Contains(out, "3 WAITING FOR YOU") {
		t.Errorf("list heading:\n%s", out)
	}
	order := []string{a.ID, b.ID, d.ID, running.ID}
	last := -1
	for _, id := range order {
		i := strings.Index(out, id)
		if i < 0 || i < last {
			t.Errorf("row %s missing or out of order:\n%s", id, out)
		}
		last = i
	}
	if !strings.Contains(out, "dispute") || !strings.Contains(out, "running loop") {
		t.Errorf("kinds not shown:\n%s", out)
	}
	m = gateKeys(m, "down", "enter")
	if m.list || m.current().ID != b.ID {
		t.Errorf("enter opened %s (list=%v), want %s", m.current().ID, m.list, b.ID)
	}
	if m = gateKeys(m, "esc"); !m.list {
		t.Error("esc did not return to the list")
	}
	if m = gateKeys(m, "a", "4", "2"); len(c.approved) != 1 || c.approved[0] != b.ID {
		t.Errorf("a on the list approved %v, want the selected %s", c.approved, b.ID)
	}
}

// Rejecting needs the code as well: r, a note and ⏎ reject nothing until it is typed.
func TestGateRejectNeedsCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, c := fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	m = gateKeys(m, "r", "n", "o", "enter")
	if len(c.rejected) != 0 || m.mode != gateConfirm {
		t.Fatalf("rejected %v before the code (mode %v)", c.rejected, m.mode)
	}
	if m = gateKeys(m, "4", "2"); len(c.rejected) != 1 {
		t.Errorf("the code did not reject: %v", c.rejected)
	}
}

// Approving and dismissing need the code shown in the popup; a wrong code decides nothing.
func TestGateCodeGuardsTheDecisionsThatLetWorkProceed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, c := fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	m = gateKeys(m, "a")
	if !strings.Contains(plain(m), "type this code to approve") {
		t.Errorf("no code prompt:\n%s", plain(m))
	}
	m = gateKeys(m, "1", "7")
	if len(c.approved) != 0 || !strings.Contains(m.result, "did not match") {
		t.Errorf("a wrong code approved: %v %q", c.approved, m.result)
	}
	m = gateKeys(m, "a", "esc")
	if len(c.approved) != 0 || m.mode != gateSummary {
		t.Error("esc during the code approved or stayed in the prompt")
	}
}

// A real rejection moves the run out of waiting: it cannot then be approved, and it drops out of
// the popup and the indicators.
func TestGateRealRejectionLeavesTheRunRevising(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	if err := loopRejectRun(r.ID, r.Revision, "too big"); err != nil {
		t.Fatal(err)
	}
	got, _ := loadLoopRun(r.ID)
	if got.State != loopRevising || loopWaiting(got) {
		t.Errorf("after rejection: %s, waiting=%v", got.State, loopWaiting(got))
	}
	if err := loopApproveRun(r.ID, r.Revision); err == nil {
		t.Error("a rejected brief was approvable")
	}
	for _, w := range loopWaitingRuns() {
		if w.ID == r.ID {
			t.Error("the rejected run is still listed as waiting")
		}
	}
}

// A decision the gate refuses changes nothing and says why.
func TestGateRefusalDecidesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, c := fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, os.ErrPermission)
	m = gateKeys(m, "a", "4", "2") // even with the right code
	if len(c.approved) != 0 || !strings.Contains(m.result, "refused") {
		t.Errorf("approved %v, result %q", c.approved, m.result)
	}
}

// A process on a pane's terminal is refused; one on another terminal is not.
func TestGateRefusesAPaneTerminal(t *testing.T) {
	fi, err := os.Stat("/dev/null")
	if err != nil {
		t.Skip(err)
	}
	dev := uint64(fi.Sys().(*syscall.Stat_t).Rdev)
	if err := gateNotAPane(dev, []string{"/dev/null"}); err == nil {
		t.Error("a decision from a pane's terminal was allowed")
	}
	if err := gateNotAPane(dev+1, []string{"/dev/null"}); err != nil {
		t.Errorf("a decision from elsewhere was refused: %v", err)
	}
}

// The summary carries everything the person needs before deciding, at both popup sizes.
func TestGateSummaryShowsWhatIsBeingApproved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	for _, size := range [][2]int{{80, 24}, {120, 38}} {
		m, _ := fakeGate(t, []*loopRun{r}, nil)
		nm, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		out := plain(nm.(gateModel))
		for _, want := range []string{"loop " + r.ID, "Export tickets as CSV.", "tab 12 claude", "first version",
			"$ make check-go", "claude,codex", "baseline", "approve", "reject with a note", "full brief"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d summary lacks %q:\n%s", size[0], size[1], want, out)
			}
		}
	}
}

// Long values wrap inside the popup rather than running off its right edge.
func TestGateWrapsToThePopupWidth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, _ := fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	out := plain(nm.(gateModel))
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(strings.TrimRight(line, " ")); w > 60 {
			t.Errorf("line of %d cells in a 60-cell popup: %q", w, line)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "partyline checks and reviews each round") {
		t.Errorf("the approving line lost its end:\n%s", out)
	}
}

// A decision acts on the run as it is on disk now, not on the copy the popup loaded: a run decided
// elsewhere, or a brief revised since the person read it, is refused and nothing changes.
func TestGateDecisionsUseTheRunOnDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	stale := *r
	_ = r.set(loopBuilding, "decided elsewhere", "approved", nil)
	if err := loopApproveRun(stale.ID, stale.Revision); err == nil || !strings.Contains(err.Error(), "nothing was decided") {
		t.Errorf("approved a run already decided elsewhere: %v", err)
	}
	r2 := waitingRun(t, loopAwaiting)
	r2.Revision = 2
	_ = r2.set(loopAwaiting, "brief 2", "brief_revised", nil)
	if err := loopRejectRun(r2.ID, 1, "too big"); err == nil || !strings.Contains(err.Error(), "brief changed") {
		t.Errorf("rejected revision 1 when the session had submitted revision 2: %v", err)
	}
	if got, _ := loadLoopRun(r2.ID); got.State != loopAwaiting || strings.Contains(got.Note, "rejected") {
		t.Errorf("the refused decision changed the run: %+v", got)
	}
}

// Text from the session cannot forge lines in the summary.
func TestGateSummaryCannotBeForged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	r.Checks = []string{"true\napproving  forged line", "x\x1b[2Jy"}
	r.Review = "codex\nreviewers  nobody"
	m, _ := fakeGate(t, []*loopRun{r}, nil)
	out := plain(m)
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "approving  forged") || strings.HasPrefix(l, "reviewers  nobody") {
			t.Errorf("forged line in the summary: %q\n%s", l, out)
		}
	}
	if strings.Contains(m.View().Content, "\x1b[2J") {
		t.Error("an escape sequence from a check reached the screen")
	}
}

// The summary says what changed in a revised brief, and the note the person gave when rejecting
// the revision before it.
func TestGateSaysWhatChangedInARevision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	_ = r.set(loopAwaiting, "brief rejected", "rejected", map[string]any{"note": "too big"})
	_ = os.WriteFile(loopFile(r.ID, "brief-2.md"), []byte(strings.Replace(goodBrief, "Export tickets as CSV.", "Export tickets as CSV, one month at a time.", 1)), 0o600)
	r.Revision = 2
	_ = r.set(loopAwaiting, "brief 2", "brief_revised", nil)
	note := briefRevisionNote(r)
	for _, want := range []string{"revision 2", "changed: Goal", `you rejected revision 1: "too big"`} {
		if !strings.Contains(note, want) {
			t.Errorf("revision note %q lacks %q", note, want)
		}
	}
}

// Enter on an empty note sends nothing and says a note is needed.
func TestGateEmptyNoteIsNotSent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, c := fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	m = gateKeys(m, "r", "enter")
	if len(c.rejected) != 0 || m.mode != gateNote || !strings.Contains(m.result, "required") {
		t.Errorf("rejected %v, mode %d, result %q", c.rejected, m.mode, m.result)
	}
}

// A summary taller than the popup scrolls, and the decision keys stay on screen.
func TestGateKeysStayVisibleWhenTheSummaryScrolls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	for i := 0; i < 30; i++ {
		r.Checks = append(r.Checks, "go test ./pkg"+strings.Repeat("x", i%3))
	}
	m, _ := fakeGate(t, []*loopRun{r}, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	out := plain(nm.(gateModel))
	if lipgloss.Height(strings.TrimRight(out, "\n")) > 24 || !strings.Contains(out, "approve") || !strings.Contains(out, "pgup/pgdn") {
		t.Errorf("tall summary at 80x24:\n%s", out)
	}
}

// A process descended from a pane's process is refused, whatever its own terminal.
func TestGateRefusesAProcessUnderAPane(t *testing.T) {
	if err := gateNotUnderAPane([]int{500, 400, 1}, map[int]bool{400: true}); err == nil {
		t.Error("a process under a pane was allowed")
	}
	if err := gateNotUnderAPane([]int{500, 300}, map[int]bool{400: true}); err != nil {
		t.Errorf("a process not under a pane was refused: %v", err)
	}
}

// The indicators on a real tmux server: the window holding a waiting run's session gets the glyph
// and the bar gets the count; deciding one of two runs in the same window keeps the glyph.
func TestGateIndicatorsOnAScratchServer(t *testing.T) {
	tmuxTestServer(t, "ptln-test-gate-indicators")
	markWaiting = loopMarkWaiting
	t.Cleanup(func() { markWaiting = func(*loopRun) {} })
	if err := tmuxCmd("new-session", "-d", "-s", "ptln", "-x", "80", "-y", "20", "sleep 300").Run(); err != nil {
		t.Skip("cannot start tmux: ", err)
	}
	_ = tmuxCmd("split-window", "-d", "-t", "ptln", "sleep 300").Run()
	panes, _ := tmuxCmd("list-panes", "-t", "ptln", "-F", "#{pane_id}").Output()
	ids := strings.Fields(string(panes))
	if len(ids) < 2 {
		t.Fatalf("panes: %q", ids)
	}
	opt := func(args ...string) string {
		out, _ := tmuxCmd(append([]string{"show-options", "-v"}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}
	mk := func(pane string) *loopRun {
		r := &loopRun{ID: newLoopID(), Repo: t.TempDir(), Session: []string{"pane:" + pane}, Revision: 1, Created: time.Now()}
		_ = os.MkdirAll(loopDir(r.ID), 0o700)
		_ = r.set(loopAwaiting, "", "started", nil)
		return r
	}
	// The conf's own formats, so what is checked is what the bar draws. Only those two lines: the
	// full conf carries hooks that run selfExe(), which under go test is this test binary, and a
	// hook firing as the scratch server shuts down would start the whole suite again.
	var formats []string
	for _, line := range strings.Split(tmuxConf(), "\n") {
		if strings.HasPrefix(line, "set -g status-right ") || strings.HasPrefix(line, "setw -g window-status-format ") || strings.HasPrefix(line, "setw -g window-status-current-format ") {
			formats = append(formats, line)
		}
	}
	if len(formats) != 3 {
		t.Fatalf("expected the three format lines in the conf, found %d", len(formats))
	}
	conf := filepath.Join(t.TempDir(), "formats.conf")
	_ = os.WriteFile(conf, []byte(strings.Join(formats, "\n")+"\n"), 0o600)
	if err := tmuxCmd("source-file", conf).Run(); err != nil {
		t.Fatalf("source the generated conf: %v", err)
	}
	// The tab is drawn by window-status-format, or window-status-current-format when it is the
	// current window: both must carry the glyph.
	bar := func() (string, string, string) {
		strip := regexp.MustCompile(`#\[[^]]*\]`)
		expand := func(f string) string {
			out, _ := tmuxCmd("display-message", "-p", "-t", ids[0], "#{T:"+f+"}").Output()
			return strings.TrimSpace(strip.ReplaceAllString(string(out), ""))
		}
		return expand("status-right"), expand("window-status-format"), expand("window-status-current-format")
	}
	a, b := mk(ids[0]), mk(ids[1])
	if right, tab, cur := bar(); !strings.Contains(right, "‼ 2 to approve · ctrl-\\ a") || !strings.HasPrefix(tab, "‼") || !strings.HasPrefix(cur, "‼") {
		t.Errorf("while two wait, the bar draws %q, the tab %q, the current tab %q", right, tab, cur)
	}
	if opt("-w", "-t", ids[0], "@ptln_needs") != "1" || opt("-g", "@ptln_waiting") != "2" {
		t.Fatalf("while two wait: needs=%q waiting=%q", opt("-w", "-t", ids[0], "@ptln_needs"), opt("-g", "@ptln_waiting"))
	}
	_ = a.set(loopBuilding, "", "approved", nil)
	if opt("-w", "-t", ids[0], "@ptln_needs") != "1" || opt("-g", "@ptln_waiting") != "1" {
		t.Errorf("one still waits in the window: needs=%q waiting=%q", opt("-w", "-t", ids[0], "@ptln_needs"), opt("-g", "@ptln_waiting"))
	}
	_ = b.set(loopBuilding, "", "approved", nil)
	if opt("-w", "-t", ids[0], "@ptln_needs") != "" || opt("-g", "@ptln_waiting") != "" {
		t.Errorf("none wait: needs=%q waiting=%q", opt("-w", "-t", ids[0], "@ptln_needs"), opt("-g", "@ptln_waiting"))
	}
	if right, tab, cur := bar(); strings.Contains(right, "to approve") || strings.Contains(tab, "‼") || strings.Contains(cur, "‼") {
		t.Errorf("after both decisions, the bar draws %q, the tab %q, the current tab %q", right, tab, cur)
	}
}

// The full brief scrolls in the details view.
func TestGateDetailsScroll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	long := goodBrief + "\n## Notes\n" + strings.Repeat("A line of notes for scrolling.\n\n", 60)
	_ = os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(long), 0o600)
	m, _ := fakeGate(t, []*loopRun{r}, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = gateKeys(nm.(gateModel), "v")
	before := plain(m)
	nm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if after := plain(nm.(gateModel)); after == before {
		t.Error("pgdown did not scroll the full brief")
	}
}

// A run ID from the session's arguments cannot name a path outside the loops directory.
func TestRunLockRefusesAPathAsAnID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := withRunLock("../../x", func(*loopRun) error { return nil }); err == nil || !strings.Contains(err.Error(), "no loop") {
		t.Errorf("a path was accepted as a run id: %v", err)
	}
}

// A brief cannot be revised while a round runs: the round would overwrite the revision.
func TestNoRevisionDuringARound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, state := range []string{loopBaseline, loopChecking} {
		r := waitingRun(t, state)
		if err := loopReviseBrief(r.ID, goodBrief); err == nil || !strings.Contains(err.Error(), "round is running") {
			t.Errorf("%s: revision allowed: %v", state, err)
		}
		if got, _ := loadLoopRun(r.ID); got.Revision != 1 || got.State != state {
			t.Errorf("%s: the refused revision changed the run: %+v", state, got)
		}
	}
	r := waitingRun(t, loopBuilding)
	if err := loopReviseBrief(r.ID, goodBrief); err != nil {
		t.Errorf("revision while building refused: %v", err)
	}
	if got, _ := loadLoopRun(r.ID); got.Revision != 2 || got.State != loopAwaiting {
		t.Errorf("after a revision while building: %+v", got)
	}
	if !strings.Contains(loopNextStep(&loopRun{ID: "abcdef", State: loopAwaiting}), "ctrl-\\ then a") {
		t.Error("the prompt context still names a shell command for approval")
	}
}

// Only a process with a popup's signature, with one client attached, may decide. The passing case is
// the real decision recorded on this machine: gate pid 21461, session 21461, parent 20193 (the tmux
// server), one client.
func TestGateRequiresAPopupAndOneClient(t *testing.T) {
	if err := gateInPopup(21461, 21461, 20193, 20193, []string{"20100 /dev/ttys004"}); err != nil {
		t.Errorf("the recorded real popup was refused: %v", err)
	}
	for name, c := range map[string][5]int{
		"own terminal, parent script": {500, 500, 499, 20193, 1},
		"not a session leader":        {500, 400, 20193, 20193, 1},
		"second client attached":      {21461, 21461, 20193, 20193, 2},
		"no client attached":          {21461, 21461, 20193, 20193, 0},
		"tmux server unknown":         {21461, 21461, 20193, 0, 1},
	} {
		if err := gateInPopup(c[0], c[1], c[2], c[3], make([]string, c[4])); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

// Wrong codes are counted on disk: after three, the run takes no confirmations, even in a popup
// opened afresh.
func TestGateWrongCodesLockTheRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	for i := 0; i < gateMaxMisses; i++ {
		m, _ := fakeGate(t, []*loopRun{r}, nil)
		gateKeys(m, "a", "1", "7")
	}
	m, c := fakeGate(t, []*loopRun{r}, nil) // a new popup
	m = gateKeys(m, "a", "4", "2")
	if len(c.approved) != 0 || !strings.Contains(m.result, "too many wrong codes") {
		t.Errorf("after %d misses: approved %v, result %q", gateMaxMisses, c.approved, m.result)
	}
	if code := gateCode(); len(code) != 4 {
		t.Errorf("code %q is not four digits", code)
	}
}

// Approving records the base commit and revision, moves to baseline and starts round 0.
func TestGateApproveStartsTheBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	r := testLoopRun(t, repo)
	_ = r.set(loopAwaiting, "", "started", nil)
	spawnedRounds = nil
	if err := loopApproveRun(r.ID, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := loadLoopRun(r.ID)
	head, _ := gitMem(repo, "rev-parse", "HEAD")
	if got.State != loopBaseline || got.Base != head || len(spawnedRounds) != 1 || spawnedRounds[0] != r.ID+"/0" {
		t.Errorf("after approve: %+v, rounds %v", got, spawnedRounds)
	}
	evs, _ := loopEvents(r.ID)
	if last := evs[len(evs)-1]; last.Type != "approved" || last.Data["revision"] != float64(1) || last.Data["by"] == nil {
		t.Errorf("approved event: %+v", last)
	}
}

// ready starts a round only from building, under the lock, and a brief revised meanwhile wins.
func TestReadyStartsARoundOnlyFromBuilding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopBuilding)
	spawnedRounds = nil
	if n, err := loopStartRound(r.ID, "report"); err != nil || n != 1 || len(spawnedRounds) != 1 {
		t.Fatalf("ready from building: %d %v %v", n, err, spawnedRounds)
	}
	r2 := waitingRun(t, loopBuilding)
	if err := loopReviseBrief(r2.ID, goodBrief); err != nil {
		t.Fatal(err)
	}
	if _, err := loopStartRound(r2.ID, "report"); err == nil {
		t.Error("ready started a round on a brief waiting for approval")
	}
}

// A brief with Windows line endings is plain text; a control character is reported with its line.
func TestBriefTextAcceptsCRLF(t *testing.T) {
	if err := briefIsText(strings.ReplaceAll(goodBrief, "\n", "\r\n")); err != nil {
		t.Errorf("CRLF brief refused: %v", err)
	}
	if err := briefIsText("# x\n## Goal\nbad \x1b[2J here\n"); err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("control character not reported by line: %v", err)
	}
}

// A finding can be disputed only while building: deciding a dispute moves the run to building, so
// one raised while a brief waits for approval, or after a stop, would skip the approval or undo it.
func TestDisputeOnlyWhileBuilding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, state := range []string{loopAwaiting, loopRevising, loopStopped, loopBuilding} {
		r := waitingRun(t, state)
		writeLoopJSON(r.ID, "findings-"+strconv.Itoa(r.Round)+".json", []loopTracked{{ID: "F1-1", loopFinding: loopFinding{Priority: "P1", File: "a.go", Line: 1}}})
		_, err := loopRaiseDispute(r.ID, "F1-1", "wrong")
		if got, _ := loadLoopRun(r.ID); (err == nil) != (state == loopBuilding) || (state != loopBuilding && got.State != state) {
			t.Errorf("dispute from %s: err %v, now %s", state, err, got.State)
		}
	}
}

// pgdn scrolls the summary no further than its end, so pgup moves straight back.
func TestGateSummaryScrollsBothWays(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	for i := 0; i < 30; i++ {
		r.Checks = append(r.Checks, "go test ./pkg"+strconv.Itoa(i))
	}
	m, _ := fakeGate(t, []*loopRun{r}, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(gateModel)
	top := plain(m)
	end := m.maxScroll()
	if end == 0 {
		t.Fatal("the summary does not overflow 80x24")
	}
	for i := 0; i < end+20; i++ {
		m = gateKeys(m, "pgdown")
	}
	if m.scroll != end || plain(m) == top {
		t.Errorf("after holding pgdn: scroll %d of %d", m.scroll, end)
	}
	if m = gateKeys(m, "pgup"); m.scroll != end-1 {
		t.Errorf("one pgup from the end: scroll %d, want %d", m.scroll, end-1)
	}
	for i := 0; i < end; i++ {
		m = gateKeys(m, "pgup")
	}
	if plain(m) != top {
		t.Error("pgup back to the top does not restore the first screen")
	}
}

// A confirmed decision clears the person's earlier typos for that run.
func TestGateDecisionClearsMisses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	m, c := fakeGate(t, []*loopRun{r}, nil)
	m = gateKeys(m, "a", "1", "7", "a", "4", "2")
	if len(c.approved) != 1 || len(gateMisses(r.ID)) != 0 {
		t.Errorf("approved %v, misses left %d", c.approved, len(gateMisses(r.ID)))
	}
}

// The gate's checks run before the code is asked for, and the confirmation method describes one
// decision.
func TestGateConfirmOrderAndProvenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	m, calls := fakeGate(t, []*loopRun{r}, fmt.Errorf("two clients attached"))
	m = gateKeys(m, "a", "4", "2")
	if len(calls.approved) != 0 || !strings.Contains(m.result, "refused") {
		t.Errorf("refused context: approved %v, result %q", calls.approved, m.result)
	}
	// An approval, then a rejection: a rejection needs confirming too (batch step 3), so each
	// decision records the method that confirmed it, the code both times here.
	var methods []string
	m, _ = fakeGate(t, []*loopRun{r, waitingRun(t, loopAwaiting)}, nil)
	m.act.approve = func(*loopRun) error { methods = append(methods, gateConfirmMethod); return nil }
	m.act.reject = func(*loopRun, string) error { methods = append(methods, gateConfirmMethod); return nil }
	m = gateKeys(m, "a", "4", "2", "esc")
	m = gateKeys(m, "r", "n", "o", "enter", "4", "2")
	if len(methods) != 2 || methods[0] != "code" || methods[1] != "code" {
		t.Errorf("methods seen by the decisions: %v", methods)
	}
}

// The code is drawn large, every digit five rows tall, beside the line that says what it confirms.
func TestGateCodeDrawnLarge(t *testing.T) {
	if got := strings.Split(bigDigits("4217"), "\n"); len(got) != 5 || got[0] != "█   █   █████     █     █████" {
		t.Errorf("bigDigits rows: %q", got)
	}
	m, _ := fakeGate(t, []*loopRun{waitingRun(t, loopAwaiting)}, nil)
	m = gateKeys(m, "a")
	if out := plain(m); !strings.Contains(out, "type this code to approve loop") || strings.Count(out, "█") < 20 {
		t.Errorf("confirm screen:\n%s", out)
	}
}

// The popup refreshes on its summary, keeping the selection, and leaves the code screen alone.
func TestGateRefreshes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, b := waitingRun(t, loopAwaiting), waitingRun(t, loopAwaiting)
	listed := []*loopRun{a}
	m, _ := fakeGate(t, listed, nil)
	m.act.list = func() []*loopRun { return listed }
	listed = []*loopRun{b, a}
	nm, cmd := m.Update(gateTick{})
	m = nm.(gateModel)
	if len(m.runs) != 2 || m.current().ID != a.ID || cmd == nil {
		t.Errorf("after a tick: %d runs, selected %s, next tick %v", len(m.runs), m.current().ID, cmd != nil)
	}
	m = gateKeys(m, "a") // the code screen
	listed = nil
	nm, _ = m.Update(gateTick{})
	if m = nm.(gateModel); m.mode != gateConfirm || len(m.runs) != 2 {
		t.Errorf("a tick on the code screen changed it: mode %v, %d runs", m.mode, len(m.runs))
	}
}

// A run in review names each reviewer and whether it has reported.
func TestGateReviewProgress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := waitingRun(t, loopAwaiting)
	r.Round = 2
	_ = r.set(loopChecking, "round 2: checks, then review", "ready", nil)
	_ = os.WriteFile(loopFile(r.ID, "review-r2-2-codex.json"), []byte("{}"), 0o600)
	if got := reviewProgress(r); got != "claude reading · codex done" {
		t.Errorf("progress %q", got)
	}
}

// The popup fits its client: tmux refuses one taller or wider and shows nothing.
func TestGatePopupFitsTheClient(t *testing.T) {
	for _, c := range []struct{ cols, rows, w, h int }{{200, 60, 96, 30}, {80, 24, 78, 22}, {0, 0, 96, 30}, {10, 5, 8, 3}} {
		if w, h := gatePopupSize(c.cols, c.rows); w != c.w || h != c.h {
			t.Errorf("%dx%d client: popup %dx%d, want %dx%d", c.cols, c.rows, w, h, c.w, c.h)
		}
	}
}

func TestGateBoardSelectionKeepsStopConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, b := waitingRun(t, loopAwaiting), waitingRun(t, loopBuilding)
	m, c := fakeGate(t, []*loopRun{a, b}, nil)
	m.list = true
	m = m.selectRun(b.ID)
	if m.current().ID != b.ID || m.list {
		t.Fatal("wrong gate target")
	}
	m = gateKeys(m, "s")
	if len(c.stopped) != 0 {
		t.Fatal("stop skipped confirmation")
	}
	m = gateKeys(m, "4", "2")
	if len(c.stopped) != 1 || c.stopped[0] != b.ID {
		t.Fatal(c.stopped)
	}
	m, c = fakeGate(t, []*loopRun{a}, nil)
	m.list = true
	m = m.selectRun("gone")
	if !m.list || len(c.stopped) > 0 {
		t.Fatal("missing target acted")
	}
}

// While the code is typed, the digits typed so far change colour and a cursor blinks under the next.
func TestGateCodeShowsProgress(t *testing.T) {
	st := gateStyle{tmuxTheme()}
	none, two := bigDigitsTyped(st, "4217", 0, true), bigDigitsTyped(st, "4217", 2, true)
	if none == two {
		t.Fatal("typing changed nothing on the big code")
	}
	rows := strings.Split(two, "\n")
	cursor := xansi.Strip(rows[5])
	if len(rows) != 6 || strings.Index(cursor, "▀") != 2*(5+3) {
		t.Fatalf("the cursor is not under the third digit: %q", cursor)
	}
	if off := xansi.Strip(strings.Split(bigDigitsTyped(st, "4217", 2, false), "\n")[5]); strings.Contains(off, "▀") {
		t.Fatal("the cursor does not blink off")
	}
	if xansi.Strip(strings.Join(rows[:5], "\n")) != bigDigits("4217") {
		t.Fatal("the digits themselves changed")
	}
}
