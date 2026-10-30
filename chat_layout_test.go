package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestChatLayout(t *testing.T) {
	m, _ := testChatPopup(t)
	at := time.Date(2026, 10, 27, 12, 34, 0, 0, time.Local)
	m = m.showNew([]chatMessage{
		{Seq: 1, From: "you", Name: "you", At: at, Text: "hello room"},
		{Seq: 2, From: "agent", Name: "#2 claude", At: at, Text: strings.Repeat("word ", 100) + "\n\n```\n" + strings.Repeat("x", 180) + "\n```"},
	}, 0)
	for _, w := range []int{140, 70, 140} {
		nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 50})
		m = nm.(chatPopup)
		want := min(92, w-4)
		if m.width() != want || m.margin() != (w-want)/2 {
			t.Fatalf("column at %d: width %d margin %d", w, m.width(), m.margin())
		}
		all, _, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
		for _, line := range all {
			if xansi.StringWidth(line) > want {
				t.Fatalf("line exceeds %d: %q", want, line)
			}
		}
		view := xansi.Strip(m.View().Content)
		for _, header := range []string{"you · 12:34", "#2 claude · 12:34"} {
			if !strings.Contains(view, strings.Repeat(" ", m.margin())+header) {
				t.Fatalf("missing centered header %q", header)
			}
		}
		if strings.Count(view, "▎") != 1 {
			t.Fatalf("own bar missing or applied to other speaker: %s", view)
		}
		if m.View().Cursor.X != m.margin()+xansi.StringWidth(chatPrompt) {
			t.Fatal("input cursor outside column")
		}
	}
	// Drag across the body, including its decoration and both outer margins.
	m = m.cleared()
	m = m.showNew([]chatMessage{{Seq: 3, From: "you", Name: "you", At: at, Text: "hello room"}}, 0)
	var copied string
	m.copyText = func(s string) tea.Cmd { copied = s; return nil }
	_, room, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
	for _, msg := range []tea.Msg{
		tea.MouseClickMsg{X: 0, Y: room - 1, Button: tea.MouseLeft},
		tea.MouseMotionMsg{X: 139, Y: room - 1, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: 139, Y: room - 1, Button: tea.MouseLeft},
	} {
		nm, _ := m.Update(msg)
		m = nm.(chatPopup)
	}
	if copied != "hello room" {
		t.Fatalf("copy includes decoration: %q", copied)
	}
}
