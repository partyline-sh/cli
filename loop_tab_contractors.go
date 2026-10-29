package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

func loopsContractors(all []*contractor, now time.Time) []*contractor {
	var out []*contractor
	for _, c := range all {
		if c.Loop != "" && (contractorShown(c) || !c.Ended.IsZero() && now.Sub(c.Ended) <= 24*time.Hour) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.State == "running") != (b.State == "running") {
			return a.State == "running"
		}
		if a.State != "running" && a.Seen != b.Seen {
			return !a.Seen
		}
		if a.State == "running" {
			return a.Started.After(b.Started)
		}
		return a.Ended.After(b.Ended)
	})
	return out
}

func (m *tabModel) refreshCrew() {
	id := ""
	if m.preview != nil {
		id = m.preview.c.ID
	}
	m.crew = loopsContractors(loadContractors(), m.act.now())
	m.crewSel = min(m.crewSel, max(len(m.crew)-1, 0))
	for i, c := range m.crew {
		if c.ID == id {
			m.crewSel = i
		}
	}
	if len(m.crew) == 0 {
		m.preview = nil
		return
	}
	if !m.contractors {
		return
	}
	c := m.crew[m.crewSel]
	if m.preview == nil || m.preview.c.ID != c.ID {
		v := newContractorView(c)
		m.preview = &v
	} else {
		m.preview.c = c
		m.preview.vp.SetContent(contractorOutput(c))
	}
	m.sizePreview()
	if m.preview.tail {
		m.preview.vp.GotoBottom()
	}
}

func (m *tabModel) sizePreview() {
	if m.preview == nil {
		return
	}
	m.preview.vp.SetWidth(max(m.w-4-m.crewWidth()-3, 1))
	m.preview.vp.SetHeight(max(m.h-8, 1))
	if m.preview.tail {
		m.preview.vp.GotoBottom()
	}
}
func (m tabModel) crewWidth() int {
	if m.w < 100 {
		return max(m.w-4, 1)
	}
	return min(48, (m.w-7)/2)
}
func (m tabModel) tabHeading(st gateStyle) string {
	n := 0
	for _, c := range m.crew {
		if contractorShown(c) {
			n++
		}
	}
	board, crew := "Board", fmt.Sprintf("Contractors (%d)", n)
	if m.contractors {
		return st.muted(board) + "   " + st.fg(st.th.Focus).Bold(true).Render(crew) + "   " + st.key("Tab", "switch")
	}
	return st.fg(st.th.Focus).Bold(true).Render(board) + "   " + st.muted(crew) + "   " + st.key("Tab", "switch")
}
func (m tabModel) updateCrew(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	old := m.crewSel
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.crewSel = max(m.crewSel-1, 0)
	case "down", "j":
		m.crewSel = min(m.crewSel+1, max(len(m.crew)-1, 0))
	case "G", "end":
		if m.preview != nil {
			m.preview.tail = true
			m.preview.vp.GotoBottom()
		}
	case "enter":
		if m.preview != nil {
			if m.preview.c.State != "running" {
				contractorsSeen(m.preview.c.ID, "")
				m.preview.c.Seen = true
			}
			m.full = true
			next, _ := m.preview.Update(tea.WindowSizeMsg{Width: m.w, Height: m.h})
			v := next.(contractorView)
			m.preview = &v
		}
	default:
		if m.preview != nil && m.w >= 100 {
			m.preview.vp, _ = m.preview.vp.Update(msg)
			m.preview.tail = m.preview.vp.AtBottom()
		}
	}
	if old != m.crewSel {
		m.preview = nil
		m.refreshCrew()
	}
	return m, nil
}
func contractorLoopLabel(c *contractor) string {
	label := "loop " + c.Loop
	if r, err := loadLoopRun(c.Loop); err == nil && r.Batch != "" {
		label += " · batch " + r.Batch + " · " + tabBatchLabel(r)
	}
	return termSafe(label)
}
func (m tabModel) crewView() tea.View {
	st := gateStyle{tmuxTheme()}
	cw := m.crewWidth()
	room := max(m.h-6, 1)
	var lines []string
	selected := 0
	group := ""
	for i, c := range m.crew {
		g := "FINISHED"
		if c.State == "running" {
			g = "RUNNING"
		}
		if g != group {
			lines = append(lines, st.muted(g))
			group = g
		}
		if i == m.crewSel {
			selected = len(lines)
		}
		glyph, colour := contractorGlyph(st.th, c, m.act.now())
		age := m.act.now().Sub(c.Started)
		if !c.Ended.IsZero() {
			age = m.act.now().Sub(c.Ended)
		}
		marker := "  "
		if i == m.crewSel {
			marker = "› "
		}
		row := marker + glyph + " " + termSafe(c.Engine+" · "+c.Title) + " · " + tabAge(age)
		lines = append(lines, st.fg(colour).Render(ansix.Truncate(row, cw, "…")), st.muted(ansix.Truncate("  "+contractorLoopLabel(c), cw, "…")))
	}
	if len(lines) == 0 {
		lines = []string{st.muted("No loop contractors")}
	}
	offset := max(selected+2-room, 0)
	offset = min(offset, max(len(lines)-room, 0))
	left := lipgloss.NewStyle().Width(cw).Height(room).Render(strings.Join(lines[offset:min(offset+room, len(lines))], "\n"))
	body := left
	if m.w >= 100 && m.preview != nil {
		c := m.preview.c
		width := max(m.w-7-cw, 1)
		head := termSafe(fmt.Sprintf("%s · %s · %s · round %d", c.Role, c.Engine, contractorLoopLabel(c), c.Round))
		right := ansix.Truncate(head, width, "…") + "\n\n" + m.preview.vp.View()
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, "   ", right)
	}
	keys := st.key("↑↓/jk", "select") + "   " + st.key("⏎", "full view") + "   " + st.key("esc/q", "close")
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(m.tabHeading(st) + "\n" + body + "\n" + keys))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// contractorTabArgs also accepts a bare --contractor for the keyboard entry point.
func contractorTabArgs(m tabModel, args []string) tabModel {
	if i := indexOfArg(args, "--contractor"); i >= 0 {
		id := ""
		if i+1 < len(args) {
			id = args[i+1]
		}
		m.selectContractor(id)
	}
	return m
}

func (m *tabModel) selectContractor(id string) {
	m.contractors = true
	m.full = false
	m.crewSel = 0
	m.preview = nil
	m.refreshCrew()
	if contractorID.MatchString(id) {
		for i, c := range m.crew {
			if c.ID == id {
				m.crewSel = i
				v := newContractorView(c)
				m.preview = &v
				m.sizePreview()
				break
			}
		}
	}
}
