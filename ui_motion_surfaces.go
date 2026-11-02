package main

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

func (m chatPopup) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	handled := m.motion.handle(msg)
	if !handled {
		var next tea.Model
		wasActive := m.motion.active
		next, cmd = m.update(msg)
		m = next.(chatPopup)
		if _, ok := msg.(chatTick); ok {
			m.motion.refreshPrefs()
		}
		if _, ok := msg.(chatTick); ok && wasActive && !m.motion.active {
			for _, line := range m.in.status {
				if strings.HasPrefix(line, "✓") {
					m.motion.settle = 6
					break
				}
			}
		}
	}
	if !handled && !motionRefresh(msg) {
		return m, cmd
	}
	animation := m.motion.schedule(m.motion.active)
	return m, tea.Batch(cmd, animation)
}
func (m chatPopup) statusSignal(line string, offset int) string {
	r, n := utf8.DecodeRuneInString(line)
	if strings.ContainsRune(chatSpinner, r) {
		return m.motion.signal("working", offset) + line[n:]
	}
	if strings.HasPrefix(line, "✓") {
		return m.motion.signal("done", offset) + line[len("✓"):]
	}
	return line
}

func (m tabModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.motion.active = m.animating()
	var cmd tea.Cmd
	handled := m.motion.handle(msg)
	if handled {
		m.frame = m.motion.frame
	} else {
		var next tea.Model
		before := m.rows
		next, cmd = m.update(msg)
		m = next.(tabModel)
		if _, ok := msg.(tabTick); ok {
			m.watcherText = watcherLine(m.act.now())
			m.motion.refreshPrefs()
			for _, r := range m.rows {
				if r.State != loopPRReady {
					continue
				}
				for _, old := range before {
					if old.RunID == r.RunID && old.State != loopPRReady {
						if m.justDone == nil {
							m.justDone = map[string]bool{}
						}
						m.justDone[r.RunID] = true
						m.motion.settle = 6
					}
				}
			}
		}
	}
	if !handled && !motionRefresh(msg) {
		m.motion.active = m.animating()
		return m, cmd
	}
	animation := m.motion.schedule(m.animating())
	if m.motion.settle == 0 {
		m.justDone = nil
	}
	return m, tea.Batch(cmd, animation)
}
func (m tabModel) animating() bool {
	if m.detail || m.full || m.contractors {
		return false
	}
	for _, r := range m.rows {
		if rowAnimating(r) {
			return true
		}
	}
	return false
}
func rowAnimating(r tabRow) bool {
	return r.Reset.IsZero() && r.State != loopPaused && (r.Section == tabInReview || r.Section == tabInProgress && (r.Age <= tabQuietAfter || r.Builder != ""))
}

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.motion.active = m.animating()
	var cmd tea.Cmd
	handled := m.motion.handle(msg)
	if !handled {
		var next tea.Model
		next, cmd = m.update(msg)
		m = next.(menuModel)
	}
	if poll, ok := msg.(uiDataPoll); ok {
		m.motion.hidden = !poll.visible
		if !poll.visible || m.motion.blurred {
			m.polling = true
			return m, uiDataPollCmd()
		}
		m.polling = false
		return m, func() tea.Msg { return menuStatuses(tmuxSessionStatuses()) }
	}
	var poll tea.Cmd
	if _, ok := msg.(menuStatuses); ok {
		m.motion.refreshPrefs()
		if !m.polling {
			m.polling = true
			poll = uiDataPollCmd()
		}
	}

	if !handled && !motionRefresh(msg) {
		m.motion.active = m.animating()
		return m, cmd
	}
	animation := m.motion.schedule(m.animating())
	return m, tea.Batch(cmd, animation, poll)
}
func (m menuModel) animating() bool {
	if m.tab != 0 || m.renaming {
		return false
	}
	for _, i := range m.tabItems(0) {
		if m.items[i].status == "active" {
			return true
		}
	}
	return false
}

func (m launcherPopup) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.menu.motion.active = m.animating()
	var cmd tea.Cmd
	handled := m.menu.motion.handle(msg)
	if !handled {
		var next tea.Model
		next, cmd = m.update(msg)
		m = next.(launcherPopup)
		if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "t" && !m.menu.filter && !m.menu.renaming && !m.menu.picking && !m.menu.help && m.menu.confirmDelete == nil {
			m.menu.motion.refreshPrefs() // palette cycling must still repaint immediately
		}
	}
	if poll, ok := msg.(uiDataPoll); ok && m.scan != nil {
		m.menu.motion.hidden = !poll.visible
		if !poll.visible || m.menu.motion.blurred {
			m.polling = true
			return m, uiDataPollCmd()
		}
		m.polling = false
		scan, live := m.scan, m.live
		return m, func() tea.Msg {
			var keys map[string]bool
			if live != nil {
				keys = live()
			}
			return launcherSessions{all: scan(), live: keys}
		}
	}
	var poll tea.Cmd
	if _, ok := msg.(launcherSessions); ok {
		m.menu.motion.refreshPrefs()
		if !m.polling && m.scan != nil {
			m.polling = true
			poll = uiDataPollCmd()
		}
	}

	if !handled && !motionRefresh(msg) {
		m.menu.motion.active = m.animating()
		return m, cmd
	}
	animation := m.menu.motion.schedule(m.animating())
	return m, tea.Batch(cmd, animation, poll)
}
func (m launcherPopup) animating() bool {
	if m.menu.help || m.menu.renaming || m.menu.picking || m.menu.confirmDelete != nil {
		return false
	}
	for _, s := range m.menu.view {
		if s.Status == "active" {
			return true
		}
	}
	return false
}

func motionRefresh(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.WindowSizeMsg, chatTick, tabTick, menuStatuses, launcherSessions:
		return true
	}
	return false
}
