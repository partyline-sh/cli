package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// The settings popup (ctrl-\ ,, or , in the Loops popup). Each AI (the builder, each reviewer, the
// arbiter) is chosen from lists, engine then model then effort; b adds a backup it hands over to at
// its usage limit, x removes one. Switches flip with ⏎; the quality standard is typed. Every change
// is saved at once, or refused with the reason.

type settingsRowKind int

const (
	rowAI settingsRowKind = iota
	rowAddReviewer
	rowFailover
	rowQuality
	rowDisputes
	rowDecisions
	rowJev
)

type settingsRow struct {
	kind  settingsRowKind
	role  string // builder, reviewer, arbiter
	index int    // the reviewer's place
}

// settingsRowsFor is the popup's rows for s: the AIs first, then the switches.
func settingsRowsFor(s loopSettings) []settingsRow {
	rows := []settingsRow{{kind: rowAI, role: "builder"}}
	n := max(len(s.Reviewers), 1)
	for i := 0; i < n; i++ {
		rows = append(rows, settingsRow{kind: rowAI, role: "reviewer", index: i})
	}
	rows = append(rows, settingsRow{kind: rowAddReviewer}, settingsRow{kind: rowFailover}, settingsRow{kind: rowQuality}, settingsRow{kind: rowDecisions})
	// Loops deciding for themselves use the arbiter (else reviewer 1) on every dispute; asking the
	// person, the arbiter is used only when disputes go to it.
	if s.AskMe {
		rows = append(rows, settingsRow{kind: rowDisputes})
	}
	if !s.AskMe || s.Disputes == "arbiter" {
		rows = append(rows, settingsRow{kind: rowAI, role: "arbiter"})
	}
	return append(rows, settingsRow{kind: rowJev})
}

// roleChoices is a role's choices, first choice then backups.
func roleChoices(s loopSettings, r settingsRow) []string {
	switch r.role {
	case "builder":
		return s.Builder
	case "reviewer":
		if r.index < len(s.Reviewers) {
			return s.Reviewers[r.index]
		}
	case "arbiter":
		if s.Arbiter != "" {
			return []string{s.Arbiter}
		}
	}
	return nil
}

func setAIList(s loopSettings, r settingsRow, list []string) loopSettings {
	switch r.role {
	case "builder":
		s.Builder = list
	case "reviewer":
		reviewers := slices.Clone(s.Reviewers)
		for len(reviewers) <= r.index {
			reviewers = append(reviewers, nil)
		}
		reviewers[r.index] = list
		s.Reviewers = reviewers
	case "arbiter":
		s.Arbiter = ""
		if len(list) > 0 {
			s.Arbiter = list[0]
		}
	}
	return s
}

func (r settingsRow) label() string {
	switch r.kind {
	case rowAddReviewer:
		return ""
	case rowFailover:
		return "Failover"
	case rowQuality:
		return "Quality"
	case rowDisputes:
		return "Disputes"
	case rowDecisions:
		return "Decisions"
	case rowJev:
		return "Jev key"
	}
	switch r.role {
	case "reviewer":
		return fmt.Sprintf("Reviewer %d", r.index+1)
	case "arbiter":
		return "Arbiter"
	}
	return "Builder"
}

// specShown is one choice as the popup shows it: "codex · gpt-6.1-sol · medium".
func specShown(spec string) string {
	name, rest, _ := strings.Cut(spec, ":")
	model, effort, _ := strings.Cut(rest, ":")
	parts := []string{name}
	if model != "" {
		parts = append(parts, model)
	}
	if effort != "" {
		parts = append(parts, effort)
	}
	return strings.Join(parts, " · ")
}

// pickColumn is one of the picker's lists.
type pickColumn int

const (
	pickEngine pickColumn = iota
	pickModel
	pickEffort
)

type settingsModel struct {
	s    loopSettings
	sel  int
	edit bool // typing the quality standard
	in   textinput.Model
	err  string
	save func(loopSettings) error
	w, h int
	pick bool // the engine · model · effort picker is open
	jev  bool // the edit field holds a Jev key (masked)
	add  bool // the pick adds a backup instead of replacing the first choice
	col  pickColumn
	at   [3]int // the highlighted entry in each column
	cat  []engineChoice
}

func newSettingsModel(s loopSettings, save func(loopSettings) error) settingsModel {
	return settingsModel{s: s, in: textinput.New(), save: save, w: 90, h: 24, cat: modelCatalog()}
}

func (m settingsModel) Init() tea.Cmd { return nil }

func (m settingsModel) rows() []settingsRow { return settingsRowsFor(m.s) }

func (m settingsModel) row() settingsRow {
	rows := m.rows()
	return rows[min(m.sel, len(rows)-1)]
}

func (m settingsModel) commit(next loopSettings) settingsModel {
	if err := m.save(next); err != nil {
		m.err = err.Error()
		return m
	}
	m.s, m.err = next, ""
	m.sel = min(m.sel, len(m.rows())-1)
	return m
}

// openPicker starts the picker on the row's current choice (or its first entries).
func (m settingsModel) openPicker(add bool) settingsModel {
	m.pick, m.add, m.col, m.at, m.err = true, add, pickEngine, [3]int{}, ""
	if list := roleChoices(m.s, m.row()); len(list) > 0 && !add {
		name, rest, _ := strings.Cut(list[0], ":")
		model, effort, _ := strings.Cut(rest, ":")
		for i, e := range m.cat {
			if e.Name != name {
				continue
			}
			m.at[pickEngine] = i
			for j, mc := range e.Models {
				if mc.Name == model {
					m.at[pickModel] = j
					m.at[pickEffort] = max(slices.Index(mc.Efforts, effort), 0)
				}
			}
		}
	}
	return m
}

func (m settingsModel) pickedEngine() engineChoice { return m.cat[min(m.at[pickEngine], len(m.cat)-1)] }
func (m settingsModel) pickedModel() modelChoice {
	ms := m.pickedEngine().Models
	return ms[min(m.at[pickModel], len(ms)-1)]
}

func (m settingsModel) columnLen(c pickColumn) int {
	switch c {
	case pickEngine:
		return len(m.cat)
	case pickModel:
		return len(m.pickedEngine().Models)
	}
	return len(m.pickedModel().Efforts)
}

func (m settingsModel) updatePicker(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.pick = false
		// A reviewer added a moment ago and never chosen goes again.
		if n := len(m.s.Reviewers); n > 1 && len(m.s.Reviewers[n-1]) == 0 {
			m.s.Reviewers = m.s.Reviewers[:n-1]
			m.sel = min(m.sel, len(m.rows())-1)
		}
	case "left", "h", "shift+tab":
		if m.col > pickEngine {
			m.col--
		}
	case "right", "l", "tab":
		if m.col < pickEffort {
			m.col++
		}
	case "up", "k":
		m.at[m.col] = max(m.at[m.col]-1, 0)
		m.clampBelow()
	case "down", "j":
		m.at[m.col] = min(m.at[m.col]+1, m.columnLen(m.col)-1)
		m.clampBelow()
	case "enter":
		if m.col < pickEffort {
			m.col++
			return m, nil
		}
		spec := m.pickedEngine().Name + ":" + m.pickedModel().Name + ":" + m.pickedModel().Efforts[m.at[pickEffort]]
		list := slices.Clone(roleChoices(m.s, m.row()))
		if m.add {
			list = append(list, spec)
		} else if len(list) == 0 {
			list = []string{spec}
		} else {
			list[0] = spec
		}
		m.pick = false
		return m.commit(setAIList(m.s, m.row(), list)), nil
	}
	return m, nil
}

// clampBelow keeps the later columns' highlights inside lists that changed with the choice.
func (m *settingsModel) clampBelow() {
	for c := m.col + 1; c <= pickEffort; c++ {
		m.at[c] = min(m.at[c], max(m.columnLen(c)-1, 0))
	}
}

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
		if m.pick {
			return m.updatePicker(msg)
		}
		if m.edit {
			switch msg.String() {
			case "esc":
				m.edit, m.err, m.jev = false, "", false
				m.in.EchoMode = textinput.EchoNormal
				m.in.SetValue("")
				return m, nil
			case "enter":
				if m.jev {
					m.edit, m.jev = false, false
					m.in.EchoMode = textinput.EchoNormal
					if v := strings.TrimSpace(m.in.Value()); v != "" {
						if err := saveJevKey(v); err != nil {
							m.err = err.Error()
						}
					}
					m.in.SetValue("")
					return m, nil
				}
				next, err := settingsApplyQuality(m.s, m.in.Value())
				if err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.edit = false
				return m.commit(next), nil
			}
			var cmd tea.Cmd
			m.in, cmd = m.in.Update(msg)
			return m, cmd
		}
		r := m.row()
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.sel = max(m.sel-1, 0)
		case "down", "j":
			m.sel = min(m.sel+1, len(m.rows())-1)
		case "b":
			if r.kind == rowAI && r.role != "arbiter" && len(roleChoices(m.s, r)) > 0 {
				return m.openPicker(true), nil
			}
		case "x":
			if r.kind == rowJev && savedJevKey() != "" {
				if err := os.Remove(jevKeyPath()); err != nil {
					m.err = err.Error()
				}
				return m, nil
			}
			if r.kind == rowAI {
				list := roleChoices(m.s, r)
				switch {
				case len(list) > 1:
					return m.commit(setAIList(m.s, r, list[:len(list)-1])), nil
				case r.role == "reviewer" && len(m.s.Reviewers) > 1:
					next := m.s
					next.Reviewers = slices.Delete(slices.Clone(next.Reviewers), r.index, r.index+1)
					return m.commit(next), nil
				}
			}
		case "enter", "space":
			switch r.kind {
			case rowAI:
				return m.openPicker(false), nil
			case rowAddReviewer:
				next := m.s
				next.Reviewers = append(slices.Clone(next.Reviewers), nil)
				m.s = next // shown at once; saved when its AI is chosen
				m.sel = slices.IndexFunc(m.rows(), func(x settingsRow) bool { return x.role == "reviewer" && x.index == len(next.Reviewers)-1 })
				return m.openPicker(false), nil
			case rowFailover:
				next := m.s
				next.NoFailover = !next.NoFailover
				return m.commit(next), nil
			case rowDecisions:
				next := m.s
				next.AskMe = !next.AskMe
				return m.commit(next), nil
			case rowJev:
				m.edit, m.err, m.jev = true, "", true
				m.in.EchoMode = textinput.EchoPassword
				m.in.SetValue("")
				return m, m.in.Focus()
			case rowDisputes:
				next := m.s
				if next.Disputes == "arbiter" {
					next.Disputes = ""
				} else {
					next.Disputes = "arbiter"
				}
				return m.commit(next), nil
			case rowQuality:
				m.edit, m.err = true, ""
				m.in.SetValue(m.s.Quality)
				m.in.CursorEnd()
				return m, m.in.Focus()
			}
		}
	}
	return m, nil
}

// settingsApplyQuality sets the quality standard from what was typed: a full path, or empty (or
// "built in") for the built-in one.
func settingsApplyQuality(s loopSettings, text string) (loopSettings, error) {
	text = strings.TrimSpace(text)
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
	return s, nil
}

// settingsShown is a row's value as the popup shows it.
func settingsShown(s loopSettings, r settingsRow) string {
	switch r.kind {
	case rowAddReviewer:
		return "+ Add a reviewer"
	case rowFailover:
		if s.NoFailover {
			return "off"
		}
		return "on: switch to the next backup at the usage limit"
	case rowQuality:
		if s.Quality == "" {
			return "built in"
		}
		return s.Quality
	case rowDisputes:
		if s.Disputes == "arbiter" {
			return "let an AI decide; ask me if unsure"
		}
		return "ask me"
	case rowDecisions:
		if s.AskMe {
			return "ask me: I approve briefs and rule on disputes"
		}
		return "loops decide: nothing waits for me"
	case rowJev:
		if savedJevKey() != "" {
			return "saved: an outside opinion when the models disagree"
		}
		return "not set (optional: an outside opinion when the models disagree)"
	}
	list := roleChoices(s, r)
	if len(list) == 0 {
		if r.role == "arbiter" && !s.AskMe {
			return "reviewer 1 (default)"
		}
		return "choose…"
	}
	shown := specShown(list[0])
	if n := len(list) - 1; n > 0 {
		shown += fmt.Sprintf("  (+%d %s)", n, plural(n, "backup", "backups"))
	}
	return shown
}

func (m settingsModel) View() tea.View {
	st := gateStyle{tmuxTheme()}
	w := max(m.w-4, 30)
	var b strings.Builder
	b.WriteString(st.heading("Loop settings", w) + "\n\n")
	selected := lipgloss.NewStyle().Background(lipgloss.Color(st.th.Focus)).Foreground(lipgloss.Color(st.th.Surface)).Bold(true)
	for i, row := range m.rows() {
		value := settingsShown(m.s, row)
		missing := row.kind == rowAI && len(roleChoices(m.s, row)) == 0
		label := fmt.Sprintf("%-11s", row.label())
		text := ansix.Truncate(termSafe(value), max(w-16, 10), "…")
		if i == m.sel && m.edit {
			m.in.SetWidth(max(w-16, 10)) // a long value scrolls inside the field instead of wrapping
			b.WriteString("  " + st.muted(label) + " " + m.in.View() + "\n")
			continue
		}
		if i == m.sel && !m.pick {
			b.WriteString(selected.Width(w).Render("▸ "+label+" "+text) + "\n")
			continue
		}
		valueStyle := st.text(text)
		if missing {
			valueStyle = st.warn(text)
		} else if row.kind == rowAddReviewer {
			valueStyle = st.muted(text)
		}
		b.WriteString("  " + st.muted(label) + " " + valueStyle + "\n")
	}
	if m.pick {
		b.WriteString("\n" + m.pickerView(st, w))
	}
	var tail strings.Builder
	if m.err != "" {
		tail.WriteString("\n" + st.warn(ansix.Truncate(termSafe(m.err), w, "…")) + "\n")
	}
	note := "\n" + lipgloss.NewStyle().Width(w).Render(st.muted("New batches use these settings. You approve them with each batch; that batch keeps them. --config or --review overrides them.")) + "\n"
	if what := m.s.notChosen(); what != "" {
		note = "\n" + lipgloss.NewStyle().Width(w).Render(st.warn("Choose your AIs before your first loop. Model and effort affect token use, so partyline leaves them to you. Still needed: "+termSafe(what)+".")) + "\n"
	}
	if !m.pick && lipgloss.Height(b.String()+note+tail.String())+4 <= m.h {
		b.WriteString(note)
	}
	b.WriteString(tail.String())
	b.WriteString("\n" + lipgloss.NewStyle().Width(w).Render(m.keys(st)))
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

func (m settingsModel) keys(st gateStyle) string {
	switch {
	case m.pick:
		return st.key("↑↓", "choose") + "   " + st.key("←→", "engine · model · effort") + "   " + st.key("⏎", "next / save") + "   " + st.key("esc", "cancel")
	case m.edit:
		return st.key("⏎", "save") + "   " + st.key("esc", "cancel")
	}
	keys := []string{st.key("↑↓", "move")}
	r := m.row()
	switch r.kind {
	case rowAI:
		keys = append(keys, st.key("⏎", "choose"))
		if r.role != "arbiter" && len(roleChoices(m.s, r)) > 0 {
			keys = append(keys, st.key("b", "add a backup"))
		}
		if len(roleChoices(m.s, r)) > 1 || (r.role == "reviewer" && len(m.s.Reviewers) > 1) {
			keys = append(keys, st.key("x", "remove"))
		}
	case rowAddReviewer:
		keys = append(keys, st.key("⏎", "add"))
	case rowQuality:
		keys = append(keys, st.key("⏎", "edit"))
	case rowJev:
		keys = append(keys, st.key("⏎", "paste a key"))
		if savedJevKey() != "" {
			keys = append(keys, st.key("x", "remove it"))
		}
	default:
		keys = append(keys, st.key("⏎", "switch"))
	}
	return strings.Join(append(keys, st.key("esc", "close")), "   ")
}

// pickerView is the three lists side by side, the active one's entry highlighted.
func (m settingsModel) pickerView(st gateStyle, w int) string {
	selected := lipgloss.NewStyle().Background(lipgloss.Color(st.th.Focus)).Foreground(lipgloss.Color(st.th.Surface)).Bold(true)
	chosen := lipgloss.NewStyle().Foreground(lipgloss.Color(st.th.Focus)).Bold(true)
	colW := max((w-4)/3, 12)
	column := func(c pickColumn, title string, items []string) string {
		var lines []string
		lines = append(lines, st.muted(title))
		for i, it := range items {
			text := ansix.Truncate(termSafe(it), colW-3, "…")
			switch {
			case i == m.at[c] && c == m.col:
				lines = append(lines, selected.Width(colW-1).Render("▸ "+text))
			case i == m.at[c]:
				lines = append(lines, chosen.Render("• "+text))
			default:
				lines = append(lines, "  "+st.text(text))
			}
		}
		return lipgloss.NewStyle().Width(colW).Render(strings.Join(lines, "\n"))
	}
	var engines, models []string
	for _, e := range m.cat {
		engines = append(engines, e.Name)
	}
	for _, mc := range m.pickedEngine().Models {
		models = append(models, mc.Label)
	}
	who := strings.ToLower(m.row().label())
	if m.row().role != "reviewer" {
		who = "the " + who
	}
	title := "Choose " + who
	if m.add {
		title = "Add a backup for " + who
	}
	return st.text(title) + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top,
		column(pickEngine, "Engine", engines), column(pickModel, "Model", models), column(pickEffort, "Effort", m.pickedModel().Efforts)) + "\n"
}

// settingsMain is `ptln tmux --settings`, inside its popup.
func settingsMain() error {
	s, err := loadSettings()
	m := newSettingsModel(s, saveSettings)
	if err != nil {
		m.err = err.Error() + " (saving replaces the file)"
	}
	_, err = tea.NewProgram(m).Run()
	resumeSettingsWaits() // loops started before the settings were chosen carry on now
	return err
}

// openSettingsPopup shows the loop settings as a popup on the current client and waits for it to close.
func openSettingsPopup() {
	cols, rows := tmuxClientSize()
	w, h := gatePopupSize(cols, rows)
	_ = tmuxCmd("display-popup", "-E", "-w", fmt.Sprint(w), "-h", fmt.Sprint(min(h, 30)), selfExe(), "tmux", "--settings").Run()
}
