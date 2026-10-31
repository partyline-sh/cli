package main

import (
	"strings"

	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// launcherModal renders shared modal content with the popup's active theme.
func launcherModal(a *aiMenu, st gateStyle) string {
	if a.help {
		lines := []string{st.fg(st.th.Focus).Render("Session launcher · keys"), ""}
		half := (len(aiMenuKeys) + 1) / 2
		width := max(1, (a.w-3)/2)
		for i := 0; i < half; i++ {
			cells := []string{}
			for _, idx := range []int{i, i + half} {
				if idx >= len(aiMenuKeys) {
					continue
				}
				r := aiMenuKeys[idx]
				if r[0] == "q" {
					r[1] = "close launcher popup"
				}
				cell := st.fg(st.th.Focus).Render(r[0]) + "  " + st.text(r[1])
				cells = append(cells, lipgloss.NewStyle().Width(width).Render(ansix.Truncate(cell, width, "…")))
			}
			lines = append(lines, strings.Join(cells, " │ "))
		}
		lines = append(lines, st.muted("press any key to close"))
		return strings.Join(lines, "\n")
	}
	if !a.picking && !a.renaming && a.confirmDelete == nil {
		return ""
	}
	title := " rename session "
	var lines []string
	switch {
	case a.picking:
		tool := ""
		if selected := a.selected(); selected != nil {
			tool = selected.Tool
		}
		title = " resume " + tool + " "
		if a.pickCustom {
			lines = []string{"custom flags:", "  " + a.pickText + " ", "", "⏎ run · esc back"}
		} else {
			for i, mode := range a.pickModes {
				marker, label := "  ", mode.label
				if mode.danger {
					label = "⚠ " + label
				}
				if i == a.pickIdx {
					marker = "▸ "
					if mode.danger && a.pickArmed {
						label = "⚠ " + mode.label + " — ⏎ AGAIN to confirm"
					}
				}
				lines = append(lines, marker+label)
			}
			tail := "↑↓ pick · ⏎ go · esc cancel"
			if a.pickArmed {
				tail = "⚠ ⏎ again to confirm"
			}
			lines = append(lines, "", tail)
		}
	case a.confirmDelete != nil:
		title = " ⚠ delete session "
		lines = deleteModalLines(*a.confirmDelete, a.delFacts)
	case a.renaming:
		was := ""
		if selected := a.selected(); selected != nil {
			was = selected.Title
		}
		lines = renameModalLines(was, a.renameBuf)
	}
	return modalBox(st, a.w, a.h, title, lines)
}

// renameModalLines is the rename box's content, shared by the launcher and the ctrl-\ menu.
func renameModalLines(was, buf string) []string {
	return []string{"was: " + ansix.Truncate(was, 46, ""), "", buf + " ", "", "⏎ save · empty resets · esc cancel"}
}

// modalBox draws a titled box centred in w×h, in the popup's theme.
func modalBox(st gateStyle, w, h int, title string, lines []string) string {
	inner := 34
	for i, line := range lines {
		lines[i] = ansix.Strip(line)
		inner = max(inner, ansix.StringWidth(lines[i])+2)
	}
	inner = min(inner, max(0, w-4))
	rest := max(0, inner-1-(ansix.StringWidth(title)+2))
	rows := []string{"╭─┤" + title + "├" + strings.Repeat("─", rest) + "╮"}
	for _, line := range lines {
		rows = append(rows, "│ "+lipgloss.NewStyle().Width(max(0, inner-2)).Render(ansix.Truncate(line, max(0, inner-2), ""))+" │")
	}
	rows = append(rows, "╰"+strings.Repeat("─", inner)+"╯")
	for i, row := range rows {
		plain := ansix.Strip(row)
		rows[i] = st.text(plain)
		if i == 0 || i == len(rows)-1 || strings.Contains(plain, "▸") {
			rows[i] = st.fg(st.th.Focus).Render(plain)
		}
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, strings.Join(rows, "\n"))
}
