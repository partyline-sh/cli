package main

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"

	uitheme "partyline.sh/partyline/internal/theme"
)

// View draws the launcher as the old switchboard did, in the chosen theme: a header with the
// counts, the session tree in a titled panel beside a detail panel, and the keys under both.
func (m launcherPopup) View() tea.View {
	a := m.menu
	st := gateStyle{tmuxTheme()}
	w, h := a.w, a.h
	var lines []string
	if modal := launcherModal(a, st); modal != "" {
		lines = strings.Split(modal, "\n")
	} else {
		left := max(24, w*11/20)
		right := max(20, w-left-1)
		inner := max(1, a.bodyH())
		list := launcherPanel(st, launcherListTitle(a), !a.focusR, launcherListLines(a, st, left-2, inner), left, inner)
		detail := launcherPanel(st, "detail", a.focusR, launcherDetailLines(a, st, right-4, inner), right, inner)
		lines = append([]string{launcherHeader(a, st)}, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, list, " ", detail), "\n")...)
		lines = append(lines, launcherFooter(a, st))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = ansix.Truncate(lines[i], w, "")
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// launcherHeader is the counts the old banner carried: projects, sessions, and how many are live.
func launcherHeader(a *aiMenu, st gateStyle) string {
	projects, live := map[string]bool{}, 0
	for _, s := range a.view {
		projects[s.projKey()] = true
		if a.live[s.ID] || s.Status == "active" || s.Status == "waiting" {
			live++
		}
	}
	line := fmt.Sprintf(" %d projects · %d sessions", len(projects), len(a.view))
	if live > 0 {
		line += fmt.Sprintf(" · %d live", live)
	}
	if hidden := a.hiddenAgentCount(); hidden > 0 && !a.showAll {
		line += fmt.Sprintf(" · %d automated hidden (a)", hidden)
	}
	out := st.muted(line)
	if up := a.updateLine; up != "" {
		out += "   " + st.fg(st.th.Focus).Render(up)
	}
	return out
}

// launcherListTitle names the list's panel: the sort, or the search while one is typed.
func launcherListTitle(a *aiMenu) string {
	if a.filter || a.query != "" {
		return "/ " + a.query
	}
	return a.sort.label()
}

// launcherPanel draws lines in a rounded frame with its title in the top border; the focused
// panel's title is in the focus colour.
func launcherPanel(st gateStyle, title string, focused bool, lines []string, w, inner int) string {
	border := st.fg(st.th.Border)
	label := st.muted(title)
	if focused {
		label = st.fg(st.th.Focus).Bold(true).Render(title)
	}
	fill := max(0, w-7-ansix.StringWidth(title)) // "╭─┤ " + title + " ├" + fill + "╮" is w wide
	out := []string{border.Render("╭─┤ ") + label + border.Render(" ├"+strings.Repeat("─", fill)+"╮")}
	for i := 0; i < inner; i++ {
		l := ""
		if i < len(lines) {
			l = ansix.Truncate(lines[i], w-2, "…")
		}
		out = append(out, border.Render("│")+l+strings.Repeat(" ", max(0, w-2-ansix.StringWidth(l)))+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(out, "\n")
}

// launcherListLines is the visible window of the tree, the selected row on the raised background.
func launcherListLines(a *aiMenu, st gateStyle, w, inner int) []string {
	if len(a.rows) == 0 {
		return []string{st.muted(" no sessions match")}
	}
	var out []string
	for i := a.top; i < len(a.rows) && len(out) < inner; i++ {
		r, sel := a.rows[i], i == a.cursor
		var row string
		if r.header() {
			row = launcherHeaderRow(a, st, r, sel)
		} else {
			row = launcherSessionRow(a, st, r, sel)
		}
		if sel {
			row = lipgloss.NewStyle().Background(lipgloss.Color(st.th.Raised)).Width(w).Render(row)
		}
		out = append(out, row)
	}
	return out
}

// launcherDot is a status dot: working (live or active), needs you (waiting), or idle.
func launcherDot(st gateStyle, active, waiting bool) string {
	switch {
	case active:
		return st.fg(st.th.Colour(uitheme.Working)).Render("●")
	case waiting:
		return st.fg(st.th.Colour(uitheme.NeedsYou)).Render("●")
	}
	return st.faint("●")
}

// launcherHeaderRow is a project: the fold arrow, the group's busiest status, its name and count.
func launcherHeaderRow(a *aiMenu, st gateStyle, r treeRow, sel bool) string {
	active, waiting := false, false
	for j := range a.view {
		if s := a.view[j]; rowMember(r, &s) {
			active = active || a.live[s.ID] || s.Status == "active"
			waiting = waiting || s.Status == "waiting"
		}
	}
	arrow := "▸"
	if a.expandedRow(r) {
		arrow = "▾"
	}
	name, indent := projLabel(r.proj), " "
	if r.group {
		name, indent = "⤷ worktrees", "   "
	}
	nameStyle := st.fg(st.th.Text)
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	return indent + st.faint(arrow) + " " + launcherDot(st, active, waiting) + " " + nameStyle.Render(name) + st.faint(fmt.Sprintf(" (%d)", r.count))
}

// launcherSessionRow is a session under its project: status dot, age, the engine in its colour,
// marks (pinned, worktree, adopted home) and the title.
func launcherSessionRow(a *aiMenu, st gateStyle, r treeRow, sel bool) string {
	s := a.view[r.sessIdx]
	marker := "   "
	if sel {
		marker = " " + st.fg(st.th.Focus).Bold(true).Render("▸") + " "
	}
	if s.WtName != "" {
		marker = "  " + marker
	}
	dot := launcherDot(st, a.live[s.ID] || s.Status == "active", s.Status == "waiting")
	if a.marked[s.ID] {
		dot = st.fg(st.th.Focus).Render("✓")
	}
	tool := st.th.LegacyForeground(toolColor(s.Tool)) + fmt.Sprintf("%-6s", toolLabel(s.Tool)) + "\x1b[39m"
	marks := ""
	if a.meta[s.ID].Pinned {
		marks += st.fg(st.th.Focus).Render("★") + " "
	}
	if s.WtName != "" {
		marks += st.faint("⤷"+trunc(s.WtName, 20)) + " "
	}
	if s.root != "" {
		marks += st.faint("⌂"+trunc(filepath.Base(s.root), 14)) + " "
	}
	title := st.fg(st.th.Text)
	if sel {
		title = title.Bold(true)
	}
	name := title.Render(a.displayTitle(s))
	if a.displayTitle(s) == "" {
		// Every message in it is one partyline injected (chat wakes, Stop hook feedback): say so
		// rather than leave a blank row.
		name = st.faint("(only partyline's messages)")
	}
	return marker + dot + " " + st.faint(fmt.Sprintf("%4s", humanAge(s.LastActive))) + " " + tool + " " + marks + name
}

// launcherDetailLines is the detail panel: the highlighted session's preview, or for a project a
// summary of its sessions, scrolled from dTop when the panel has focus.
func launcherDetailLines(a *aiMenu, st gateStyle, w, inner int) []string {
	if a.selected() == nil {
		if r := a.curRow(); r != nil && r.header() {
			return launcherProjectSummary(a, st, *r)
		}
	}
	detail := a.detailLines(w)
	a.dLen = len(detail)
	var out []string
	for i := a.dTop; i < len(detail) && len(out) < inner; i++ {
		out = append(out, " "+launcherDetailLine(st, ansix.Strip(detail[i])))
	}
	return out
}

// launcherDetailLine styles one preview line: a field label muted, its value in the text colour.
func launcherDetailLine(st gateStyle, l string) string {
	for _, label := range []string{"location", "started", "active", "model", "branch", "worktree", "status"} {
		if rest, ok := strings.CutPrefix(l, label); ok && strings.HasPrefix(rest, " ") {
			return st.muted(label) + st.text(rest)
		}
	}
	return st.text(l)
}

// launcherProjectSummary is what a project row shows instead of nothing: where it is, how many
// sessions, how many are live or waiting, and when it was last used.
func launcherProjectSummary(a *aiMenu, st gateStyle, r treeRow) []string {
	n, live, waiting := 0, 0, 0
	var last aiSession
	for j := range a.view {
		s := a.view[j]
		if !rowMember(r, &s) {
			continue
		}
		n++
		if a.live[s.ID] || s.Status == "active" {
			live++
		}
		if s.Status == "waiting" {
			waiting++
		}
		if s.LastActive.After(last.LastActive) {
			last = s
		}
	}
	out := []string{" " + st.fg(st.th.Text).Bold(true).Render(projLabel(r.proj)), ""}
	out = append(out, " "+st.muted("location ")+st.text(shortHome(r.proj)))
	out = append(out, " "+st.muted("sessions ")+st.text(fmt.Sprint(n)))
	if live+waiting > 0 {
		out = append(out, " "+st.muted("live     ")+st.text(fmt.Sprintf("%d working · %d need you", live, waiting)))
	}
	if n > 0 {
		out = append(out, " "+st.muted("last     ")+st.text(humanAge(last.LastActive)+" ago · "+trunc(a.displayTitle(last), 40)))
	}
	return append(out, "", " "+st.faint("→ or ⏎ expands · | opens two side by side"))
}

// launcherFooter is the keys, each in the focus colour with its label muted; a flash or a
// selection replaces them while it is current.
func launcherFooter(a *aiMenu, st gateStyle) string {
	if a.flash != "" {
		// A flash can carry a session title or a filesystem error: escapes and controls are
		// stripped so they cannot restyle the popup or reach the terminal.
		return " " + st.text(termSafe(ansix.Strip(a.flash)))
	}
	if len(a.marked) > 0 {
		return " " + st.text(fmt.Sprintf("%d selected", len(a.marked))) + "   " + launcherKeys(st, "⏎", "open all in one tab", "space", "toggle", "esc", "clear")
	}
	if a.focusR {
		return " " + launcherKeys(st, "↑↓", "scroll", "tab", "back to the list", "esc", "back")
	}
	return " " + launcherKeys(st, "⏎", "open", "|", "split", "/", "search", "a", "all", "?", "keys", "esc", "back")
}

// launcherKeys renders key/label pairs.
func launcherKeys(st gateStyle, pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, st.fg(st.th.Focus).Bold(true).Render(pairs[i])+" "+st.muted(pairs[i+1]))
	}
	return strings.Join(parts, "   ")
}

// hiddenAgentCount is how many agent-made sessions the list leaves out until `a` shows them.
func (m *aiMenu) hiddenAgentCount() int {
	n := 0
	for _, s := range m.all {
		if isAgentSession(s) || s.DeadWt {
			n++
		}
	}
	return n
}
