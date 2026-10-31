package main

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"partyline.sh/partyline/internal/gatewayjoin"
)

func (m *relaySetupModel) activate() tea.Cmd {
	m.scroll = 0
	switch m.stage {
	case "choose":
		m.method = m.selection
		m.note = ""
		if m.existing && m.method != 1 {
			m.note = "This computer already has a relay connection. It is preserved. You can host a separate private gateway here; changing the existing client connection is not part of this setup."
			return nil
		}
		switch m.method {
		case 0:
			m.stage = "discover"
			m.scanning = true
			m.selection = 0
			return m.scanCommand()
		case 1:
			name, _ := os.Hostname()
			return m.field("name", "Name of this private gateway", gatewayjoin.CleanName(name), false)
		case 2:
			return m.field("join-origin", "https://relay.example.net", "", false)
		case 3:
			m.stage = "cloudflare"
		}
	case "discover":
		if m.scanning {
			return nil
		}
		if m.selection < len(m.entries) {
			origin := m.entries[m.selection].Origin
			m.busy = true
			m.generation++
			attempt := m.generation
			return func() tea.Msg {
				result := startGatewayJoin(origin).(relayJoinReadyMsg)
				result.Attempt = attempt
				return result
			}
		}
		if m.selection == len(m.entries) {
			return m.field("invite", "Paste the one-use invite from your relay", "", true)
		}
		m.scanning = true
		m.note = ""
		return m.scanCommand()
	case "join-origin":
		origin := strings.TrimSpace(m.input.Value())
		if _, err := relayURL(origin); err != nil {
			m.note = err.Error()
			return nil
		}
		m.busy = true
		m.generation++
		attempt := m.generation
		return func() tea.Msg {
			result := startGatewayJoin(strings.TrimRight(origin, "/")).(relayJoinReadyMsg)
			result.Attempt = attempt
			return result
		}
	case "invite":
		if strings.TrimSpace(m.input.Value()) == "" {
			return nil
		}
		m.action = relaySetupAction{Kind: "invite", Invite: strings.TrimSpace(m.input.Value())}
		return tea.Quit
	case "verify":
		if !m.verified {
			m.verified = true
			m.note = "Code comparison confirmed here. Waiting for approval on the private gateway."
			if m.joinResult != nil {
				m.note = "Code comparison confirmed. Continue to save this connection."
			}
			return nil
		}
		if m.joinResult == nil {
			return nil
		}
		if !m.joinResult.Approved || len(m.joinResult.Token) != 64 {
			return nil
		}
		m.action = relaySetupAction{Kind: "join", Origin: m.joinOrigin, Machine: m.joinMachine, Token: m.joinResult.Token}
		m.cancelJoin()
		return tea.Quit
	case "name":
		m.name = gatewayjoin.CleanName(m.input.Value())
		m.stage = "route"
		m.selection = 0
	case "route":
		m.route = m.selection
		if m.route == 0 {
			origin, err := tailscaleRelayOrigin()
			if err != nil {
				m.note = err.Error()
				return nil
			}
			m.origin = origin
			m.stage = "review"
		} else {
			return m.field("origin", "https://relay.example.net", "", false)
		}
	case "origin":
		origin := strings.TrimSpace(m.input.Value())
		if _, err := relayURL(origin); err != nil {
			m.note = err.Error()
			return nil
		}
		m.origin = strings.TrimRight(origin, "/")
		m.stage = "review"
	case "review":
		m.action = relaySetupAction{Kind: "host", Origin: m.origin, Name: m.name, Route: map[int]string{0: "tailscale", 1: "proxy"}[m.route]}
		return tea.Quit
	case "cloudflare":
		m.action = relaySetupAction{Kind: "cloudflare"}
		return tea.Quit
	}
	return nil
}
