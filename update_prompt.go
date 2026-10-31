package main

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
	uitheme "partyline.sh/partyline/internal/theme"
)

// The update popup: when a newer partyline is out it says what changed and offers to update now,
// to skip that version, or to turn on automatic updates. esc asks again the next day. Updating
// replaces the binary only: sessions in tmux and running loops keep their processes, the watcher
// restarts itself on the new binary (watcherMain), and the next command runs the new version.

type updateActions struct {
	update   func() tea.Cmd // runs the upgrade in the popup's terminal; its result arrives as updateDone
	skip     func(version string) error
	autoOn   func(on bool) error
	canAuto  string // "" when automatic updates can run unattended, else why not
	how      string // "Homebrew" or "the install script"
	autoNote string // what automatic updating does on this install
}

// updateDone is the update's end: its error, or the version the new binary reports.
type updateDone struct {
	err       error
	installed string
}

type updatePromptModel struct {
	latest, current string
	changes         []string
	auto            bool
	sel             int
	act             updateActions
	state           string // "", "updating", "updated", "skipped"
	installed       string // what the binary reports after updating
	err             string
	w, h            int
}

const (
	updRowNow = iota
	updRowSkip
	updRowAuto
	updRows
)

func newUpdatePrompt(latest, current string, changes []string, auto bool, act updateActions) updatePromptModel {
	return updatePromptModel{latest: latest, current: current, changes: changes, auto: auto, act: act, w: 72, h: 24}
}

func (m updatePromptModel) Init() tea.Cmd { return nil }

func (m updatePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case updateDone:
		m.state, m.err, m.installed = "updated", "", msg.installed
		if msg.err != nil {
			m.state, m.err = "", "The update failed: "+msg.err.Error()
		}
	case tea.KeyPressMsg:
		if m.state == "updated" || m.state == "skipped" {
			return m, tea.Quit // any key closes once it is done
		}
		if m.state == "updating" {
			return m, nil
		}
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.sel = (m.sel + updRows - 1) % updRows
		case "down", "j", "tab":
			m.sel = (m.sel + 1) % updRows
		case "enter", "space":
			return m.choose()
		}
	}
	return m, nil
}

func (m updatePromptModel) choose() (tea.Model, tea.Cmd) {
	m.err = ""
	switch m.sel {
	case updRowNow:
		m.state = "updating"
		return m, m.act.update()
	case updRowSkip:
		if err := m.act.skip(m.latest); err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.state = "skipped"
	case updRowAuto:
		if m.act.canAuto != "" && !m.auto {
			m.err = m.act.canAuto
			return m, nil
		}
		if err := m.act.autoOn(!m.auto); err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.auto = !m.auto
	}
	return m, nil
}

func (m updatePromptModel) View() tea.View {
	st := gateStyle{tmuxTheme()}
	w := max(m.w-4, 30)
	wrap := func(s string) string { return lipgloss.NewStyle().Width(w).Render(s) }
	var b strings.Builder
	b.WriteString(st.heading("partyline "+m.latest+" is available", w) + "\n\n")
	b.WriteString(st.muted(fmt.Sprintf("You have %s. Installed with %s.", m.current, m.act.how)) + "\n\n")
	if len(m.changes) > 0 {
		b.WriteString(st.text("What's new") + "\n")
		shown := min(len(m.changes), max(m.h-20, 3))
		for _, c := range m.changes[:shown] {
			b.WriteString("  " + st.muted("·") + " " + st.text(ansix.Truncate(termSafe(c), w-4, "…")) + "\n")
		}
		if more := len(m.changes) - shown; more > 0 {
			b.WriteString("  " + st.muted(fmt.Sprintf("and %d more", more)) + "\n")
		}
		b.WriteString("\n")
	}
	switch m.state {
	case "updating":
		b.WriteString(st.text("Updating with "+m.act.how+"…") + "\n")
	case "updated":
		if m.installed != m.latest {
			// The installer finished but the binary is not the release asked for: say what it is.
			b.WriteString(st.warn(fmt.Sprintf("The installer finished, but partyline now reports %q, not %s.", m.installed, m.latest)) + "\n")
			b.WriteString(wrap(st.muted("Run ptln version to check, or ptln upgrade to try again.")) + "\n\n")
		} else {
			b.WriteString(st.fg(st.th.Colour(uitheme.Passed)).Render("✓ Updated to "+m.installed+".") + "\n")
			b.WriteString(wrap(st.muted("Your sessions and loops kept running. New commands use "+m.installed+", and the watcher moves to it within a few seconds.")) + "\n\n")
		}
		b.WriteString(st.key("any key", "close"))
	case "skipped":
		b.WriteString(st.text("Skipped "+m.latest+".") + "\n")
		b.WriteString(wrap(st.muted("You won't be asked about it again. A newer version will ask; ptln upgrade updates any time.")) + "\n\n")
		b.WriteString(st.key("any key", "close"))
	default:
		b.WriteString(m.rowsView(st, w))
		if m.err != "" {
			b.WriteString("\n" + wrap(st.warn(termSafe(m.err))) + "\n")
		}
		b.WriteString("\n" + wrap(st.muted(m.rowNote())) + "\n\n")
		b.WriteString(st.key("↑↓", "move") + "   " + st.key("⏎", m.rowVerb()) + "   " + st.key("esc", "later (asks again tomorrow)"))
	}
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

func (m updatePromptModel) rowsView(st gateStyle, w int) string {
	selected := lipgloss.NewStyle().Background(lipgloss.Color(st.th.Focus)).Foreground(lipgloss.Color(st.th.Surface)).Bold(true)
	auto := "off"
	if m.auto {
		auto = "on"
	}
	rows := [updRows][2]string{
		{"Update now", "to " + m.latest + "; your sessions keep running"},
		{"Skip " + m.latest, "don't ask about this version again"},
		{"Automatic", auto},
	}
	var b strings.Builder
	for i, r := range rows {
		label := fmt.Sprintf("%-14s", r[0])
		if i == m.sel {
			b.WriteString(selected.Width(w).Render("▸ "+label+" "+r[1]) + "\n")
			continue
		}
		value := st.muted(r[1])
		if i == updRowAuto && m.act.canAuto != "" && !m.auto {
			value = st.faint("off · not available on this install")
		}
		b.WriteString("  " + st.text(label) + " " + value + "\n")
	}
	return b.String()
}

// rowNote is what the selected row does, in full.
func (m updatePromptModel) rowNote() string {
	switch m.sel {
	case updRowNow:
		return "Runs the update here with " + m.act.how + ". It replaces the partyline binary only: sessions in tmux and running loops keep going, and the next command you run is the new version."
	case updRowSkip:
		return "You won't be asked about " + m.latest + " again. A newer release asks as usual, and ptln upgrade still updates whenever you want."
	}
	if m.act.canAuto != "" {
		return m.act.canAuto
	}
	return m.act.autoNote
}

func (m updatePromptModel) rowVerb() string {
	switch m.sel {
	case updRowNow:
		return "update"
	case updRowSkip:
		return "skip"
	}
	return "switch"
}

// releaseChanges is a release's notes as one line per change: GoReleaser's changelog lines
// ("* <sha> <subject> (#1490)") without the hash and PR number.
var changelogLine = regexp.MustCompile(`^\*\s+(?:[0-9a-f]{7,40}\s+)?(.+?)(?:\s+\(#\d+\))?$`)

func releaseChanges(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if m := changelogLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}
