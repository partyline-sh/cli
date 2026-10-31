package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// Drawing the chat popup: the room's history in a viewport that keeps the newest at the bottom,
// then the status lines, a hint or search line, and the input with its cursor.

func (m chatPopup) View() tea.View {
	st := gateStyle{tmuxTheme()}
	all, room, below, rows, curCol, curRow := m.layout(st)
	scroll := m.clampScroll(m.scroll, len(all), room)
	end := len(all) - scroll
	history := append([]string(nil), all[max(end-room, 0):end]...)
	for i := range history {
		history[i] = m.highlight(history[i], max(end-room, 0)+i-len(all), st)
	}
	for len(history) < room {
		history = append([]string{""}, history...)
	}
	// While scrolled back, where you are goes in the blank row under the history: it never changes
	// the history's height, so entering or leaving scroll mode does not move the lines.
	sep := ""
	if scroll > 0 {
		cols := m.width()
		mark := "scrolled back"
		if m.newBelow > 0 {
			mark = fmt.Sprintf("↓ %d new", m.newBelow)
		}
		older := "older: pgup"
		if !m.oldestAt.IsZero() {
			older += " · loaded from " + m.oldestAt.Local().Format("Jan 2 15:04")
		}
		if m.atStart && scroll >= len(all)-room {
			older = "start of the room"
		}
		sep = st.faint(runewidth.Truncate(mark+" · "+older+" · newest: pgdn or esc", cols-1, "…"))
	}
	screen := append(append(append(history, sep), below...), rows...)
	for i := range screen {
		screen[i] = strings.TrimRight(strings.Repeat(" ", m.margin())+screen[i], " ")
	}
	v := tea.NewView(strings.Join(screen, "\n"))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	v.MouseMode = tea.MouseModeCellMotion
	v.Cursor = tea.NewCursor(curCol+m.margin(), room+1+len(below)+curRow)
	return v
}

// clampScroll keeps a scroll within the history: never past its first line, never below the newest.
func (m chatPopup) clampScroll(scroll, lines, room int) int {
	return min(max(scroll, 0), max(lines-room, 0))
}

// layout is the screen's parts: the history as lines (the header first), the rows it may fill,
// and the status lines and input below it with the cursor.
func (m chatPopup) layout(st gateStyle) (all []string, room int, below, rows []string, curCol, curRow int) {
	cols := m.width()
	dim := func(s string) string { return st.faint(runewidth.Truncate(oneRow(s), cols-1, "…")) }
	for _, s := range m.in.status {
		below = append(below, dim(s))
	}
	if time.Now().Before(m.copiedUntil) {
		below = append(below, dim(m.copied))
	} else if m.in.hint != "" {
		below = append(below, dim(m.in.hint))
	}
	if m.in.searching {
		match := "no match"
		if m.in.searchAt >= 0 {
			match = strings.ReplaceAll(m.in.history[m.in.searchAt], "\n", " ⏎ ")
		}
		below = append(below, dim("history: "+match))
	}
	// The @ menu sits right above the input, the highlighted name in the focus colour.
	for i, name := range m.in.menu {
		if i == m.in.menuAt {
			below = append(below, st.fg(st.th.Focus).Bold(true).Render("› "+runewidth.Truncate(name, cols-3, "…")))
		} else {
			below = append(below, st.muted("  "+runewidth.Truncate(name, cols-3, "…")))
		}
	}
	if len(m.in.menu) > 0 {
		below = append(below, dim("↑↓ choose · ⏎ or tab inserts · esc closes the list"))
	}
	rows, curRow, curCol = m.in.layout(cols)
	first := 0
	if len(rows) > chatInputMaxRows { // a long message shows at most this many rows, around the cursor
		first = min(max(0, curRow-chatInputMaxRows/2), len(rows)-chatInputMaxRows)
		rows = rows[first : first+chatInputMaxRows]
		curRow -= first
	}
	prompt := chatPrompt
	if m.in.searching {
		prompt = "search › "
	}
	if first == 0 && len(rows) > 0 {
		rows[0] = st.fg(st.th.Focus).Bold(true).Render(prompt) + strings.TrimPrefix(rows[0], prompt)
	}
	// The input and its status lines never push the screen past the popup: status lines give way
	// first, then the input shows fewer rows around the cursor.
	for len(below) > 0 && len(below)+len(rows)+2 > m.h {
		below = below[1:]
	}
	if extra := len(rows) + 2 - m.h; extra > 0 && len(rows) > 1 {
		keep := max(len(rows)-extra, 1)
		start := min(max(curRow-keep/2, 0), len(rows)-keep)
		rows, curRow = rows[start:start+keep], curRow-start
	}
	// The history fills what is left, newest at the bottom unless scrolled back.
	header := "partyline chat · esc leaves · @ mentions · drag copies · wheel/pgup scroll · /help"
	if m.pinned {
		header = "partyline chat · esc or ctrl-t closes this tab · @ mentions · drag copies · wheel/pgup scroll · /help"
	}
	room = max(m.h-len(below)-len(rows)-1, 0)
	var entries []string
	for _, line := range m.lines {
		if msg, ok := m.sources[line]; ok {
			line = chatFormat(msg, cols)
		} else if strings.HasPrefix(line, sgrDim) {
			line = st.faint(runewidth.Truncate(oneRow(stripColours(line)), cols, "…"))
		}
		entries = append(entries, line)
	}
	all = append([]string{st.faint(runewidth.Truncate(header, cols-1, "…"))}, strings.Split(lipgloss.NewStyle().Width(cols).Render(strings.Join(entries, "\n")), "\n")...)
	return all, room, below, rows, curCol, curRow
}

// chatPopupMain is `ptln chat` (a popup, or any terminal) and `ptln chat --tab` (pinned).
func chatPopupMain(pinned bool) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fatal(fmt.Errorf("ptln chat needs a terminal; use `ptln chat send` from scripts"))
	}
	_ = os.MkdirAll(chatDir(), 0o700)
	_ = withChatLock(func(*chatRoom) error { return nil }) // create the room so hooks see it
	if !pinned {
		defer markChatPopupOpen()()
	}
	// Bubble Tea asks the terminal for modified keys itself on every render with input (it sends
	// modifyOtherKeys level 2 and pushes the kitty keyboard protocol, plus bracketed paste), which is
	// what the old view's modifyKeysOn and pasteOn did; tmux's extended-keys forwards them.
	// TestChatPopupAsksForModifiedKeys reads the program's output for those requests.
	if _, err := tea.NewProgram(newChatPopup(pinned)).Run(); err != nil {
		fatal(err)
	}
}

func chatPopupPIDPath() string { return filepath.Join(stateDir(), "chat-popup.pid") }

// markChatPopupOpen records an open chat popup and the pane it covers: that pane is not being
// looked at, so a wake may type into it. Every other pane, and every pane of another client, is
// checked as before (paneWatched). The covered pane is the client's active pane, asked of tmux:
// TMUX_PANE in a popup is whatever the server's environment carried, not the covered pane
// (checked on tmux 3.7c).
func markChatPopupOpen() func() {
	out, _ := tmuxCmd("display-message", "-p", "#{pane_id}").Output()
	pane := strings.TrimSpace(string(out))
	_ = os.WriteFile(chatPopupPIDPath(), []byte(strconv.Itoa(os.Getpid())+" "+pane), 0o600)
	return func() { _ = os.Remove(chatPopupPIDPath()) }
}

// chatPopupOpen: a chat popup is running now.
func chatPopupOpen() bool {
	b, err := os.ReadFile(chatPopupPIDPath())
	if err != nil {
		return false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return false
	}
	pid, err := strconv.Atoi(f[0])
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

// chatPopupCovers reports whether an open chat popup covers paneID.
func chatPopupCovers(paneID string) bool {
	b, err := os.ReadFile(chatPopupPIDPath())
	if err != nil {
		return false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || f[1] != paneID {
		return false
	}
	pid, err := strconv.Atoi(f[0])
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

// chatPopupCmd is the popup's command line, sized to the client like the approval popup.
func chatPopupCmd(self string) []string {
	pw, ph := chatPopupSize(tmuxClientSize())
	// A double border in the accent colour reads as a window frame you can take hold of: tmux grabs
	// only the border's single cells, so the border itself has to be the obvious handle.
	return []string{"display-popup", "-E", "-w", strconv.Itoa(pw), "-h", strconv.Itoa(ph), "-b", "double",
		"-S", "fg=" + tmuxTheme().Focus, "-T", chatPopupTitle(pw), self, "chat", "--here"}
}

// chatPopupTitle is the popup's border title. It says how to move and resize the popup, which tmux
// does itself and never shows: a left-button drag on any edge moves it, a right-button drag on the
// right or bottom edge resizes it (tmux popup.c; a right-click on the left or top edge opens a menu
// instead). The chat re-wraps to the new width (chatFormat at each layout). Shorter on a narrow
// popup, so tmux never cuts the hint mid-word.
func chatPopupTitle(width int) string {
	for _, t := range []string{
		" chat · drag the frame to move it · RIGHT-drag the right or bottom frame ↘ to resize ",
		" chat · drag frame to move · RIGHT-drag right/bottom frame ↘ to resize ",
		" chat · RIGHT-drag frame ↘ to resize ",
	} {
		if runewidth.StringWidth(t) <= width-4 {
			return t
		}
	}
	return " chat "
}

// chatPopupSize is 150x50 at most, and never larger than the client: tmux refuses a popup bigger
// than its client and shows nothing.
func chatPopupSize(cols, rows int) (int, int) {
	w, h := 150, 50 // 120x40 was cramped; the client's size caps it
	if cols > 0 {
		w = min(w, cols-2)
	}
	if rows > 0 {
		h = min(h, rows-2)
	}
	return max(w, 20), max(h, 8)
}
