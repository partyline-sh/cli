package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestChatDragCopies(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		m := seededChat(t, 0)
		m.pinned = pinned
		m.lines = []string{"\x1b[31mabcdef\x1b[0m", "ghijkl"}
		var copied []string
		m.copyText = func(s string) tea.Cmd { copied = append(copied, s); return nil }
		update := func(msg tea.Msg) { nm, _ := m.Update(msg); m = nm.(chatPopup) }
		all, room, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
		y := room - 2
		if p, ok := m.historyCell(1, y); !ok || p.row != -2 {
			t.Fatalf("padding mapping %v %v (%d lines)", p, ok, len(all))
		}
		update(tea.MouseClickMsg{X: 1 + m.margin(), Y: y, Button: tea.MouseLeft})
		update(tea.MouseMotionMsg{X: 2 + m.margin(), Y: y + 1, Button: tea.MouseLeft})
		if m.View().Content == xansi.Strip(m.View().Content) {
			t.Fatal("selection has no styling")
		}
		st := gateStyle{tmuxTheme()}
		highlighted := m.highlight(all[len(all)-2], -2, st)
		if highlighted == all[len(all)-2] || xansi.Strip(highlighted) != xansi.Strip(all[len(all)-2]) {
			t.Fatal("highlight changed text or did not render")
		}
		update(tea.MouseReleaseMsg{X: 2 + m.margin(), Y: y + 1, Button: tea.MouseLeft})
		if len(copied) != 1 || copied[0] != "bcdef\nghi" {
			t.Fatalf("copied %q", copied)
		}
		if strings.Contains(m.View().Content, "copied 2 lines") {
			t.Fatal("claimed a copy before the clipboard writer answered")
		}
		// The hint says what the writer did: copied, no writer (OSC 52 only), or failed.
		for _, c := range []struct {
			msg  chatCopied
			want string
		}{
			{chatCopied{lines: 2, err: errors.New("pbcopy: exit 1")}, "copy failed"},
			{chatCopied{lines: 2}, "sent 2 lines to the terminal clipboard"},
			{chatCopied{lines: 2, native: true}, "copied 2 lines"},
		} {
			update(c.msg)
			if v := m.View().Content; !strings.Contains(v, c.want) || (!c.msg.native && strings.Contains(v, "copied 2 lines")) {
				t.Fatalf("after %+v the hint does not say %q", c.msg, c.want)
			}
		}
		m.copiedUntil = time.Now().Add(-time.Second)
		if strings.Contains(m.View().Content, "copied 2 lines") {
			t.Fatal("hint did not expire")
		}
		update(tea.MouseClickMsg{X: 1 + m.margin(), Y: y, Button: tea.MouseLeft})
		update(tea.MouseReleaseMsg{X: 1 + m.margin(), Y: y, Button: tea.MouseLeft})
		if len(copied) != 1 || m.selection.moved {
			t.Fatal("click copied or kept selection")
		}
		update(tea.MouseClickMsg{X: 1 + m.margin(), Y: m.h - 1, Button: tea.MouseLeft})
		if m.selection.dragging {
			t.Fatal("input starts selection")
		}
	}
}

func TestChatDragPastEdges(t *testing.T) {
	m := seededChat(t, 150)
	nm, _ := m.Update(tea.MouseClickMsg{X: 0, Y: 1, Button: tea.MouseLeft})
	m = nm.(chatPopup)
	m = m.dragTo(0, -1)
	if m.scroll != 1 {
		t.Fatal("top drag did not scroll")
	}
	m = m.dragTo(0, m.h)
	if m.scroll != 0 {
		t.Fatal("bottom drag did not scroll")
	}
	// Loading a page preserves selection rows measured from the newest.
	for i := 0; i < 500; i++ {
		m = m.dragTo(0, -1)
	}
	if !m.atStart {
		t.Fatal("drag did not load older history")
	}
}

func TestChatSelectionCellsStayWithText(t *testing.T) {
	m := seededChat(t, 0)
	m.lines = []string{"\x1b[31ma界éz\x1b[0m   ", "next"}
	m.selection = chatSelection{anchor: chatCell{-1, 1}, end: chatCell{-2, 1}, moved: true}
	text, n := m.selectionText()
	if text != "界éz\nne" || n != 2 {
		t.Fatalf("reverse cell selection: %q (%d)", text, n)
	}
	m = m.appendHistory("another message")
	after, _ := m.selectionText()
	if after != text {
		t.Fatalf("arrival moved selection: %q -> %q", text, after)
	}
	m = m.loadOlder([]chatMessage{{Seq: -1, Text: "older"}})
	after, _ = m.selectionText()
	if after != text {
		t.Fatalf("older page moved selection: %q -> %q", text, after)
	}
}
