// The welcome screen — partyline's front door when the switchboard has nothing to show
// (bare `ptln` with zero sessions) and on demand via `ptln welcome`. A centered brand
// wordmark over a short list of "doors", each wired to an EXISTING entry point: resume
// the newest session, a fresh mux terminal (the switchboard's 'n'), a shared shell
// (`ptln start`), the Planning agent (`ptln plan`), and the switchboard's search.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ansix "github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// welcomeWantSearch asks runLLMSApp to open the switchboard with search already active
// (the welcome screen's "/ find a session" door). Consumed once at launcher build.
var welcomeWantSearch bool

// welcomeDoor is a shortcut and the action dispatched after the screen exits.
type welcomeDoor struct {
	disp     string // the key as shown ("↵", "n", …)
	key      byte   // the raw byte that activates it directly
	title    string
	subtitle string
	run      func() error // dispatched after the terminal is restored
}

// welcomeMain is `ptln welcome` — always shows the screen, sessions or not.
func welcomeMain(_ []string) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println("ptln welcome needs an interactive terminal")
		return
	}
	if err := runWelcome(); err != nil {
		fatal(err)
	}
}

// lastResumable is the newest resumable user session (the same data + "last used" rule
// the switchboard sorts by), with when it was last touched. nil when there are none.
func lastResumable(all []aiSession, meta map[string]sessMeta) (*aiSession, time.Time) {
	var best *aiSession
	var bestT time.Time
	for i := range all {
		s := &all[i]
		if s.resumeArgv == nil || isAgentSession(*s) {
			continue
		}
		t := s.LastActive
		if lu := meta[s.ID].LastUsed; lu.After(t) {
			t = lu
		}
		if best == nil || t.After(bestT) {
			best, bestT = s, t
		}
	}
	return best, bestT
}

// runWelcome builds the doors from current session state, runs the full-screen picker,
// and dispatches the chosen door. q/esc opens the switchboard when there are sessions
// to switch, else just quits (the switchboard refuses to open empty).
func runWelcome() error {
	all := collectSessions()
	// The one moment the user is guaranteed to care: they came looking for a session and there
	// isn't one. Ask WHERE TO LOOK here rather than shipping a config key nobody would ever find —
	// a session started under a different HOME is invisible, not missing (see llms_roots.go).
	// Offered once per empty launch; declining just proceeds to the normal doors.
	if len(all) == 0 {
		if offerSessionRoots() {
			all = collectSessions()
		}
	}
	meta := loadLLMMeta()
	var doors []welcomeDoor
	if s, when := lastResumable(all, meta); s != nil {
		cp := *s
		doors = append(doors, welcomeDoor{"↵", '\r', "resume last session",
			cp.Tool + " · " + projLabel(cp.Cwd) + " · " + humanAge(when), func() error {
				mt := meta[cp.ID]
				mt.LastUsed = time.Now()
				meta[cp.ID] = mt
				saveLLMMeta(meta)
				spec := prepareLaunchSpec(Spec{Label: muxLabelFor(cp, meta), Key: cp.ID,
					Model: sessionModel(cp), Argv: cp.resumeArgv, Dir: cp.resumeDir})
				return runLLMSApp([]Spec{spec})
			}})
	}
	doors = append(doors,
		welcomeDoor{"n", 'n', "new session", "", func() error {
			return runLLMSApp([]Spec{*shellSpec()}) // the switchboard's 'n': a fresh mux terminal
		}},
	)
	if len(all) > 0 { // searching an empty switchboard would just bounce back here
		doors = append(doors, welcomeDoor{"/", '/', "find a session", "", func() error {
			welcomeWantSearch = true
			return runLLMSApp(nil)
		}})
	}
	idx, err := welcomeLoop(doors, len(all) > 0)
	if err != nil || idx < 0 {
		return err
	}
	if idx == len(doors) { // q/esc with sessions present → the switchboard
		return runLLMSApp(nil)
	}
	return doors[idx].run()
}

// welcomeLoop returns the chosen door after restoring the terminal.
func welcomeLoop(doors []welcomeDoor, canSwitch bool) (int, error) {
	if !stdinIsTTY() {
		// As before the screen was a program: with no terminal, raw mode fails and that error is
		// what the caller reports.
		_, err := term.MakeRaw(int(os.Stdin.Fd()))
		return -1, err
	}
	final, err := tea.NewProgram(welcomeModel{doors: doors, canSwitch: canSwitch, result: -1}, tea.WithInput(os.Stdin)).Run()
	if err != nil {
		return -1, err
	}
	return final.(welcomeModel).result, nil
}

// welcomeModel selects a door without running it.
type welcomeModel struct {
	doors             []welcomeDoor
	canSwitch         bool
	sel, result, w, h int
}

// Init starts the welcome screen.
func (m welcomeModel) Init() tea.Cmd { return nil }

// Update handles navigation and records the chosen door.
func (m welcomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			m.sel = (m.sel + len(m.doors) - 1) % len(m.doors)
		case "down", "j":
			m.sel = (m.sel + 1) % len(m.doors)
		case "esc", "q", "ctrl+c":
			m.result = -1
			if m.canSwitch {
				m.result = len(m.doors)
			}
			return m, tea.Quit
		case "enter":
			m.result = m.sel
			return m, tea.Quit
		default:
			for i, d := range m.doors {
				if msg.Text == string(d.key) {
					m.result = i
					return m, tea.Quit
				}
			}
		}
	}
	return m, nil
}

// View draws the welcome doors in the active theme.
func (m welcomeModel) View() tea.View {
	st := gateStyle{tmuxTheme()}
	lines := []string{st.fg(st.th.Focus).Bold(true).Render("☎  PARTYLINE"), "", st.muted("multiplayer for your terminal"), "", ""}
	for i, d := range m.doors {
		mark := "  "
		if i == m.sel {
			mark = "▸ "
		}
		line := mark + st.fg(st.th.Focus).Render(d.disp) + "  " + st.text(d.title)
		if d.subtitle != "" {
			line += "  " + st.muted(d.subtitle)
		}
		lines = append(lines, line)
	}
	// Doors align as one column: every door row is padded to the widest, so centring moves them
	// together rather than giving each its own ragged centre.
	doorW := 0
	for _, l := range lines[5:] {
		doorW = max(doorW, ansix.StringWidth(l))
	}
	for i := 5; i < len(lines); i++ {
		lines[i] += strings.Repeat(" ", doorW-ansix.StringWidth(lines[i]))
	}
	exit := "quit"
	if m.canSwitch {
		exit = "switchboard"
	}
	lines = append(lines, "", st.faint("↑↓ · jk choose · ⏎ open · q / esc "+exit))
	return launcherScreen(lines, m.w, m.h)
}
