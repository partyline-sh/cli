package main

import (
	"partyline.sh/partyline/internal/brand"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// launcherScreen centers bounded content on an alternate screen.
func launcherScreen(lines []string, w, h int) tea.View {
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, line := range lines {
		line = ansix.Truncate(line, w, "…")
		lines[i] = strings.Repeat(" ", max(0, (w-ansix.StringWidth(line))/2)) + line
	}
	content := strings.Repeat("\n", max(0, (h-len(lines))/2)) + strings.Join(lines, "\n")
	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(tmuxTheme().Surface)
	return v
}

// legacyChoice preserves headless painting while interactive menus use forms.
func legacyChoice(m formModel, lines []string) rune {
	if !formTerminal() {
		cgBox(m.title, lines)
		return 0
	}
	result := formState(runForm(m))
	if !result.ok {
		return 0
	}
	return result.key
}

// recoveryNote shows a Recover message: a note form in a terminal; with none, the same frame the
// message box always printed (nothing waits for a key without a terminal).
func recoveryNote(title string, lines []string) {
	if !formTerminal() {
		m := cgModal{Title: title, Body: lines, Hints: []brand.Hint{{Key: "⏎ · esc", Label: "back"}}}
		m.paint()
		return
	}
	noteForm(title, lines)
}
