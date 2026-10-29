package main

import (
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// launcherExec lets tests record pager requests without starting a process.
var launcherExec = tea.ExecProcess

// launcherKeyBytes translates terminal keys to the shared launcher's input protocol.
func launcherKeyBytes(msg tea.KeyPressMsg) []byte {
	keys := map[string]string{"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D", "pgup": "\x1b[5~", "pgdown": "\x1b[6~", "tab": "\t", "esc": "\x1b", "enter": "\r", "backspace": "\x7f", "ctrl+h": "\x08", "ctrl+c": "\x03", "ctrl+n": "\x0e", "space": " "}
	if b, ok := keys[msg.String()]; ok {
		return []byte(b)
	}
	return []byte(msg.Text)
}

// key delegates modal input before interpreting the menu's host requests.
func (m launcherPopup) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a := m.menu
	b := launcherKeyBytes(msg)
	if len(b) == 0 {
		return m, nil
	}
	ready := !a.picking && a.confirmDelete == nil && !a.help && !a.renaming && !a.filter
	if ready && string(b) == "\x1b" && len(a.marked) == 0 {
		// esc leaves the detail pane first (handleKey has no esc for it), then goes back.
		if a.focusR {
			a.focusR = false
			return m, nil
		}
		m.back = true
		return m, tea.Quit
	}
	if ready && string(b) == "\r" && len(a.marked) > 0 {
		m.sessions = m.markedSessions()
		a.marked = nil
		if len(m.sessions) > 0 {
			m.action = "open-many"
			return m, tea.Quit
		}
	}
	a.openTab = func(s aiSession) (string, error) {
		m.action = "terminal"
		cp := s
		m.picked = &cp
		return "opening terminal", nil
	}
	a.recoverSession = func(s aiSession, _ string) func() { m.action = "recover"; cp := s; m.picked = &cp; return nil }
	done, chosen := false, (*aiSession)(nil)
	// Text arrives as a complete paste; the legacy handler consumes one byte at a time.
	if len(b) > 1 && b[0] != '\x1b' {
		for _, c := range b {
			done, chosen = a.handleKey([]byte{c})
			if done {
				break
			}
		}
	} else {
		done, chosen = a.handleKey(b)
	}
	// These callbacks capture this update's result only.
	a.openTab, a.recoverSession = nil, nil
	if chosen != nil {
		m.picked = chosen
		m.action = "open"
	}
	switch {
	case a.spawnShell:
		a.spawnShell = false
		m.action = "new"
	case a.quickNew:
		a.quickNew = false
		m.action = "quick-new"
	case a.quickNewBypass:
		a.quickNewBypass = false
		m.action = "quick-bypass"
	case a.splitSetup:
		a.splitSetup = false
		m.action = "split"
		m.sessions = m.markedSessions()
	}
	if a.suspendFn != nil {
		a.suspendFn = nil
		if s := a.selected(); s != nil {
			return m, launcherExec(launcherDiffCommand(*s), func(err error) tea.Msg { return launcherPagerDone{err} })
		}
	}
	if m.action != "" || done {
		return m, tea.Quit
	}
	return m, nil
}

// launcherPagerDone reports a pager failure after the popup resumes.
type launcherPagerDone struct{ err error }

// launcherDiffCommand pipes the shared diff viewer's git output into its configured pager.
func launcherDiffCommand(s aiSession) *exec.Cmd {
	pager := strings.Fields(getLauncherPager())
	quoted := make([]string, len(pager))
	for i, p := range pager {
		quoted[i] = shQuote(p)
	}
	return exec.Command("sh", "-c", "git -C "+shQuote(s.Cwd)+" diff --color=always | "+strings.Join(quoted, " "))
}

// markedSessions preserves the shared launcher's display order and resumability gate.
func (m launcherPopup) markedSessions() []aiSession {
	var sessions []aiSession
	for _, s := range m.menu.view {
		if m.menu.marked[s.ID] && s.resumeArgv != nil {
			sessions = append(sessions, s)
		}
	}
	return sessions
}
