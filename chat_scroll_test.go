package main

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// seededChat opens the popup on a room of n messages, "msg 000" the oldest.
func seededChat(t *testing.T, n int) chatPopup {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_ = withChatLock(func(r *chatRoom) error {
		for i := 0; i < n; i++ {
			if _, err := postLocked(r, chatMessage{From: "you", Name: "you", Text: fmt.Sprintf("msg %03d", i)}); err != nil {
				return err
			}
		}
		return nil
	})
	m := newChatPopup(false)
	m.pin, m.saveTail = func() error { return nil }, func(string) {}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	return nm.(chatPopup)
}

var chatMsgOnScreen = regexp.MustCompile(`msg \d{3}`)

func chatVisible(m chatPopup) string {
	return strings.Join(chatMsgOnScreen.FindAllString(m.View().Content, -1), ",")
}

// The whole room is reachable: the popup opens on the newest, pgup pages back to the first message
// and stops there, and pgdn comes back at once rather than unwinding scrolls past the top.
func TestChatScrollWholeHistory(t *testing.T) {
	m := seededChat(t, 500)
	if v := m.View().Content; !strings.Contains(v, "msg 499") || strings.Contains(v, "msg 000") {
		t.Fatal("did not open on the newest")
	}
	for i := 0; i < 500; i++ {
		prev := m.scroll
		if m, _ = chatPress(m, "pgup"); m.atStart && m.scroll == prev {
			break
		}
	}
	if !strings.Contains(m.View().Content, "msg 000") || !m.atStart {
		t.Fatalf("the first message was never reached (atStart %t)", m.atStart)
	}
	top, screen := m.scroll, chatVisible(m)
	if m, _ = chatPress(m, "pgup"); m.scroll != top || chatVisible(m) != screen {
		t.Fatalf("pgup at the top moved: %d -> %d", top, m.scroll)
	}
	if m, _ = chatPress(m, "pgdown"); m.scroll >= top || chatVisible(m) == screen {
		t.Fatal("pgdn at the top did not move back down at once")
	}
}

// Scrolled back, new messages leave the screen where it is and are counted; esc returns to the
// newest; at the newest esc keeps editing the input as before.
func TestChatScrollKeepsPlace(t *testing.T) {
	m := seededChat(t, 100)
	m, _ = chatPress(m, "pgup")
	m, _ = chatPress(m, "pgup")
	before := chatVisible(m)
	_ = withChatLock(func(r *chatRoom) error {
		for i := 100; i < 103; i++ {
			_, _ = postLocked(r, chatMessage{From: "you", Name: "you", Text: fmt.Sprintf("msg %03d", i)})
		}
		return nil
	})
	nm, _ := m.Update(chatTick{})
	m = nm.(chatPopup)
	if after := chatVisible(m); after != before {
		t.Fatalf("new messages moved the view:\n%s\n%s", before, after)
	}
	if !strings.Contains(m.View().Content, "↓ 3 new") {
		t.Fatal("new messages below are not counted")
	}
	m, _ = chatPress(m, "esc")
	if v := m.View().Content; m.scroll != 0 || !strings.Contains(v, "msg 102") || strings.Contains(v, "new ·") {
		t.Fatalf("esc did not return to the newest (scroll %d)", m.scroll)
	}
	m.in.ed.Set("draft")
	m, _ = chatPress(m, "esc")
	m, _ = chatPress(m, "esc")
	if m.in.ed.String() != "" {
		t.Errorf("at the newest, esc esc no longer clears the input: %q", m.in.ed.String())
	}
}

// After a clear, scrolling up brings the cleared messages back, down to the room's first.
func TestChatScrollAfterClear(t *testing.T) {
	m := seededChat(t, 60)
	m = m.cleared()
	if strings.Contains(m.View().Content, "msg 059") {
		t.Fatal("clear left the history on screen")
	}
	for i := 0; i < 200 && !m.atStart; i++ {
		m, _ = chatPress(m, "pgup")
	}
	if !m.atStart || !strings.Contains(strings.Join(m.lines, "\n"), "msg 000") || !strings.Contains(strings.Join(m.lines, "\n"), "msg 059") {
		t.Fatal("the cleared messages did not come back")
	}
}

// Entering scroll mode does not change the history's height: one pgup moves the screen by exactly
// the lines it scrolls.
func TestChatScrollMovesRenderedLines(t *testing.T) {
	m := seededChat(t, 200)
	before := strings.Split(m.View().Content, "\n")
	m, _ = chatPress(m, "pgup")
	after := strings.Split(m.View().Content, "\n")
	n := m.scroll
	for r := 2; r+n < len(after) && r < 6; r++ {
		if before[r] != after[r+n] {
			t.Fatalf("row %d moved by something other than %d lines:\n%q\n%q", r, n, before[r], after[r+n])
		}
	}
}

// Mouse reporting lets the chat handle both wheel scrolling and selection.
func TestChatScrollWheel(t *testing.T) {
	m := seededChat(t, 20)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("cell motion not enabled")
	}
	in, inW := io.Pipe()
	defer inW.Close()
	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(&out), tea.WithWindowSize(80, 24))
	go func() { time.Sleep(300 * time.Millisecond); p.Quit() }()
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[?1002h") {
		t.Fatal("program did not request cell motion")
	}
}

func TestChatWheelScrolls(t *testing.T) {
	TestChatScrollWheel(t)
	m := seededChat(t, 150)
	wheel := func(button tea.MouseButton) { nm, _ := m.Update(tea.MouseWheelMsg{Button: button}); m = nm.(chatPopup) }
	wheel(tea.MouseWheelDown)
	if m.scroll != 0 {
		t.Fatal("scrolled below newest")
	}
	wheel(tea.MouseWheelUp)
	if m.scroll != 3 {
		t.Fatalf("scroll=%d", m.scroll)
	}
	wheel(tea.MouseWheelDown)
	if m.scroll != 0 {
		t.Fatal("down did not move three lines")
	}
	for i := 0; i < 500; i++ {
		wheel(tea.MouseWheelUp)
	}
	top := m.scroll
	if !m.atStart || !strings.Contains(m.View().Content, "msg 000") {
		t.Fatal("did not load first message")
	}
	wheel(tea.MouseWheelUp)
	if m.scroll != top {
		t.Fatal("not clamped at top")
	}
	wheel(tea.MouseWheelDown)
	if m.scroll != top-3 {
		t.Fatal("down from top")
	}
}
