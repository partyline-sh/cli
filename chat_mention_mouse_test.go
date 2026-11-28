package main

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatMentionClickInsertsWithoutSending(t *testing.T) {
	m, _ := testChatPopup(t)
	m.in.complete = func(string, string) []string { return []string{"@13", "@codex"} }
	m = chatType(m, "please @co")
	_, room, below, _, _, _ := m.layout(gateStyle{m.motion.theme()})
	y := room + len(below) - len(m.in.menu) + 1
	saved := ""
	m.saveTail = func(text string) { saved = text }
	n, cmd := m.Update(tea.MouseClickMsg{X: m.margin() + 3, Y: y, Button: tea.MouseLeft})
	m = n.(chatPopup)
	if cmd != nil || m.in.ed.String() != "please @codex " || saved != m.in.ed.String() {
		t.Fatalf("mention click: %q", m.in.ed.String())
	}
	if len(readChat()) != 0 || m.selection.dragging {
		t.Fatal("mention click sent text or started history selection")
	}
}
func TestChatMentionClickUsesClippedMenuRows(t *testing.T) {
	m, _ := testChatPopup(t)
	m.h = 8
	m.in.complete = func(string, string) []string { return []string{"@10", "@11", "@12", "@13", "@14", "@15"} }
	m = chatType(m, "@")
	for i := 0; i < 12; i++ {
		m.in.status = append(m.in.status, fmt.Sprint(i))
	}
	_, room, below, _, _, _ := m.layout(gateStyle{m.motion.theme()})
	start := room + len(below) - len(m.in.menu)
	y := max(start, room+1)
	want := m.in.menu[y-start] + " "
	if !m.pickMentionAt(m.margin()+1, y) || m.in.ed.String() != want {
		t.Fatal("clipped suggestion selected the wrong member")
	}
}
