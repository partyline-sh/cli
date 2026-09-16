package ptymux

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// Reported from a screenshot: nine sessions, and the ⌂ launcher — the tenth item, and the only way
// to REACH the launcher by arrowing — was clipped off the right edge, leaving a partial glyph.
//
// Two bugs met. The bar was measured in BYTES while the terminal renders COLUMNS, so every
// multi-byte glyph (● ☎ ◉ ⌂) counted 3 for the 1 column it draws; and the clipper walked bytes too,
// so it could stop mid-rune. The row believed it was ~38 columns wider than it was and cut that
// much real content.

// realTabs rebuilds the reported bar: the labels from the screenshot, with the markers a live
// session actually carries.
func realTabs() []string {
	labels := []string{
		"XERO RECEIPTS", "ACR ODOO MCP", "LANDSEARCH", "FLEET MANAGER", "HOOPS DASHBOARD",
		"Partyline Original", "DARCYRENO.COM WEBSITE", "ACR BACKLOG !", "Syetems Capital",
	}
	tabs := make([]string, 0, len(labels))
	for i, l := range labels {
		mark := ""
		if i == 5 { // one session attached to a thread, as in the screenshot
			mark = "\x1b[38;5;39m☎\x1b[38;5;250m"
		}
		tabs = append(tabs, "\x1b[48;5;236m\x1b[38;5;46m●\x1b[38;5;250m"+mark+" "+
			string(rune('1'+i))+" "+l+" \x1b[0m")
	}
	return tabs
}

const launcher = "\x1b[48;5;236m\x1b[38;5;250m ⌂ launcher \x1b[0m"

func TestTheLauncherSurvivesANineSessionRibbon(t *testing.T) {
	// THE regression. 200 columns is a normal wide terminal — and the reported bar fit in one.
	bar := fitRibbon("☎ ", realTabs(), launcher, " ", 200)
	if !strings.Contains(bar, "⌂ launcher") {
		t.Fatal("the launcher was dropped — it is the only way to reach the launcher by arrowing")
	}
	if w := brand.VisWidth(bar); w > 200 {
		t.Fatalf("bar is %d columns wide on a 200-column row", w)
	}
}

func TestTheLauncherSurvivesEvenWhenTabsMustBeDropped(t *testing.T) {
	// A narrow terminal has to lose something. It must never be the door.
	for _, cols := range []int{40, 60, 80, 100} {
		bar := fitRibbon("☎ ", realTabs(), launcher, " ", cols)
		if !strings.Contains(bar, "⌂ launcher") {
			t.Errorf("cols=%d: launcher dropped", cols)
		}
		if w := brand.VisWidth(bar); w > cols {
			t.Errorf("cols=%d: bar is %d columns", cols, w)
		}
	}
}

func TestDroppedTabsAreCountedRatherThanHidden(t *testing.T) {
	// Showing fewer tabs than exist, silently, is how you conclude a session died. The numbers
	// still address them, so "+N" is actionable.
	bar := fitRibbon("☎ ", realTabs(), launcher, " ", 50)
	if !strings.Contains(bar, "+") {
		t.Error("tabs were dropped with no indication that more exist")
	}
}

func TestTheClipperNeverEmitsAPartialRune(t *testing.T) {
	// The stray glyph at the right edge of the screenshot. A cut inside a multi-byte character
	// produces a replacement box, which is exactly what was visible.
	src := "☎ ●1 one ●2 two ⌂ launcher"
	for w := 1; w <= brand.VisWidth(src)+3; w++ {
		got := clipANSI(src, w)
		if strings.ContainsRune(got, '�') {
			t.Fatalf("w=%d produced a replacement rune: %q", w, got)
		}
		if brand.VisWidth(got) > w {
			t.Fatalf("w=%d produced %d columns", w, brand.VisWidth(got))
		}
	}
}

func TestTheBarIsMeasuredInColumnsNotBytes(t *testing.T) {
	// The root cause, pinned. One tab from the real bar: 19 columns, 23 bytes. Measuring bytes
	// over-counts by 4 per tab — ~38 across the reported row, which is more than the launcher's
	// whole width.
	tab := realTabs()[0]
	cols := visCols(tab)
	bytes := 0
	for i := 0; i < len(tab); {
		if tab[i] == 0x1b {
			for i < len(tab) && tab[i] != 'm' {
				i++
			}
			if i < len(tab) {
				i++
			}
			continue
		}
		bytes++
		i++
	}
	if cols >= bytes {
		t.Skip("this tab has no multi-byte glyphs — fixture no longer exercises the bug")
	}
	if cols != brand.VisWidth(tab) {
		t.Fatalf("visCols disagrees with the app-wide metric: %d vs %d", cols, brand.VisWidth(tab))
	}
	t.Logf("one tab: %d columns, %d bytes — the old metric over-counted by %d", cols, bytes, bytes-cols)
}

// The tests above exercise fitRibbon directly, which proves the LAYOUT and nothing about whether
// drawBar uses it. That gap is exactly how three earlier fixes in this codebase shipped with tests
// that passed on the broken code. This one drives the real drawBar and reads the bytes it produced.
func TestDrawBarKeepsTheLauncherWithNineSessions(t *testing.T) {
	labels := []string{
		"XERO RECEIPTS", "ACR ODOO MCP", "LANDSEARCH", "FLEET MANAGER", "HOOPS DASHBOARD",
		"Partyline Original", "DARCYRENO.COM WEBSITE", "ACR BACKLOG !", "Syetems Capital",
	}
	// 180 columns, deliberately: the nine real tabs plus the launcher measure 199, so a 200-column
	// row happens to fit and would prove nothing. 180 is an ordinary terminal and genuinely
	// overflows — which is the case that lost the launcher.
	mx := &Mux{wakeR: -1, wakeW: -1, mode: modeLive, cols: 180, rows: 40}
	for i, l := range labels {
		mx.children = append(mx.children, &child{key: "s-" + string(rune('1'+i)), label: l})
	}
	mx.active = 0
	mx.drawBar()

	mx.outMu.Lock()
	bar := string(mx.barBytes)
	mx.outMu.Unlock()

	if !strings.Contains(bar, "launcher") {
		t.Fatal("drawBar dropped the launcher on a 9-session ribbon — the reported bug")
	}
	// Every session must still be addressable: the numbers are how ctrl-\\ <n> reaches them.
	for i := range labels {
		if !strings.Contains(bar, " "+string(rune('1'+i))+" ") {
			t.Errorf("tab %d is missing from the bar", i+1)
		}
	}
}

func TestDrawBarNeverOverflowsTheRow(t *testing.T) {
	// Overflow is not merely ugly: a bar wider than the terminal wraps, which pushes the reserved
	// bottom row up and corrupts the pane above it.
	for _, cols := range []int{60, 80, 120, 200} {
		mx := &Mux{wakeR: -1, wakeW: -1, mode: modeLive, cols: cols, rows: 40}
		for i := 0; i < 9; i++ {
			mx.children = append(mx.children, &child{key: "s", label: "SOME PROJECT NAME"})
		}
		mx.drawBar()
		mx.outMu.Lock()
		bar := string(mx.barBytes)
		mx.outMu.Unlock()
		// Strip the cursor-save/restore and positioning wrapper; measure only the painted row.
		i, j := strings.Index(bar, "\x1b[2K"), strings.LastIndex(bar, "\x1b8")
		if i < 0 || j < 0 || j < i {
			t.Fatalf("cols=%d: unexpected bar envelope", cols)
		}
		if w := brand.VisWidth(bar[i+4 : j]); w > cols {
			t.Errorf("cols=%d: painted row is %d columns — it will wrap", cols, w)
		}
	}
}
