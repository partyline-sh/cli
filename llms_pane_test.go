package main

import (
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// cupRe matches any absolute cursor positioning (CSI r;cH / CSI nH / CSI nd / CSI nG) — the
// escapes that would make a frame impossible to relocate into a split pane.
var cupRe = regexp.MustCompile(`\x1b\[[0-9;]*[HfdG]`)

func paneMenu() *aiMenu {
	m := &aiMenu{
		all: []aiSession{
			{ID: "s1", Cwd: "/proj/a", Tool: "claude", Title: "fix the parser"},
			{ID: "s2", Cwd: "/proj/a", Tool: "codex", Title: "add tests"},
			{ID: "s3", Cwd: "/proj/b", Tool: "gemini", Title: "docs pass"},
		},
		meta:    map[string]sessMeta{},
		tagline: "test",
	}
	m.applyFilter()
	return m
}

// TestPaneLinesArePositionIndependent is the load-bearing contract behind in-pane managers:
// RenderLines' rows must carry SGR colour ONLY. Any cursor positioning, erase-in-line or
// erase-display escape left in a row would corrupt the neighbouring pane (clipPadANSI scans
// escapes only up to 'm', so a stray \x1b[K would swallow the rest of the row).
func TestPaneLinesArePositionIndependent(t *testing.T) {
	const cols, rows = 44, 18
	for _, tc := range []struct {
		name string
		prep func(*aiMenu)
	}{
		{"plain", func(*aiMenu) {}},
		{"searching", func(m *aiMenu) { m.filter, m.query = true, "s" }},
		{"picker modal", func(m *aiMenu) { m.picking, m.pickModes = true, permissionModes("claude") }},
		{"rename modal", func(m *aiMenu) { m.renaming, m.renameBuf = true, "new name" }},
		{"help", func(m *aiMenu) { m.help = true }},
	} {
		m := paneMenu()
		tc.prep(m)
		lines := m.paneLines(cols, rows)
		if len(lines) != rows {
			t.Fatalf("%s: got %d lines, want exactly %d", tc.name, len(lines), rows)
		}
		for i, ln := range lines {
			if loc := cupRe.FindString(ln); loc != "" {
				t.Errorf("%s: line %d carries positioning %q: %q", tc.name, i, loc, ln)
			}
			for _, bad := range []string{"\x1b[K", "\x1b[J", "\r", "\n"} {
				if strings.Contains(ln, bad) {
					t.Errorf("%s: line %d contains %q: %q", tc.name, i, bad, ln)
				}
			}
			if w := brand.VisWidth(ln); w > cols {
				t.Errorf("%s: line %d is %d cols wide, pane is %d: %q", tc.name, i, w, cols, ln)
			}
		}
	}
}

// TestPaneLinesDropsDetailPanel pins the narrow (in-pane) layout: the list spans the whole pane
// and the detail panel is gone, so a half-width pane isn't rendered as an unreadable sliver.
func TestPaneLinesDropsDetailPanel(t *testing.T) {
	m := paneMenu()
	lines := m.paneLines(44, 18)
	// The panel top row is line 3 (banner, tagline, then the box top). Full-screen it holds two
	// boxes ("╭…╮ ╭…╮"); in a pane exactly one.
	if n := strings.Count(lines[2], "╭"); n != 1 {
		t.Fatalf("pane layout has %d panel(s) on the box-top row, want 1: %q", n, lines[2])
	}
	if m.narrow {
		t.Error("narrow must be reset after paneLines so a later full-screen render is unaffected")
	}
}

// TestPaneLinesModalIsComposited proves the modal survived the position-independence rewrite:
// its box is really drawn into the rows (not silently dropped with the CUP escapes).
func TestPaneLinesModalIsComposited(t *testing.T) {
	m := paneMenu()
	m.renaming, m.renameBuf = true, "zzmarker"
	joined := strings.Join(m.paneLines(44, 18), "\n")
	if !strings.Contains(joined, "rename session") || !strings.Contains(joined, "zzmarker") {
		t.Fatalf("modal box not composited into the pane rows:\n%s", joined)
	}
}

// TestManagerAdvertisesSplitKey pins the discoverability fix: EVERY manager hint row names the
// split route. The first version only did it on the SESSION row, which shipped the bug this test
// now guards: the switchboard opens with the tree COLLAPSED, so the first row a new user ever
// sees is PROJECT — and that was the one state not advertising the split. The firstRun footer had
// the same hole. `|` is a global action (it opens two pickers, it never reads the selection), so
// selection state must not decide whether it's discoverable.
func TestManagerAdvertisesSplitKey(t *testing.T) {
	const want = "| open in split"

	t.Run("SESSION row", func(t *testing.T) {
		m := paneMenu()
		m.w, m.h = 120, 30
		expandAll(m)
		foot := stripSGRText(m.footer())
		for _, s := range []string{"↵ open", want} {
			if !strings.Contains(foot, s) {
				t.Errorf("SESSION hint row %q lacks %q", foot, s)
			}
		}
		if i, j := strings.Index(foot, "↵ open"), strings.Index(foot, want); j < i {
			t.Errorf("hint order is wrong — %q must come after %q: %q", want, "↵ open", foot)
		}
	})

	// The state a first-time user actually lands in: collapsed tree, cursor on a project header.
	t.Run("PROJECT row", func(t *testing.T) {
		m := paneMenu()
		m.w, m.h = 120, 30
		m.cursor = 0 // the tree starts collapsed, so row 0 is a header
		if r := m.curRow(); r == nil || !r.header() {
			t.Fatalf("expected a project header at the cursor — got %+v", r)
		}
		foot := stripSGRText(m.footer())
		if !strings.Contains(foot, "PROJECT") {
			t.Fatalf("expected the PROJECT mode pill: %q", foot)
		}
		if !strings.Contains(foot, want) {
			t.Errorf("PROJECT hint row %q lacks %q — this is the row a new user sees FIRST", foot, want)
		}
	})

	t.Run("DETAIL row", func(t *testing.T) {
		m := paneMenu()
		m.w, m.h = 120, 30
		expandAll(m)
		m.focusR = true
		foot := stripSGRText(m.footer())
		if !strings.Contains(foot, want) {
			t.Errorf("DETAIL hint row %q lacks %q", foot, want)
		}
	})

	t.Run("firstRun footer", func(t *testing.T) {
		m := paneMenu()
		m.w, m.h = 160, 30
		m.firstRun = true
		if foot := stripSGRText(m.footer()); !strings.Contains(foot, "|") {
			t.Errorf("firstRun footer %q never mentions the split key", foot)
		}
	})

	// Second in the row means it outlives right-edge truncation; the pill always survives.
	t.Run("survives a narrow terminal", func(t *testing.T) {
		m := paneMenu()
		m.h = 30
		expandAll(m)
		m.w = 40
		if foot := stripSGRText(m.footer()); !strings.Contains(foot, want) {
			t.Errorf("at 40 cols the split hint was truncated away: %q", foot)
		}
		m.w = 12
		if narrow := stripSGRText(m.footer()); !strings.Contains(narrow, "SESSION") {
			t.Errorf("narrow hint row dropped the mode pill: %q", narrow)
		}
	})
}

// TestManagerSplitKeyRequestsSetup: bare `|` in the manager asks for the guided split (and does
// not fall through to some other verb or a silent no-op).
func TestManagerSplitKeyRequestsSetup(t *testing.T) {
	m := paneMenu()
	if done, chosen := m.handleKey([]byte("|")); done || chosen != nil {
		t.Fatalf("`|` = (done=%v, chosen=%v), want it to neither quit nor resume", done, chosen)
	}
	if !m.splitSetup {
		t.Fatal("`|` did not request the guided split")
	}
	if m.spawnShell || m.sharing || m.filter || m.help || m.picking {
		t.Error("`|` triggered some other manager verb as well")
	}
}

// TestFirstPaintIsNotScrolled is the clampScroll regression: applyFilter clamps before the menu
// has a size (m.h == 0 ⇒ bodyH() == -5), which used to push m.top to 6 — the launcher's FIRST
// paint opened scrolled 6 rows down, blank whenever the tree had fewer than 7 rows, until a
// keypress re-clamped. Reproduces on the full-screen switchboard, not just in a pane.
func TestFirstPaintIsNotScrolled(t *testing.T) {
	m := paneMenu() // built + applyFilter()ed with no size, exactly as the real launcher is
	if m.top != 0 {
		t.Fatalf("m.top = %d before the first paint, want 0", m.top)
	}
	// And the first frame really shows the top of the tree (it used to open blank).
	expandAll(m)
	m.top, m.cursor = 0, 0
	m.applyFilter() // the launcher's own build order: filter with no size, then paint
	if m.top != 0 {
		t.Fatalf("m.top = %d after a size-less applyFilter, want 0", m.top)
	}
	lines := m.paneLines(44, 18)
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "fix the parser") {
		t.Errorf("first frame is scrolled past the first session:\n%s", joined)
	}
	// A cursor genuinely below the fold still scrolls once the height is known.
	m.h = 10 // bodyH() == 5
	m.cursor = 7
	m.clampScroll()
	if m.top != 3 {
		t.Errorf("m.top = %d with cursor 7 in a 5-row body, want 3", m.top)
	}
}

// stripSGRText drops every escape so hint text can be asserted on directly.
func stripSGRText(s string) string { return cupRe.ReplaceAllString(sgrRe.ReplaceAllString(s, ""), "") }

// expandAll opens every project in the tree so a SESSION row can be selected.
func expandAll(m *aiMenu) {
	for _, r := range append([]treeRow(nil), m.rows...) {
		if r.header() {
			m.setRowCollapsed(r, false)
		}
	}
	for i := range m.rows { // put the cursor on the first session row, not a header
		if !m.rows[i].header() {
			m.cursor = i
			break
		}
	}
}

// TestPaneManagerEscCancelsWithNoLiveSessions pins the guided split's promised exit. The setup's
// status bar says "esc cancels", and its documented entry path is a bare `|` with NOTHING open
// yet (splitmux.go) — so esc has to return a cancel action at ZERO live sessions. The LiveKeys
// gate that guards the FULL-SCREEN switchboard's esc ("go back to the session you came from")
// must not reach the in-pane managers, where esc means cancelSplit instead.
func TestPaneManagerEscCancelsWithNoLiveSessions(t *testing.T) {
	mx := &ptymux.Mux{}
	if n := len(mx.LiveKeys()); n != 0 {
		t.Fatalf("precondition: %d live sessions, want 0", n)
	}
	full := &llmsHome{m: paneMenu(), mux: mx}
	pane := newPaneHome(full)
	if act := pane.HandleKey([]byte{0x1b}); !act.Return {
		t.Errorf("esc in a split-setup pane = %+v, want Return:true (→ cancelSplit)", act)
	}
	// The full-screen manager keeps its existing gate: with nothing live there is no session to
	// return TO, so esc stays a no-op there.
	if act := full.HandleKey([]byte{0x1b}); act.Return {
		t.Errorf("esc in the full-screen manager with nothing live = %+v, want no-op", act)
	}
}
