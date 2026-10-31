package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
	uitheme "partyline.sh/partyline/internal/theme"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func testChatPopup(t *testing.T) (chatPopup, *int) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_ = withChatLock(func(*chatRoom) error { return nil })
	pinned := 0
	m := newChatPopup(false)
	m.pin = func() error { pinned++; return nil }
	m.saveTail = func(string) {}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return nm.(chatPopup), &pinned
}

// chatPress sends one key, named as Bubble Tea names it, or typed text.
func chatPress(m chatPopup, k string) (chatPopup, tea.Cmd) {
	msg := chatKeyMsg(k)
	nm, cmd := m.Update(msg)
	return nm.(chatPopup), cmd
}

func chatKeyMsg(k string) tea.KeyPressMsg {
	named := map[string]tea.KeyPressMsg{
		"enter": {Code: tea.KeyEnter}, "shift+enter": {Code: tea.KeyEnter, Mod: tea.ModShift},
		"alt+enter": {Code: tea.KeyEnter, Mod: tea.ModAlt}, "ctrl+enter": {Code: tea.KeyEnter, Mod: tea.ModCtrl},
		"backspace": {Code: tea.KeyBackspace}, "alt+backspace": {Code: tea.KeyBackspace, Mod: tea.ModAlt},
		"ctrl+backspace": {Code: tea.KeyBackspace, Mod: tea.ModCtrl}, "delete": {Code: tea.KeyDelete},
		"left": {Code: tea.KeyLeft}, "right": {Code: tea.KeyRight}, "up": {Code: tea.KeyUp}, "down": {Code: tea.KeyDown},
		"home": {Code: tea.KeyHome}, "end": {Code: tea.KeyEnd}, "tab": {Code: tea.KeyTab}, "esc": {Code: tea.KeyEscape},
		"alt+left": {Code: tea.KeyLeft, Mod: tea.ModAlt}, "alt+right": {Code: tea.KeyRight, Mod: tea.ModAlt},
		"ctrl+left": {Code: tea.KeyLeft, Mod: tea.ModCtrl}, "ctrl+right": {Code: tea.KeyRight, Mod: tea.ModCtrl},
	}
	if msg, ok := named[k]; ok {
		return msg
	}
	if r, ok := strings.CutPrefix(k, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(r[0]), Mod: tea.ModCtrl}
	}
	if r, ok := strings.CutPrefix(k, "alt+"); ok {
		return tea.KeyPressMsg{Code: rune(r[0]), Mod: tea.ModAlt}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

func chatType(m chatPopup, s string) chatPopup {
	for _, r := range s {
		m, _ = chatPress(m, string(r))
	}
	return m
}

// Every chat key in docs/plans/ui-key-inventory.md keeps its action in the popup.
func TestChatPopup(t *testing.T) {
	for _, c := range []struct {
		name  string
		typed string
		keys  []string
		text  string // the input afterwards
		cur   int    // the cursor afterwards, -1 to skip
	}{
		{"shift+enter makes a new line", "a", []string{"shift+enter"}, "a\n", -1},
		{"alt+enter makes a new line", "a", []string{"alt+enter"}, "a\n", -1},
		{"ctrl+enter makes a new line", "a", []string{"ctrl+enter"}, "a\n", -1},
		{"backslash then enter makes a new line", "a\\", []string{"enter"}, "a\n", -1},
		{"left and right", "ab", []string{"left", "left", "right"}, "ab", 1},
		{"ctrl+b and ctrl+f", "ab", []string{"ctrl+b", "ctrl+b", "ctrl+f"}, "ab", 1},
		{"home and end", "ab", []string{"home"}, "ab", 0},
		{"ctrl+e goes to the end", "ab", []string{"ctrl+a", "ctrl+e"}, "ab", 2},
		{"alt+left by word", "one two", []string{"alt+left"}, "one two", 4},
		{"alt+b by word", "one two", []string{"alt+b"}, "one two", 4},
		{"ctrl+left by word", "one two", []string{"ctrl+left"}, "one two", 4},
		{"alt+right by word", "one two", []string{"home", "alt+right"}, "one two", 3},
		{"alt+f by word", "one two", []string{"home", "alt+f"}, "one two", 3},
		{"ctrl+right by word", "one two", []string{"home", "ctrl+right"}, "one two", 3},
		{"backspace", "ab", []string{"backspace"}, "a", -1},
		{"delete", "ab", []string{"left", "delete"}, "a", -1},
		{"ctrl+d deletes forward", "ab", []string{"left", "ctrl+d"}, "a", -1},
		{"ctrl+w deletes a word", "one two", []string{"ctrl+w"}, "one ", -1},
		{"alt+backspace deletes a word", "one two", []string{"alt+backspace"}, "one ", -1},
		{"ctrl+backspace deletes a word", "one two", []string{"ctrl+backspace"}, "one ", -1},
		{"alt+d deletes a word forward", "one two", []string{"home", "alt+d"}, " two", -1},
		{"ctrl+u kills to the start", "one two", []string{"ctrl+u"}, "", -1},
		{"ctrl+k kills to the end", "one two", []string{"home", "ctrl+k"}, "", -1},
		{"ctrl+u alone kills", "ab", []string{"ctrl+u"}, "", -1},
		{"ctrl+z undoes the kill", "ab", []string{"ctrl+u", "ctrl+z"}, "ab", -1},
		{"ctrl+_ undoes the kill", "ab", []string{"ctrl+u", "ctrl+_"}, "ab", -1},
		{"ctrl+a goes to the start", "ab", []string{"ctrl+a"}, "ab", 0},
		{"down moves to the next line", "a", []string{"shift+enter", "b", "up", "down"}, "a\nb", 3},
		{"shift+backspace deletes a word", "one two", []string{"shift+backspace"}, "one ", -1},
		{"tab completes a command", "/he", []string{"tab"}, "/help ", -1},
		{"ctrl+u clears the draft", "draft", []string{"ctrl+u"}, "", -1},
	} {
		m, _ := testChatPopup(t)
		m = chatType(m, c.typed)
		for _, k := range c.keys {
			m, _ = chatPress(m, k)
		}
		if got := m.in.ed.String(); got != c.text {
			t.Errorf("%s: input %q, want %q", c.name, got, c.text)
		}
		if c.cur >= 0 && m.in.ed.cur != c.cur {
			t.Errorf("%s: cursor %d, want %d", c.name, m.in.ed.cur, c.cur)
		}
	}

	// ⏎ sends; ↑ brings it back; esc while browsing history returns to the draft.
	m, _ := testChatPopup(t)
	m = chatType(m, "hello room")
	m, _ = chatPress(m, "enter")
	if msgs := readChat(); len(msgs) == 0 || msgs[len(msgs)-1].Text != "hello room" {
		t.Fatalf("enter did not send: %+v", msgs)
	}
	m = chatType(m, "draft")
	m, _ = chatPress(m, "up")
	if m.in.ed.String() != "hello room" {
		t.Errorf("up: %q", m.in.ed.String())
	}
	m, _ = chatPress(m, "esc")
	if m.in.ed.String() != "draft" {
		t.Errorf("esc while browsing history: %q", m.in.ed.String())
	}
	m, _ = chatPress(m, "ctrl+r")
	if !m.in.searching {
		t.Error("ctrl+r did not start a history search")
	}
	m, _ = chatPress(m, "esc")

	// ctrl+l clears the screen; ctrl+g opens the editor; ctrl+d on an empty line and ctrl+c leave.
	m, _ = chatPress(m, "ctrl+l")
	if len(m.lines) != 0 {
		t.Error("ctrl+l did not clear the screen")
	}
	if _, cmd := chatPress(m, "ctrl+g"); cmd == nil {
		t.Error("ctrl+g did not open the editor")
	}
	m.in.ed.Set("")
	if _, cmd := chatPress(m, "ctrl+d"); cmd == nil || cmd() != tea.Quit() {
		t.Error("ctrl+d on an empty line did not leave")
	}
	if _, cmd := chatPress(m, "ctrl+c"); cmd == nil || cmd() != tea.Quit() {
		t.Error("ctrl+c did not leave")
	}

	// A paste is one edit: one undo removes it.
	m.in.ed.Set("")
	nm, _ := m.Update(tea.PasteMsg{Content: "pasted\ttext"})
	m = nm.(chatPopup)
	if m.in.ed.String() != "pasted    text" {
		t.Errorf("paste: %q", m.in.ed.String())
	}
	if m, _ = chatPress(m, "ctrl+z"); m.in.ed.String() != "" {
		t.Errorf("one undo did not remove the paste: %q", m.in.ed.String())
	}

	// ctrl+t pins the room as a tab and closes the popup.
	m, pinned := testChatPopup(t)
	if _, cmd := chatPress(m, "ctrl+t"); *pinned != 1 || cmd == nil || cmd() != tea.Quit() {
		t.Errorf("ctrl+t: pinned %d", *pinned)
	}
	// In the tab, ctrl+t unpins: it leaves without opening another tab.
	m, pinned = testChatPopup(t)
	m.pinned = true
	if _, cmd := chatPress(m, "ctrl+t"); *pinned != 0 || cmd == nil {
		t.Errorf("ctrl+t in the tab opened another (%d)", *pinned)
	}
}

// The room at 80x24, 120x38 and 60x20; Partyline dark and light draw it on their own backgrounds.
func TestChatPopupGolden(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 38}, {60, 20}} {
		m, _ := testChatPopup(t)
		m = chatLayoutFixture(m)
		m.in.status = []string{"codex-1 working · 2m"}
		m = chatType(m, "thanks")
		nm, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		got := gateANSI.ReplaceAllString(nm.(chatPopup).View().Content, "")
		golden := fmt.Sprintf("testdata/chat-popup-%dx%d.golden", size[0], size[1])
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
		if lines := strings.Count(got, "\n") + 1; lines > size[1] {
			t.Errorf("%dx%d: %d lines, taller than the popup", size[0], size[1], lines)
		}
	}
	m, _ := testChatPopup(t)
	dark := m.View().BackgroundColor
	_ = saveBackground(themeBackground{Choice: "light"})
	if m.View().BackgroundColor == dark {
		t.Error("Partyline light draws the room on the same background as dark")
	}
}

// The popup asks the terminal for modified keys (Shift-⏎, Ctrl-⏎, Option-arrows) and bracketed
// paste: Bubble Tea sends modifyOtherKeys level 2 and the kitty keyboard push on its first render
// with input, which is what the old view's modifyKeysOn and pasteOn did.
func TestChatPopupAsksForModifiedKeys(t *testing.T) {
	m, _ := testChatPopup(t)
	in, inW := io.Pipe()
	defer inW.Close()
	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(&out), tea.WithWindowSize(80, 24))
	go func() { time.Sleep(300 * time.Millisecond); p.Quit() }()
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	for name, seq := range map[string]string{"modifyOtherKeys level 2": "\x1b[>4;2m", "kitty keyboard push": "\x1b[>1u", "bracketed paste": "\x1b[?2004h"} {
		if !strings.Contains(out.String(), seq) {
			t.Errorf("the popup does not ask for %s (%q)", name, seq)
		}
	}
}

// Only the pane an open chat popup covers counts as not being looked at.
func TestChatPopupCoversOnePane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = os.WriteFile(chatPopupPIDPath(), []byte(fmt.Sprintf("%d %%5", os.Getpid())), 0o600)
	if !chatPopupCovers("%5") {
		t.Error("the covered pane is not recognised")
	}
	if chatPopupCovers("%6") {
		t.Error("another pane counts as covered")
	}
	_ = os.WriteFile(chatPopupPIDPath(), []byte("999999 %5"), 0o600)
	if chatPopupCovers("%5") {
		t.Error("a popup that is gone still covers its pane")
	}
}

// The history scrolls with pgup/pgdn; the screen never grows past the popup, even with many status
// lines and a long message; "X is free" notes from one watcher tick all show.
func TestChatPopupFitsAndScrolls(t *testing.T) {
	m, _ := testChatPopup(t)
	for i := 0; i < 60; i++ {
		m.lines = append(m.lines, fmt.Sprintf("line %d", i))
	}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = nm.(chatPopup)
	if !strings.Contains(m.View().Content, "line 59") {
		t.Error("the newest line is not shown")
	}
	m, _ = chatPress(m, "pgup")
	if strings.Contains(m.View().Content, "line 59") {
		t.Error("pgup did not scroll back")
	}
	m, _ = chatPress(m, "pgdown")
	if !strings.Contains(m.View().Content, "line 59") {
		t.Error("pgdown did not come back to the newest")
	}
	m.in.status = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	m.in.ed.Set(strings.Repeat("word ", 200))
	if n := strings.Count(m.View().Content, "\n") + 1; n > 12 {
		t.Errorf("%d lines in a 12-line popup", n)
	}

	saveWatcherState(watcherState{Notes: []watcherNote{
		{At: m.opened.Add(time.Second), Text: "codex-1 is free"},
		{At: m.opened.Add(time.Second), Text: "claude-2 is free"},
	}})
	nm, _ = m.Update(chatTick{})
	m = nm.(chatPopup)
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "codex-1 is free") || !strings.Contains(joined, "claude-2 is free") {
		t.Errorf("notes from one tick: %q", joined[max(len(joined)-80, 0):])
	}
	if got := chatPopupCmd("ptln"); got[len(got)-1] != "--here" {
		t.Errorf("the popup does not run the room in place: %q", got)
	}
	if w, h := chatPopupSize(40, 14); w != 38 || h != 12 {
		t.Errorf("40x14 client: popup %dx%d", w, h)
	}
}

// Typing @ opens a list of who can be mentioned; ↑↓ choose, ⏎ inserts without sending, typing
// filters, and Esc closes the list before it closes the chat.
func TestChatMentionMenu(t *testing.T) {
	m, _ := testChatPopup(t)
	m.in.complete = func(token, _ string) []string {
		var out []string
		for _, c := range []string{"@all", "@claude", "@codex", "@13"} {
			if strings.HasPrefix(c, token) && c != token {
				out = append(out, c)
			}
		}
		return out
	}
	m = chatType(m, "hi @")
	if got := strings.Join(m.in.menu, " "); got != "@all @claude @codex @13" {
		t.Fatalf("@ opened %q", got)
	}
	if v := m.View().Content; !strings.Contains(v, "› @all") || !strings.Contains(v, "↑↓ choose") {
		t.Errorf("the list is not drawn:\n%s", v)
	}
	m, _ = chatPress(m, "down")
	m, cmd := chatPress(m, "enter")
	if m.in.ed.String() != "hi @claude " || len(m.in.menu) != 0 || cmd != nil {
		t.Errorf("enter on the second entry: %q, menu %v, sent %v", m.in.ed.String(), m.in.menu, cmd != nil)
	}
	if msgs := readChat(); len(msgs) != 0 {
		t.Errorf("choosing a name sent the message: %+v", msgs)
	}
	m = chatType(m, "@c")
	if got := strings.Join(m.in.menu, " "); got != "@claude @codex" {
		t.Errorf("typing filters: %q", got)
	}
	m, cmd = chatPress(m, "esc")
	if len(m.in.menu) != 0 || cmd != nil {
		t.Errorf("the first esc should close the list, not the chat")
	}
	m = chatType(m, "o")
	if len(m.in.menu) != 0 {
		t.Errorf("a list esc closed reopened while typing the same mention: %v", m.in.menu)
	}
	m = chatType(m, " @")
	if len(m.in.menu) == 0 {
		t.Error("a new mention should open the list again")
	}
	m, _ = chatPress(m, "esc")
	if _, cmd = chatPress(m, "esc"); cmd == nil || cmd() != tea.Quit() {
		t.Error("with the list closed, esc should close the chat")
	}
}

// The popup is up to 150x50, and never larger than the client.
func TestChatPopupIsBigger(t *testing.T) {
	if w, h := chatPopupSize(200, 60); w != 150 || h != 50 {
		t.Errorf("200x60 client: %dx%d", w, h)
	}
	if w, h := chatPopupSize(130, 45); w != 128 || h != 43 {
		t.Errorf("130x45 client: %dx%d", w, h)
	}
}

// The popup's title says how to move and resize it, which tmux supports but never shows, and it
// fits the popup's border at every width.
func TestChatPopupTitleShowsMoveAndResize(t *testing.T) {
	if got := chatPopupTitle(120); !strings.Contains(got, "drag the frame to move it") || !strings.Contains(got, "RIGHT-drag the right or bottom frame ↘ to resize") {
		t.Errorf("wide title: %q", got)
	}
	for _, w := range []int{20, 40, 60, 70, 84, 120} {
		if got := chatPopupTitle(w); runewidth.StringWidth(got) > w-4 || !strings.HasPrefix(got, " chat") {
			t.Errorf("width %d: %q does not fit", w, got)
		}
	}
}

// The selection is the accent tinted over the background: visible, the text still readable.
func TestChatSelectionIsVisible(t *testing.T) {
	th := uitheme.Theme{Tokens: uitheme.Tokens{Focus: "#ff5e8a", Surface: "#232220"}}
	if got := chatSelectionColour(th); got != "#653440" {
		t.Errorf("selection colour %s", got)
	}
	if mixHex("#000000", "#ffffff", 0.5) != "#808080" || mixHex("bad", "#ffffff", 0.5) != "bad" {
		t.Error("mixHex")
	}
}
