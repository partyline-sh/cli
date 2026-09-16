package ptymux

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// The plan's one real gap: Snapshot() restores content+cursor+alt-screen but NOT the
// other DEC private modes, so the mux must sniff them from each child's output and
// re-assert on switch. These tests pin that sniff-and-restore contract.

func TestModeStateObserveRestore(t *testing.T) {
	var m modeState
	// A child hides the cursor, turns on SGR mouse + bracketed paste + app-cursor-keys.
	m.observe([]byte("hello\x1b[?25l\x1b[?1h\x1b[?2004h\x1b[?1006hworld"))
	got := string(m.restore())
	for _, want := range []string{"\x1b[?25l", "\x1b[?1h", "\x1b[?2004h", "\x1b[?1006h"} {
		if !strings.Contains(got, want) {
			t.Errorf("restore() = %q, missing %q", got, want)
		}
	}
}

func TestModeStateClearedModeNotRestored(t *testing.T) {
	var m modeState
	// Set bracketed paste then clear it; cursor shown (default) — restore should emit neither.
	m.observe([]byte("\x1b[?2004h\x1b[?2004l\x1b[?25h"))
	got := string(m.restore())
	if strings.Contains(got, "2004") {
		t.Errorf("restore() = %q, should not re-assert a cleared mode", got)
	}
	if strings.Contains(got, "?25l") {
		t.Errorf("restore() = %q, cursor was shown — should not re-hide", got)
	}
}

func TestModeStateMultiParam(t *testing.T) {
	var m modeState
	// One escape can set several modes: `\x1b[?1000;1006h`.
	m.observe([]byte("\x1b[?1000;1006h"))
	got := string(m.restore())
	if !strings.Contains(got, "?1000h") || !strings.Contains(got, "?1006h") {
		t.Errorf("restore() = %q, want both 1000h and 1006h", got)
	}
}

func TestModeStateIgnoresUntracked(t *testing.T) {
	var m modeState
	// ?1049 (alt-screen) is Snapshot's job, not ours — must not appear in restore().
	m.observe([]byte("\x1b[?1049h\x1b[?7h"))
	if got := string(m.restore()); got != "" {
		t.Errorf("restore() = %q, want empty (only untracked modes seen)", got)
	}
}

func TestDecodeCmdKey(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		key  byte
		n    int
	}{
		{"plain letter", []byte("l"), 'l', 1},
		{"plain digit", []byte("3"), '3', 1},
		{"ctrl-letter byte", []byte{0x0c}, 'l', 1},        // ctrl-l held through the chord
		{"ctrl-p byte", []byte{0x10}, 'p', 1},             // ctrl-p
		{"literal ctrl-backslash", []byte{0x1c}, 0x1c, 1}, // ctrl-\ ctrl-\ → literal
		{"csi-u ctrl-p", []byte("\x1b[112;5u"), 'p', 8},   // from the log: holding ctrl
		{"csi-u ctrl-l", []byte("\x1b[108;5u"), 'l', 8},
		{"csi-u plain l", []byte("\x1b[108u"), 'l', 6},
		{"csi-u uppercase", []byte("\x1b[76;5u"), 'l', 7},             // code 76 = 'L' → lowercased
		{"modifyOtherKeys ctrl-q", []byte("\x1b[27;5;113~"), 'q', 11}, // from the log
		{"modifyOtherKeys ctrl-n", []byte("\x1b[27;5;110~"), 'n', 11},
		{"kitty sub-param", []byte("\x1b[112;5:1u"), 'p', 10}, // event-type sub-param ignored
	}
	for _, c := range cases {
		k, n := decodeCmdKey(c.in)
		if k != c.key || n != c.n {
			t.Errorf("%s: decodeCmdKey(%q) = (%q,%d), want (%q,%d)", c.name, c.in, k, n, c.key, c.n)
		}
	}
}

func TestLegacyKeys(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain passthrough", "abc", "abc"},
		{"raw enter passthrough", "\r", "\r"},
		{"legacy arrow passthrough", "\x1b[A", "\x1b[A"},
		{"legacy pageup passthrough", "\x1b[5~", "\x1b[5~"},
		{"bracketed paste marker passthrough", "\x1b[200~", "\x1b[200~"},
		{"csi-u enter", "\x1b[13;1u", "\r"},
		{"csi-u letter j", "\x1b[106;1u", "j"},
		{"csi-u uppercase stays char", "\x1b[74u", "J"},
		{"csi-u ctrl-c", "\x1b[99;5u", "\x03"},
		{"release dropped", "\x1b[116;1:3u", ""},
		{"press kept, release dropped", "\x1b[116;1u\x1b[116;1:3u", "t"},
		{"modifyOtherKeys enter", "\x1b[27;1;13~", "\r"},
		{"esc", "\x1b[27;1u", "\x1b"},
	}
	for _, c := range cases {
		if got := string(legacyKeys([]byte(c.in))); got != c.want {
			t.Errorf("%s: legacyKeys(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestArrowAt(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		dir  int
		n    int
	}{
		{"csi right", []byte("\x1b[C"), 1, 3},
		{"csi left", []byte("\x1b[D"), -1, 3},
		{"ss3 right", []byte("\x1bOC"), 1, 3},
		{"ss3 left", []byte("\x1bOD"), -1, 3},
		{"ctrl-right with mods", []byte("\x1b[1;5C"), 1, 6},
		{"shift-left with mods", []byte("\x1b[1;2D"), -1, 6},
		{"up consumed, no move", []byte("\x1b[A"), 0, 3},
		{"down consumed, no move", []byte("\x1b[B"), 0, 3},
		{"home key is NOT an arrow", []byte("\x1b[H"), 0, 0},
		{"plain byte NOT", []byte("x"), 0, 0},
		{"bare esc NOT", []byte("\x1b"), 0, 0},
	}
	for _, c := range cases {
		dir, n := arrowAt(c.in)
		if dir != c.dir || n != c.n {
			t.Errorf("%s: arrowAt(%q) = (%d,%d), want (%d,%d)", c.name, c.in, dir, n, c.dir, c.n)
		}
	}
}

func TestClipANSIVisibleWidth(t *testing.T) {
	// Escape sequences must not count toward the visible width budget.
	s := "\x1b[7m AB \x1b[0m"
	got := clipANSI(s, 2)
	// A RESET is appended when the cut lands inside styled text. This changed with the move to
	// brand.Clip and is deliberate: cutting mid-style used to leave the attribute open, so whatever
	// the terminal drew next inherited the tab's background. Safer, and the shared clipper's
	// documented contract.
	if want := "\x1b[7m A\x1b[0m"; got != want {
		t.Errorf("clipANSI(%q, 2) = %q, want %q", s, got, want)
	}
	if clipANSI("abcdef", 0) != "" {
		t.Error("clipANSI with width 0 should be empty")
	}
	// Fits entirely → unchanged.
	if clipANSI("abc", 10) != "abc" {
		t.Error("clipANSI should pass through strings narrower than the width")
	}
}

func TestScrollKeyAt(t *testing.T) {
	cases := []struct {
		name   string
		in     []byte
		action int
	}{
		{"up arrow", []byte("\x1b[A"), scrUp},
		{"down arrow", []byte("\x1b[B"), scrDown},
		{"ss3 up", []byte("\x1bOA"), scrUp},
		{"pageup", []byte("\x1b[5~"), scrPgUp},
		{"pagedown", []byte("\x1b[6~"), scrPgDn},
		{"home csi", []byte("\x1b[H"), scrTop},
		{"home tilde", []byte("\x1b[1~"), scrTop},
		{"end csi exits", []byte("\x1b[F"), scrExit},
		{"vi k", []byte("k"), scrUp},
		{"vi j", []byte("j"), scrDown},
		{"vi g top", []byte("g"), scrTop},
		{"space pages down", []byte(" "), scrPgDn},
		{"q exits", []byte("q"), scrExit},
		{"esc exits", []byte("\x1b"), scrExit},
		{"enter exits", []byte("\r"), scrExit},
		{"random key exits", []byte("z"), scrExit},
	}
	for _, c := range cases {
		act, n := scrollKeyAt(c.in)
		if act != c.action || n <= 0 {
			t.Errorf("%s: scrollKeyAt(%q) = (action %d, n %d), want action %d, n>0", c.name, c.in, act, n, c.action)
		}
	}
}

// TestChildEnvIsolation is the regression guard for the cross-contamination bug: each session's
// PARTYLINE_THREAD_ID must come from its OWN Spec, and a thread inherited in the mux's env must
// never leak into a child that isn't attached (or into one attached to a different thread).
func TestChildEnvIsolation(t *testing.T) {
	t.Setenv("PARTYLINE_THREAD_ID", "STALE-GLOBAL")
	t.Setenv("PARTYLINE_ENGINE", "stale")

	has := func(env []string, kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}

	// Attached session A → its own thread, and the stale global is stripped.
	a := childEnv(Spec{Thread: "A", Engine: "claude"})
	if !has(a, "PARTYLINE_THREAD_ID=A") || !has(a, "PARTYLINE_ENGINE=claude") {
		t.Fatalf("A missing its own thread/engine: %v", a)
	}
	if has(a, "PARTYLINE_THREAD_ID=STALE-GLOBAL") {
		t.Fatalf("A leaked the stale global thread")
	}

	// A different attached session → its own thread, not A's.
	b := childEnv(Spec{Thread: "B", Engine: "codex"})
	if !has(b, "PARTYLINE_THREAD_ID=B") || has(b, "PARTYLINE_THREAD_ID=A") {
		t.Fatalf("B cross-contaminated: %v", b)
	}

	// An UNATTACHED session → no thread at all, even though the mux's env has a stale one.
	n := childEnv(Spec{})
	for _, e := range n {
		if len(e) >= 20 && e[:20] == "PARTYLINE_THREAD_ID=" {
			t.Fatalf("unattached session got a thread: %q", e)
		}
	}
}

// commandPanelLines is the pure layout behind the ctrl-\ command panel: it packs items into
// rows that fit the given width and grows the row count with the item count. These tests pin
// (a) no row ever exceeds the width and (b) the row count matches the greedy packing.
func TestCommandPanelLines(t *testing.T) {
	// Plain items (visLen measures display columns, so ANSI is irrelevant here).
	items := []string{
		"n new/run", "c context", "m mcp", "w worktree", "g keep-going", "s share",
		"←/→·1-9 switch", "[ scroll", "o launcher", "x close", "q quit", "esc cancel",
	}
	widths := []int{20, 40, 80, 120, 200}
	for _, w := range widths {
		lines := commandPanelLines(items, w)
		if len(lines) == 0 {
			t.Fatalf("width %d: got no lines", w)
		}
		for i, ln := range lines {
			if got := brand.VisWidth(ln); got > w {
				t.Errorf("width %d: line %d = %q has visible width %d > %d", w, i, ln, got, w)
			}
		}
	}
	// Growth: a wider panel packs into fewer (or equal) rows, never more.
	prev := 1 << 30
	for _, w := range widths {
		n := len(commandPanelLines(items, w))
		if n > prev {
			t.Errorf("width %d produced %d rows, more than the narrower panel's %d", w, n, prev)
		}
		prev = n
	}
	// Row count matches greedy packing at a width that forces wrapping. With w=20 each item is
	// its own row unless two short ones fit: "m mcp"(5)+2+"q quit"(6) style. Verify the exact
	// count against a hand-run of the greedy algorithm.
	small := []string{"a bb", "c dd", "e ff"} // widths 4,4,4; gap 2
	if got := commandPanelLines(small, 10); len(got) != 2 {
		// "a bb"(4)+2+"c dd"(4)=10 fits → row1; "e ff" → row2
		t.Errorf("greedy pack at width 10: got %d rows (%q), want 2", len(got), got)
	}
	if got := commandPanelLines(small, 4); len(got) != 3 {
		t.Errorf("greedy pack at width 4: got %d rows, want 3 (one per item)", len(got))
	}
	// Degenerate widths return nil rather than panic.
	if commandPanelLines(items, 0) != nil {
		t.Error("width 0 should yield no lines")
	}
	if commandPanelLines(nil, 80) != nil {
		t.Error("no items should yield no lines")
	}
	// An item wider than the width still lands on its own row (nothing to split it on).
	if got := commandPanelLines([]string{"waytoolong"}, 4); len(got) != 1 {
		t.Errorf("oversized item: got %d rows, want 1", len(got))
	}
}

// muxWithGatedChild builds a mux with one gated child focused in live mode — enough to drive
// the overlay bookkeeping (no PTY, no terminal).
func muxWithGatedChild() (*Mux, *child) {
	mx := &Mux{wakeR: -1, wakeW: -1, mode: modeLive, cols: 80, rows: 24}
	ch := &child{key: "A", label: "A"}
	ch.gate = &gate{out: &mx.outMu, mx: mx, ch: ch, active: true}
	mx.children = append(mx.children, ch)
	mx.active = 0
	return mx, ch
}

func (g *gate) isPaused() bool {
	g.out.Lock()
	defer g.out.Unlock()
	return g.paused
}

// TestResizeClearsQuitPrompt is TRAP-4: a resize erases the quit box and hands the child its
// screen back, so mx.confirming MUST NOT survive it. The forbidden state is "box gone + child
// live again + confirming still true" — there, Run keeps routing keys to handleConfirm and the
// next y/⏎ quits partyline and closes every session, with nothing on screen to explain it.
func TestResizeClearsQuitPrompt(t *testing.T) {
	mx, _ := muxWithGatedChild()
	if mx.requestQuit() {
		t.Fatal("requestQuit quit outright with a live session")
	}
	mx.mu.Lock()
	confirming := mx.confirming
	mx.mu.Unlock()
	if !confirming {
		t.Fatal("requestQuit did not raise the prompt")
	}
	mx.clearResizeOverlays()
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if mx.confirming {
		t.Error("mx.confirming survived a resize — the next y/⏎ would quit with no prompt on screen")
	}
}

// TestResizeClearsArmedPanel is the same shape for the ctrl-\ command panel: the repaint erases
// it, so its bookkeeping goes too — otherwise the panel's child stays paused forever (a frozen
// session) and the next key is still eaten as a mux command.
func TestResizeClearsArmedPanel(t *testing.T) {
	mx, ch := muxWithGatedChild()
	mx.mu.Lock()
	mx.pfxCh, mx.sawPfx = ch, true
	mx.mu.Unlock()
	ch.gate.setPaused(true)

	resumed := mx.clearResizeOverlays()
	mx.mu.Lock()
	pfx, saw := mx.pfxCh, mx.sawPfx
	mx.mu.Unlock()
	if pfx != nil {
		t.Error("mx.pfxCh survived a resize — the panel is erased but its child stays paused")
	}
	if saw {
		t.Error("mx.sawPfx survived a resize — the next key would be eaten as a mux command")
	}
	if ch.gate.isPaused() {
		t.Error("the armed panel's child is still paused after a resize (frozen session)")
	}
	if len(resumed) != 1 || resumed[0] != ch {
		t.Errorf("resumed = %v, want the one paused child", resumed)
	}
}

// TestResizeLeavesScrollExit unchanged: scroll mode already dropped out on resize, and that
// behaviour moved into clearResizeOverlays — pin it so the move didn't lose it.
func TestResizeLeavesScrollExit(t *testing.T) {
	mx, ch := muxWithGatedChild()
	mx.mu.Lock()
	mx.scrolling, mx.scrollOff = true, 7
	mx.mu.Unlock()
	ch.gate.setPaused(true)

	mx.clearResizeOverlays()
	mx.mu.Lock()
	scrolling, off := mx.scrolling, mx.scrollOff
	mx.mu.Unlock()
	if scrolling || off != 0 {
		t.Errorf("scrolling=%v off=%d after a resize, want false/0", scrolling, off)
	}
	if ch.gate.isPaused() {
		t.Error("the scroll viewport's child is still paused after a resize")
	}
}
