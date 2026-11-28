package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uitheme "partyline.sh/partyline/internal/theme"
)

type motionPrefs struct {
	Still bool `json:"still"`
	ASCII bool `json:"ascii"`
}

func motionPrefsPath() string { return filepath.Join(stateDir(), "presentation.json") }
func readMotionPrefs() motionPrefs {
	var p motionPrefs
	if b, err := os.ReadFile(motionPrefsPath()); err == nil {
		if json.Unmarshal(b, &p) != nil {
			return motionPrefs{}
		}
	}
	return p
}
func loadMotionPrefs() motionPrefs {
	p := readMotionPrefs()
	p.Still = p.Still || strings.TrimSpace(os.Getenv("PARTYLINE_STILL")) == "1" || strings.TrimSpace(os.Getenv("TERM")) == "dumb"
	locale := strings.TrimSpace(os.Getenv("LC_ALL"))
	if locale == "" {
		locale = strings.TrimSpace(os.Getenv("LC_CTYPE"))
	}
	if locale == "" {
		locale = strings.TrimSpace(os.Getenv("LANG"))
	}
	p.ASCII = p.ASCII || strings.TrimSpace(os.Getenv("NO_COLOR")) != "" || strings.TrimSpace(os.Getenv("TERM")) == "dumb" || (locale != "" && !strings.Contains(strings.ToLower(locale), "utf-8") && !strings.Contains(strings.ToLower(locale), "utf8"))
	return p
}
func saveMotionPrefs(p motionPrefs) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(motionPrefsPath(), append(b, '\n'), 0o600)
}

type uiMotionFrame struct{}
type uiMotionWatch struct{ visible bool }
type surfaceMotion struct {
	prefs                                      motionPrefs
	paint                                      uitheme.Theme
	frame, settle                              int
	active, pending, watching, hidden, blurred bool
}

func newSurfaceMotion() surfaceMotion {
	return surfaceMotion{prefs: loadMotionPrefs(), paint: tmuxTheme()}
}
func uiMotionFrameCmd() tea.Cmd {
	return tea.Tick(125*time.Millisecond, func(time.Time) tea.Msg { return uiMotionFrame{} })
}
func uiMotionWatchCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return uiMotionWatch{visible: motionPaneVisible()} })
}

// A slow visibility probe, never a frame-timer shell-out. Do not enable global
// focus-events: that would change focus handling inside the person's agent TUIs.
// Popup context is captured explicitly at open; inherited TMUX_PANE may be stale.
// Native tmux owns popup lifetime; closing it exits the Bubble Tea program.
func motionPaneVisible() bool {
	pane := uiPaneTarget()
	if pane == "" {
		return true
	}
	b, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_active} #{window_active} #{session_attached}").Output()
	f := strings.Fields(string(b))
	return err == nil && len(f) == 3 && f[0] == "1" && f[1] == "1" && f[2] != "0"
}
func (m *surfaceMotion) allowed() bool { return !m.prefs.Still && !m.hidden && !m.blurred }
func (m *surfaceMotion) handle(msg tea.Msg) bool {
	switch v := msg.(type) {
	case uiMotionFrame:
		m.pending = false
		if m.allowed() && (m.active || m.settle > 0) {
			m.frame++
			if m.settle > 0 {
				m.settle--
			}
		}
	case uiMotionWatch:
		m.watching = false
		m.hidden = !v.visible
		if m.hidden {
			m.settle = 0
		}
	case tea.FocusMsg:
		m.blurred = false
	case tea.BlurMsg:
		m.blurred = true
		m.settle = 0
	default:
		return false
	}
	return true
}
func (m *surfaceMotion) schedule(active bool) tea.Cmd {
	m.active = active
	if m.prefs.Still {
		m.settle = 0
	}
	var cmds []tea.Cmd
	if (active || m.settle > 0) && !m.prefs.Still && !m.blurred && !m.watching {
		m.watching = true
		cmds = append(cmds, uiMotionWatchCmd())
	}
	if m.allowed() && (active || m.settle > 0) && !m.pending {
		m.pending = true
		cmds = append(cmds, uiMotionFrameCmd())
	}
	return tea.Batch(cmds...)
}

func (m surfaceMotion) signal(state string, offset int) string {
	return uitheme.Signal(state, m.frame+offset, m.prefs.ASCII, m.allowed())
}

func (m surfaceMotion) theme() uitheme.Theme {
	if m.paint.Palette != "" {
		return m.paint
	}
	return tmuxTheme()
}

func (m *surfaceMotion) refreshPrefs() { m.prefs = loadMotionPrefs(); m.paint = tmuxTheme() }

type uiDataPoll struct{ visible bool }

func uiDataPollCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return uiDataPoll{visible: motionPaneVisible()} })
}
