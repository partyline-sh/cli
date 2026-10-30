package main

import (
	"fmt"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// Rows count backwards from the end, so loading an older page preserves the anchor.
type chatCell struct{ row, col int }
type chatSelection struct {
	anchor, end     chatCell
	dragging, moved bool
}

func (m chatPopup) historyCell(x, y int) (chatCell, bool) {
	all, room, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
	row := len(all) - m.clampScroll(m.scroll, len(all), room) - room + y
	return chatCell{row - len(all), min(max(x-m.margin(), 0), m.width()-1)}, y >= 0 && y < room && row >= 0 && row < len(all)
}

func (m chatPopup) dragTo(x, y int) chatPopup {
	if !m.selection.dragging {
		return m
	}
	_, room, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
	if room == 0 {
		return m
	}
	if y < 0 {
		m = m.scrollBy(1)
	}
	if y >= room {
		m = m.scrollBy(-1)
	}
	if p, ok := m.historyCell(x, min(max(y, 0), room-1)); ok {
		m.selection.end = p
		m.selection.moved = m.selection.moved || p != m.selection.anchor
	}
	return m
}

func (s chatSelection) bounds(row, width int) (int, int) {
	if !s.moved {
		return 0, 0
	}
	a, b := s.anchor, s.end
	if a.row > b.row || a.row == b.row && a.col > b.col {
		a, b = b, a
	}
	if row < a.row || row > b.row {
		return 0, 0
	}
	left, right := 0, width
	if row == a.row {
		left = a.col
	}
	if row == b.row {
		right = b.col + 1
	}
	return left, right
}

func (m chatPopup) selectionText() (string, int) {
	all, _, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
	var lines []string
	for i, line := range all {
		left, right := m.selection.bounds(i-len(all), m.width())
		if right > left {
			plain := xansi.Strip(line)
			if strings.HasPrefix(plain, chatIndent) || strings.HasPrefix(plain, "▎ ") {
				left = max(left, 2)
			}
			lines = append(lines, strings.TrimRight(xansi.Strip(xansi.Cut(line, left, right)), " \t"))
		}
	}
	return strings.Join(lines, "\n"), len(lines)
}

func (m chatPopup) highlight(line string, row int, st gateStyle) string {
	left, right := m.selection.bounds(row, m.width())
	if right <= left {
		return line
	}
	// Strip the selected span so nested ANSI resets cannot override its background.
	plain := xansi.Strip(line)
	if w := xansi.StringWidth(plain); w < right {
		plain += strings.Repeat(" ", right-w)
	}
	return xansi.Cut(line, 0, left) + lipgloss.NewStyle().Background(lipgloss.Color(st.th.Raised)).Render(xansi.Cut(plain, left, right)) + xansi.Cut(line, right, m.width())
}

// chatCopied reports how a copy went, so the hint says what happened rather than what was tried.
type chatCopied struct {
	lines  int
	native bool  // the system clipboard writer took it
	err    error // the writer failed; OSC 52 was still sent
}

// chatClipboardWrite hands text to the system clipboard writer partyline's tmux.conf uses
// (clipboardCmd: pbcopy, wl-copy, xclip, xsel). It reports false when there is none. A variable
// so tests need no clipboard.
var chatClipboardWrite = func(text string) (bool, error) {
	writer := clipboardCmd()
	if writer == "" {
		return false, nil
	}
	cmd := exec.Command("sh", "-c", writer)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("%s: %v %s", strings.Fields(writer)[0], err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// chatCopyText copies text two ways: OSC 52 to the terminal (through tmux's set-clipboard) and
// the system writer, whose result decides the hint.
func chatCopyText(text string) tea.Cmd {
	lines := strings.Count(text, "\n") + 1
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		ok, err := chatClipboardWrite(text)
		return chatCopied{lines: lines, native: ok, err: err}
	})
}

// appendHistory keeps selected cells attached to the same text when messages arrive.
func (m chatPopup) appendHistory(lines ...string) chatPopup {
	if !m.selection.dragging && !m.selection.moved {
		m.lines = append(m.lines, lines...)
		return m
	}
	before, _, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
	m.lines = append(m.lines, lines...)
	after, _, _, _, _, _ := m.layout(gateStyle{tmuxTheme()})
	delta := len(after) - len(before)
	m.selection.anchor.row -= delta
	m.selection.end.row -= delta
	return m
}
