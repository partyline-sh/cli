package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// The settings popup (ctrl-\ ,): one row per setting; ⏎ toggles a switch or edits a value, and every
// change is saved at once, or refused with the reason.

type settingsRow struct {
	label  string
	toggle bool
}

var settingsRows = []settingsRow{{"Builder", false}, {"Reviewers", false}, {"Failover", true}, {"Quality", false}, {"Disputes", true}, {"Arbiter", false}}

type settingsModel struct {
	s    loopSettings
	sel  int
	edit bool
	in   textinput.Model
	err  string
	save func(loopSettings) error
	w, h int
}

func newSettingsModel(s loopSettings, save func(loopSettings) error) settingsModel {
	in := textinput.New()
	return settingsModel{s: s, in: in, save: save, w: 90, h: 24}
}

func (m settingsModel) Init() tea.Cmd { return nil }

func (m settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	default:
		// A paste and the cursor's blink arrive as other messages: the field gets them while editing.
		if m.edit {
			var cmd tea.Cmd
			m.in, cmd = m.in.Update(msg)
			return m, cmd
		}
	case tea.KeyPressMsg:
		if m.edit {
			switch msg.String() {
			case "esc":
				m.edit, m.err = false, ""
				return m, nil
			case "enter":
				next, err := settingsApply(m.s, m.sel, m.in.Value())
				if err == nil {
					err = m.save(next)
				}
				if err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.s, m.edit, m.err = next, false, ""
				return m, nil
			}
			var cmd tea.Cmd
			m.in, cmd = m.in.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.sel = max(m.sel-1, 0)
		case "down", "j":
			m.sel = min(m.sel+1, len(settingsRows)-1)
		case "enter", "space":
			if settingsRows[m.sel].toggle {
				next := settingsToggle(m.s, m.sel)
				if err := m.save(next); err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.s, m.err = next, ""
				return m, nil
			}
			m.edit, m.err = true, ""
			m.in.SetValue(settingsText(m.s, m.sel))
			m.in.CursorEnd()
			return m, m.in.Focus()
		}
	}
	return m, nil
}

// settingsText is a row's value as it is edited.
func settingsText(s loopSettings, row int) string {
	switch row {
	case 0:
		return strings.Join(s.Builder, ", ")
	case 1:
		slots := make([]string, len(s.Reviewers))
		for i, slot := range s.Reviewers {
			slots[i] = strings.Join(slot, ", ")
		}
		return strings.Join(slots, "; ")
	case 3:
		return s.Quality
	case 5:
		return s.Arbiter
	}
	return ""
}

// settingsShown is a row's value as the popup shows it.
func settingsShown(s loopSettings, row int) string {
	switch row {
	case 0:
		if len(s.Builder) == 0 {
			return "the engine the batch's session runs on"
		}
		return strings.Join(s.Builder, " -> ")
	case 1:
		if len(s.Reviewers) == 0 {
			return "claude; codex (the --review default)"
		}
		slots := make([]string, len(s.Reviewers))
		for i, slot := range s.Reviewers {
			slots[i] = strings.Join(slot, " -> ")
		}
		return strings.Join(slots, "; ")
	case 2:
		if s.NoFailover {
			return "off"
		}
		return "on: a model at its usage limit hands over to the next in its list"
	case 3:
		if s.Quality == "" {
			return "built in"
		}
		return s.Quality
	case 4:
		if s.Disputes == "arbiter" {
			return "arbiter: a model decides; you are asked when it is unsure"
		}
		return "ask me"
	case 5:
		if s.Disputes != "arbiter" {
			return "(used when disputes go to the arbiter)"
		}
		return s.Arbiter
	}
	return ""
}

// settingsApply sets a text row from what was typed: lists split on commas, reviewer slots on
// semicolons; an empty value is the default.
func settingsApply(s loopSettings, row int, text string) (loopSettings, error) {
	text = strings.TrimSpace(text)
	list := func(v string) []string {
		var out []string
		for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' }) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	switch row {
	case 0:
		s.Builder = list(strings.ReplaceAll(text, "->", ","))
	case 1:
		s.Reviewers = nil
		for _, slot := range strings.Split(text, ";") {
			if l := list(strings.ReplaceAll(slot, "->", ",")); len(l) > 0 {
				s.Reviewers = append(s.Reviewers, l)
			}
		}
	case 3:
		if strings.EqualFold(text, "built in") {
			text = ""
		}
		// Absolute: the settings are global, and a batch starts from any directory.
		if text != "" {
			abs, err := filepath.Abs(expandHome(text))
			if err != nil {
				return s, err
			}
			text = abs
		}
		s.Quality = text
	case 5:
		s.Arbiter = text
	default:
		return s, fmt.Errorf("%s is switched with ⏎", settingsRows[row].label)
	}
	return s, nil
}

// settingsToggle flips a switch row. Disputes to the arbiter starts with claude as the arbiter.
func settingsToggle(s loopSettings, row int) loopSettings {
	switch row {
	case 2:
		s.NoFailover = !s.NoFailover
	case 4:
		if s.Disputes == "arbiter" {
			s.Disputes = ""
		} else {
			s.Disputes = "arbiter"
			if s.Arbiter == "" {
				s.Arbiter = "claude"
			}
		}
	}
	return s
}

func (m settingsModel) View() tea.View {
	st := gateStyle{tmuxTheme()}
	w := max(m.w-4, 30)
	var b strings.Builder
	b.WriteString(st.heading("Loop settings", w) + "\n\n")
	for i, row := range settingsRows {
		value := st.text(ansix.Truncate(termSafe(settingsShown(m.s, i)), max(w-12, 10), "…"))
		if i == m.sel && m.edit {
			m.in.SetWidth(max(w-14, 10)) // a long value scrolls inside the field instead of wrapping
			value = m.in.View()
		}
		line := st.row(row.label, value, w)
		if i == m.sel && !m.edit {
			line = lipgloss.NewStyle().Background(lipgloss.Color(st.th.Raised)).Width(w).Render(line)
		}
		b.WriteString(line + "\n")
	}
	var tail strings.Builder
	if m.edit {
		tail.WriteString(st.faint(ansix.Truncate(settingsHint(m.sel), w, "…")) + "\n")
	}
	if m.err != "" {
		tail.WriteString("\n" + st.warn(ansix.Truncate(termSafe(m.err), w, "…")) + "\n")
	}
	// The note goes first when the popup is short: the rows, the hint, the error and the keys stay.
	note := "\n" + lipgloss.NewStyle().Width(w).Render(st.muted("New batches use these, and you approve them with the batch; a batch keeps the settings it was approved with. --config or --review overrides them.")) + "\n"
	if lipgloss.Height(b.String()+note+tail.String())+4 <= m.h {
		b.WriteString(note)
	} else {
		b.WriteString("\n")
	}
	b.WriteString(tail.String())
	keys := st.key("↑↓", "move") + "   " + st.key("⏎", "change") + "   " + st.key("esc", "close")
	if m.edit {
		keys = st.key("⏎", "save") + "   " + st.key("esc", "cancel")
	}
	b.WriteString("\n" + keys)
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

func settingsHint(row int) string {
	switch row {
	case 0:
		return "engine or engine:model, first choice first: claude:opus, codex:gpt-5; empty for the default"
	case 1:
		return "slots split by ;, each a list to fail over through: codex:gpt-5, claude:opus; claude:sonnet"
	case 3:
		return "a file holding the standard, or empty for the built-in one"
	case 5:
		return "the arbiter's engine:model"
	}
	return ""
}

// settingsMain is `ptln tmux --settings`, inside its popup.
func settingsMain() error {
	s, err := loadSettings()
	m := newSettingsModel(s, saveSettings)
	if err != nil {
		m.err = err.Error() + " (saving replaces the file)"
	}
	_, err = tea.NewProgram(m).Run()
	return err
}
