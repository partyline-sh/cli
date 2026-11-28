package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// View renders a bounded popup using the active tmux theme.
func (m formModel) View() tea.View {
	st := gateStyle{tmuxTheme()}
	w, h := m.w, m.h
	if w == 0 {
		w = 80
	}
	if h == 0 {
		h = 24
	}
	m = m.clean()
	lines := []string{st.fg(st.th.Focus).Bold(true).Render("☎  " + m.title), ""}
	for _, line := range m.body {
		lines = append(lines, st.muted(line))
	}
	for _, r := range m.rows {
		line := st.fg(st.th.Focus).Render(r.key) + "  " + st.text(r.label)
		if r.note != "" {
			line += "  " + st.muted(r.note)
		}
		lines = append(lines, line)
	}
	cursorLine, cursorCol := -1, 0
	hint := "q / esc cancel"
	if m.enterCancel {
		hint = "q / esc / enter cancel"
	}
	switch m.kind {
	case formPick:
		budget := m.pickRows()
		start := min(m.off, max(len(m.items)-budget, 0))
		end := min(len(m.items), start+budget)
		if start > 0 {
			lines = append(lines, st.faint("↑ more"))
		}
		for i := start; i < end; i++ {
			lines = append(lines, st.fg(st.th.Focus).Render(fmt.Sprintf("%2d", i+1))+"  "+st.text(m.items[i]))
		}
		if end < len(m.items) {
			lines = append(lines, st.faint("↓ more"))
		}
		for _, e := range m.extras {
			lines = append(lines, st.fg(st.th.Focus).Render(string(e.Key))+"  "+st.text(e.Label))
		}
		lines = append(lines, st.text(m.label+" › "+m.typed))
		hint = "digits pick · ↑↓ · q / esc / ⏎ cancel" // ⏎ only confirms a typed number on a long list
		if len(m.items) > 9 {
			hint = "N ⏎ pick · ↑↓ · q / esc cancel"
		}
	case formAsk:
		prefix := m.label + " › "
		text := m.typed
		for ansix.StringWidth(prefix+text) > max(1, w-3) && len([]rune(text)) > 0 {
			text = string([]rune(text)[1:])
		}
		cursorLine, cursorCol = len(lines), min(ansix.StringWidth(prefix+text), max(0, w-3))
		lines = append(lines, st.text(prefix+text))
		hint = "⏎ accept · ctrl-u clear · esc cancel · lone q cancels"
		if m.def != "" {
			lines = append(lines, st.muted("enter for "+m.def))
		}
	case formConfirm:
		defaults := "y/N"
		if m.defaultYes {
			defaults = "Y/n"
		}
		lines = append(lines, st.text(m.label+" ["+defaults+"]"))
		hint = "y yes · n no · ⏎ default · q / esc cancel"
	case formNote:
		hint = "any key back"
	}
	lines = append(lines, "", st.faint(hint))
	// Too tall: the body gives way first, so every row you can press stays on screen.
	if over := len(lines) - h; over > 0 && len(m.body) > 0 {
		cut := min(over, len(m.body))
		end := 2 + len(m.body) // the body follows the title and its blank line
		lines = append(lines[:end-cut], lines[end:]...)
	}
	if len(lines) > h {
		lines = append(lines[:max(0, h-1)], st.faint(hint))
	}
	for i := range lines {
		lines[i] = ansix.Truncate(lines[i], max(1, w-2), "…")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	if cursorLine >= 0 && cursorLine < h-1 {
		v.Cursor = tea.NewCursor(cursorCol, cursorLine)
		v.Cursor.Color = lipgloss.Color(st.th.Focus)
	}
	return v
}

// pickRows is how many items the picker shows at once, from the popup's height less its title,
// body, rows, extras, prompt and hint. Update bounds the scroll with it too, so ↓ at the bottom
// does not wind up an offset that ↑ then has to unwind.
func (m formModel) pickRows() int {
	h := m.h
	if h == 0 {
		h = 24
	}
	return max(1, h-(2+len(m.body)+len(m.rows))-len(m.extras)-5)
}

// clean strips escapes and control characters from everything the form prints: a title, label,
// default or row can come from a path, branch or session name, not only a literal.
func (m formModel) clean() formModel {
	c := func(s string) string { return termSafe(ansix.Strip(s)) }
	m.title, m.label, m.def = c(m.title), c(m.label), c(m.def)
	m.body = append([]string(nil), m.body...)
	for i := range m.body {
		m.body[i] = c(m.body[i])
	}
	m.items = append([]string(nil), m.items...)
	for i := range m.items {
		m.items[i] = c(m.items[i])
	}
	m.rows = append([]formRow(nil), m.rows...)
	for i := range m.rows {
		m.rows[i].label, m.rows[i].note = c(m.rows[i].label), c(m.rows[i].note)
	}
	m.extras = append([]formExtra(nil), m.extras...)
	for i := range m.extras {
		m.extras[i].Label = c(m.extras[i].Label)
	}
	return m
}

// askForm collects a line inside a popup.
func askForm(title string, body []string, label, def string) (string, bool) {
	m := formState(runForm(formModel{kind: formAsk, title: title, body: body, label: label, def: def}))
	return m.answer, m.ok
}

// confirmForm asks for explicit consent inside a popup.
func confirmForm(title string, body []string, label string, def bool) (bool, bool) {
	m := formState(runForm(formModel{kind: formConfirm, title: title, body: body, label: label, defaultYes: def}))
	return m.value, m.ok
}

// noteForm displays a dismissable popup.
func noteForm(title string, body []string) {
	runForm(formModel{kind: formNote, title: title, body: body})
}

// run picks an item or an extra letter action.
func (p formModel) run() (int, rune, bool) {
	if len(p.items) == 0 {
		return -1, 0, false
	}
	m := formState(runForm(p))
	if !m.ok || m.key != 0 {
		return -1, m.key, m.ok
	}
	return m.index, 0, true
}

// newRunForm builds the New/Run choices.
func newRunForm() formModel {
	return formModel{kind: formChoice, title: "New / Run", body: []string{"start something new — opens in a new tab"}, enterCancel: true, rows: []formRow{{"1", "new AI session", "claude · codex · gemini · opencode · goose · antigravity"}, {"2", "blank terminal", "just a shell, in a directory you pick"}}}
}

// worktreeForm builds the worktree actions.
func worktreeForm() formModel {
	return formModel{kind: formChoice, title: "Worktrees", enterCancel: true, rows: []formRow{{"f", "fork this session into a worktree", "parallel agent, shared memory"}, {"l", "list this repo's worktrees", ""}, {"r", "remove a worktree", "guarded — never the repo"}}}
}
