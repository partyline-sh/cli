package main

import (
	"os"
	"strconv"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// A contractor's live view: a read-only popup of its pane while it runs, its log once it ended.
// Opening it marks a finished contractor seen.

type contractorView struct {
	c    *contractor
	vp   viewport.Model
	w, h int
	tail bool // follow the newest output until the person scrolls
}

type contractorTick struct{}

func contractorTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return contractorTick{} })
}

func newContractorView(c *contractor) contractorView {
	m := contractorView{c: c, vp: viewport.New(), w: 100, h: 30, tail: true}
	m.vp.SetContent(contractorOutput(c))
	m.vp.GotoBottom()
	return m
}

// contractorOutput is what a contractor has printed: its pane while it runs, else its log.
var contractorOutput = func(c *contractor) string {
	if c.State == "running" && c.Window != "" {
		if out, err := tmuxCmd("capture-pane", "-p", "-J", "-S", "-2000", "-t", c.Window).Output(); err == nil {
			return termSafeLines(string(out))
		}
	}
	b, _ := os.ReadFile(contractorLog(c.ID))
	return termSafeLines(string(b))
}

func (m contractorView) Init() tea.Cmd { return contractorTickCmd() }

func (m contractorView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.vp.SetWidth(max(m.w-4, 10))
		m.vp.SetHeight(max(m.h-4, 3))
	case contractorTick:
		if c, err := loadContractor(m.c.ID); err == nil {
			if m.c.State == "running" && c.State != "running" {
				contractorsSeen(c.ID, "") // it finished while being watched
				c.Seen = true
			}
			m.c = c
		}
		m.vp.SetContent(contractorOutput(m.c))
		if m.tail {
			m.vp.GotoBottom()
		}
		return m, contractorTickCmd()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "end", "G":
			m.tail = true
			m.vp.GotoBottom()
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.tail = m.vp.AtBottom()
		return m, cmd
	}
	return m, nil
}

func (m contractorView) View() tea.View {
	st := gateStyle{tmuxTheme()}
	glyph, colour := contractorGlyph(st.th, m.c, time.Now())
	head := st.fg(colour).Render(glyph) + " " + st.text(termSafe(m.c.Engine+" · "+m.c.Role+" · "+m.c.Title)) + st.muted("  loop "+termSafe(m.c.Loop)+" · round "+strconv.Itoa(m.c.Round))
	if m.c.State != "running" {
		head += "\n" + st.muted(termSafe("ended "+m.c.State+" (exit "+strconv.Itoa(m.c.Exit)+"): "+m.c.Result))
	}
	keys := st.key("↑↓", "scroll") + "   " + st.key("G", "follow") + "   " + st.key("esc", "close")
	body := ansix.Truncate(head, max(m.w-4, 10), "…") + "\n\n" + m.vp.View() + "\n" + keys
	v := tea.NewView(lipgloss.NewStyle().Padding(0, 2).Render(body))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// contractorViewMain is `ptln tmux --contractor-view <id>`, inside its popup.
func contractorViewMain(id string) error {
	c, err := loadContractor(id)
	if err != nil {
		return err
	}
	if c.State != "running" {
		contractorsSeen(c.ID, "")
	}
	_, err = tea.NewProgram(newContractorView(c)).Run()
	return err
}

// contractorViewPopup is the display-popup that shows one contractor, the launcher's size.
func contractorViewPopup(id string, cols, rows int) []string {
	w, h := launcherPopupSize(cols, rows)
	return []string{"display-popup", "-E", "-w", strconv.Itoa(w), "-h", strconv.Itoa(h),
		"-T", "#[align=centre fg=" + tmuxTheme().Focus + ",bold] ⚙ contractor " + id + " ", selfExe(), "tmux", "--contractor-view", id}
}
