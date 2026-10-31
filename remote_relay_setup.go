package main

import (
	"context"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/coder/websocket"
	"partyline.sh/partyline/internal/gatewayjoin"
)

type relaySetupAction struct{ Kind, Origin, Name, Route, Invite, Machine, Token string }
type relayDiscoveryMsg struct {
	Entries []discoveredRelay
	Err     error
	Attempt int
}
type relayJoinReadyMsg struct {
	Channel         *gatewayjoin.Channel
	Conn            *websocket.Conn
	Machine, Origin string
	Cancel          context.CancelFunc
	Attempt         int
	Err             error
}
type relayJoinResultMsg struct {
	Result gatewayjoin.Result
	ID     string
	Err    error
}
type relaySetupModel struct {
	stage                    string
	generation               int
	joinOrigin, joinMachine  string
	selection, method, route int
	input                    textinput.Model
	name, origin, note       string
	entries                  []discoveredRelay
	scanning, busy, verified bool
	channel                  *gatewayjoin.Channel
	connection               *websocket.Conn
	cancel                   context.CancelFunc
	joinResult               *gatewayjoin.Result
	action                   relaySetupAction
	w, h                     int
	scroll                   int
	existing                 bool
}

func newRelaySetupModel() relaySetupModel {
	in := textinput.New()
	in.CharLimit = 2048
	in.Prompt = "> "
	return relaySetupModel{stage: "choose", input: in, w: 88, h: 32}
}
func (m relaySetupModel) Init() tea.Cmd { return nil }
func (m *relaySetupModel) cancelJoin() {
	m.busy = false
	if m.cancel != nil {
		m.cancel()
	}
	if m.connection != nil {
		m.connection.CloseNow()
	}
	m.cancel = nil
	m.connection = nil
	m.channel = nil
	m.joinResult = nil
	m.verified = false
}
func (m *relaySetupModel) field(stage, placeholder, value string, secret bool) tea.Cmd {
	m.scroll = 0
	m.stage = stage
	m.note = ""
	m.input.SetValue(value)
	m.input.Placeholder = placeholder
	m.input.EchoMode = textinput.EchoNormal
	if secret {
		m.input.EchoMode = textinput.EchoPassword
	}
	return m.input.Focus()
}
func (m relaySetupModel) optionCount() int {
	switch m.stage {
	case "choose":
		return 4
	case "discover":
		return len(m.entries) + 2
	case "route":
		return 2
	}
	return 0
}
func (m *relaySetupModel) scanCommand() tea.Cmd {
	m.generation++
	attempt := m.generation
	return func() tea.Msg { result := scanRelayCmd().(relayDiscoveryMsg); result.Attempt = attempt; return result }
}
