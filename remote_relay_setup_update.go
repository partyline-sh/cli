package main

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m relaySetupModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case relayDiscoveryMsg:
		if m.stage != "discover" || msg.Attempt != m.generation {
			return m, nil
		}
		m.scroll = 0
		m.entries, m.scanning = msg.Entries, false
		m.busy = false
		if msg.Err != nil {
			m.note = msg.Err.Error()
		}
		m.selection = 0
		return m, nil
	case relayJoinReadyMsg:
		if msg.Attempt != m.generation || (m.stage != "discover" && m.stage != "join-origin") {
			if msg.Cancel != nil {
				msg.Cancel()
			}
			if msg.Conn != nil {
				msg.Conn.CloseNow()
			}
			return m, nil
		}
		m.busy = false
		if msg.Err != nil {
			m.note = msg.Err.Error() + " · Use an invite if this gateway does not support verified discovery joins."
			return m, nil
		}
		m.stage = "verify"
		m.scroll = 0
		m.channel, m.connection, m.cancel = msg.Channel, msg.Conn, msg.Cancel
		m.joinOrigin, m.joinMachine = msg.Origin, msg.Machine
		m.verified = false
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			result, err := msg.Channel.Receive(ctx)
			return relayJoinResultMsg{Result: result, ID: msg.Channel.ID, Err: err}
		}
	case relayJoinResultMsg:
		if m.stage != "verify" || m.channel == nil || m.channel.ID != msg.ID {
			return m, nil
		}
		if msg.Err != nil {
			m.note = "The join expired or disconnected. Nothing was saved. Go back to try again."
			return m, nil
		}
		if !msg.Result.Approved {
			m.note = "The private gateway declined this join. Nothing was saved."
			return m, nil
		}
		m.joinResult = &msg.Result
		m.note = ""
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		_, hits := m.layout()
		_, offset := m.visibleLayout()
		y := msg.Y + offset
		if msg.Y >= max(m.h-2, 1) {
			return m, nil
		}
		for _, hit := range hits {
			if y >= hit.Top && y < hit.Bottom && msg.X >= 2 && msg.X < m.w-2 {
				if hit.Index >= 0 {
					m.selection = hit.Index
				}
				cmd := m.activate()
				return m, cmd
			}
		}
		return m, nil
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelDown {
			m.scroll += 3
		} else if msg.Button == tea.MouseWheelUp {
			m.scroll = max(0, m.scroll-3)
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "pgdown" {
			m.scroll += max(m.h-4, 1)
			return m, nil
		}
		if msg.String() == "pgup" {
			m.scroll = max(0, m.scroll-max(m.h-4, 1))
			return m, nil
		}
		if msg.String() == "ctrl+c" {
			m.generation++
			m.cancelJoin()
			m.action = relaySetupAction{}
			return m, tea.Quit
		}
		if msg.String() == "esc" {
			m.generation++
			m.cancelJoin()
			m.action = relaySetupAction{}
			if m.stage == "choose" {
				return m, tea.Quit
			}
			m.stage = "choose"
			m.selection = 0
			m.note = ""
			return m, nil
		}
		if m.busy {
			return m, nil
		}
		if m.stage == "name" || m.stage == "origin" || m.stage == "invite" || m.stage == "join-origin" {
			if msg.String() == "enter" {
				cmd := m.activate()
				return m, cmd
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if msg.String() == "enter" || msg.String() == " " {
			cmd := m.activate()
			return m, cmd
		}
		if n := m.optionCount(); n > 0 {
			switch msg.String() {
			case "up", "k":
				m.selection = (m.selection + n - 1) % n
			case "down", "j", "tab":
				m.selection = (m.selection + 1) % n
			}
			m.keepSelectionVisible()
		}
		return m, nil
	default:
		if m.stage == "name" || m.stage == "origin" || m.stage == "invite" || m.stage == "join-origin" {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(message)
			return m, cmd
		}
	}
	return m, nil
}
