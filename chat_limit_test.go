package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatLimitPersistenceAndDelivery(t *testing.T) {
	chatTestRoom(t, chatMember{Key: "agent", Name: "#1 codex", Engine: "codex"})
	post(t, chatMessage{From: "you", Text: "waiting"})
	_ = withChatLock(func(r *chatRoom) error { r.AgentRun = 16; return nil })
	r := loadChatRoom()
	if r.cap() != 16 || !r.held(r.Members[0]) {
		t.Fatal("default must pause at 16")
	}
	if got := chatLimit([]string{"20"}); got != "agent limit set to 20" {
		t.Fatal(got)
	}
	r = loadChatRoom()
	if r.AgentRun != 16 || r.held(r.Members[0]) || len(unreadFor(readChat(), r.Members[0])) != 1 {
		t.Fatal("raise must release pending delivery without resetting count or messages")
	}
	chatLimit([]string{"4"})
	r = loadChatRoom()
	if !r.paused() {
		t.Fatal("lower must pause")
	}
	chatLimit([]string{"off"})
	r = loadChatRoom()
	if r.paused() || r.cap() != -1 {
		t.Fatal("off must persist")
	}
	_ = withChatLock(func(r *chatRoom) error { r.Stopped = true; return nil })
	chatLimit([]string{"24"})
	r = loadChatRoom()
	if !r.paused() {
		t.Fatal("limit must preserve manual stop")
	}
}

func TestChatLimitParsing(t *testing.T) {
	chatTestRoom(t)
	for _, args := range [][]string{nil, {"0"}, {"3"}, {"-1"}, {"1000001"}, {"wat"}, {"16", "20"}} {
		if !strings.HasPrefix(chatLimit(args), "usage:") {
			t.Fatalf("accepted %v", args)
		}
	}
	var said string
	chatSubmit("/limit 32", func(s string) { said = s }, func() {})
	if said != "agent limit set to 32" || len(readChat()) != 0 {
		t.Fatal("limit must be a command, not a message")
	}
}

func TestChatLimitMeterPinnedAndClickable(t *testing.T) {
	m, _ := testChatPopup(t)
	for _, docked := range []bool{false, true} {
		m.docked = docked
		m.in.status = []string{"one", "two", "three", "four"}
		m.h = 8
		m.scroll = 100
		_, room, below, _, _, _ := m.layout(gateStyle{m.motion.theme()})
		if !strings.Contains(stripColours(below[0]), "agents 0/16") {
			t.Fatal("meter scrolled or clipped away")
		}
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: m.margin() + 2, Y: room + 1})
		m = next.(chatPopup)
		if m.room.cap() != 20 {
			t.Fatal("plus did not update room")
		}
		if !m.pickLimitAt(m.margin(), room+1) || m.room.cap() != 16 {
			t.Fatal("minus did not update room")
		}
	}
}

func TestChatLimitLongDraftFitsTinyPanel(t *testing.T) {
	m, _ := testChatPopup(t)
	m.h = 8
	m.in.ed.Set(strings.Repeat("draft\n", 20))
	_, room, below, rows, _, _ := m.layout(gateStyle{m.motion.theme()})
	if room+1+len(below)+len(rows) > m.h {
		t.Fatal("meter and long draft exceed panel height")
	}
	if !strings.Contains(stripColours(below[0]), "agents") {
		t.Fatal("meter missing")
	}
}
