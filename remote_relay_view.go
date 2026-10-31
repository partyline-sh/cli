package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// The relay settings screen (ctrl-\ r, or `ptln relay` in a terminal): where the relay is, whether
// this machine is connected, whether the phone app is current, whether a Cloudflare token is saved,
// and the keys to deploy or update it, save or forget the token, and pair a phone. A deploy runs in
// this terminal, so its hidden prompt works here too.

type relayView struct {
	set                    bool // a relay is configured
	url                    string
	connected              string
	outdated               bool
	saved                  bool
	phones                 int
	hosting                string
	owner                  bool
	pending                int
	proxy                  string
	localHost              bool
	serverOnly             bool
	otherGateway           bool
	gatewayName, discovery string
	healthy                bool
	auto                   string
}

func loadRelayView() relayView {
	c, token, err := loadRelay()
	return loadRelayViewFor(c, token, err)
}

func loadRelayViewFor(c relayConfig, token string, err error) relayView {
	var v relayView
	if err != nil {
		return v
	}
	v.set, v.url, v.outdated, v.saved = true, c.URL, relayOutdated(c), savedRelayAPIToken() != ""
	v.hosting = c.Hosting
	if c.Hosting == "self-hosted" {
		v.saved = false
		if host, e := loadRelayHost(relayHostDir()); e == nil && host.Origin == c.URL && host.Machine == c.MachineID {
			v.localHost, v.owner = true, true
			v.pending = -1
			v.gatewayName = host.Name
			v.discovery = "off"
			if host.Discover {
				v.discovery = "configured on · availability depends on local multicast"
			}
		}
	}
	primary, _, primaryErr := loadRelay()
	v.serverOnly = v.localHost && (primaryErr != nil || primary.URL != c.URL || primary.MachineID != c.MachineID)
	if host, e := loadRelayHost(relayHostDir()); e == nil {
		v.otherGateway = host.Origin != c.URL || host.Machine != c.MachineID
	}
	v.auto = relayAutoLine(c)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var result struct {
		Connected bool   `json:"connected"`
		Owner     bool   `json:"owner"`
		Pending   int    `json:"pending_joins"`
		Proxy     string `json:"proxy_mode"`
	}
	v.connected = "unknown: the relay did not answer"
	if relayPost(ctx, c.URL, "/status", map[string]string{"id": c.MachineID, "token": token}, &result) == nil {
		v.healthy = true
		v.owner, v.pending = result.Owner, result.Pending
		v.proxy = result.Proxy
		v.connected = map[bool]string{true: "yes", false: "no: the watcher starts with partyline's tmux server"}[result.Connected]
		if c.Hosting == "self-hosted" && !result.Connected {
			v.connected = "not connected · open Partyline here to share this computer's sessions"
		}
	}
	if !v.serverOnly {
		phones, _ := remotePhones()
		v.phones = len(phones)
	} else {
		v.connected = "server only · this computer keeps its existing relay connection"
	}
	return v
}

type relayModel struct {
	v       relayView
	next    string // deploy, pair, or "" to close
	edit    bool   // typing a token
	in      textinput.Model
	note    string
	confirm string
	w, h    int
	scroll  int
}

func newRelayModel(v relayView) relayModel {
	in := textinput.New()
	in.EchoMode = textinput.EchoPassword
	in.Placeholder = "paste the token"
	return relayModel{v: v, in: in, w: 80, h: 30}
}

func (m relayModel) Init() tea.Cmd { return nil }

func (m relayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelDown {
			m.scroll += 3
		} else if msg.Button == tea.MouseWheelUp {
			m.scroll = max(0, m.scroll-3)
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		full := m.fullView().Content
		_, offset := relayViewport(full, m.h, m.scroll)
		lines := strings.Split(full, "\n")
		last := len(lines) - 1
		for last >= 0 && strings.TrimSpace(xansi.Strip(lines[last])) == "" {
			last--
		}
		rows := strings.Split(relayKeyRows(m.footerKeys(gateStyle{tmuxTheme()}), min(max(m.w-4, 30), 76)), "\n")
		clicked := msg.Y + offset
		if last < 0 || clicked < last-len(rows)+1 || clicked > last {
			return m, nil
		}
		line := xansi.Strip(lines[clicked])
		for _, button := range []struct{ label, key string }{{"⏎ confirm restart", "enter"}, {"⏎ save", "enter"}, {"esc cancel", "esc"}, {"esc close", "esc"}, {"s setup gateway", "s"}, {"g private gateway", "g"}, {"p pair a phone", "p"}, {"a approve computer", "a"}, {"r restart service", "r"}, {"u update the relay", "u"}, {"d deploy", "d"}, {"t save a token", "t"}, {"x forget it", "x"}} {
			at := strings.Index(line, button.label)
			if at < 0 {
				continue
			}
			left := xansi.StringWidth(line[:at])
			if msg.X >= left && msg.X < left+xansi.StringWidth(button.label) {
				var key tea.KeyPressMsg
				switch button.key {
				case "enter":
					key = tea.KeyPressMsg{Code: tea.KeyEnter}
				case "esc":
					key = tea.KeyPressMsg{Code: tea.KeyEsc}
				default:
					key = tea.KeyPressMsg{Code: rune(button.key[0]), Text: button.key}
				}
				return m.Update(key)
			}
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
		if m.confirm != "" {
			switch msg.String() {
			case "esc":
				m.confirm = ""
				m.scroll = 0
				return m, nil
			case "enter":
				m.next = m.confirm
				return m, tea.Quit
			}
			return m, nil
		}
		if m.edit {
			switch msg.String() {
			case "esc":
				m.edit = false
				m.in.SetValue("")
				return m, nil
			case "enter":
				token := strings.TrimSpace(m.in.Value())
				m.in.SetValue("")
				m.edit = false
				if token == "" {
					return m, nil
				}
				if err := saveRelayAPIToken(token); err != nil {
					m.note = "Not saved: " + err.Error()
				} else {
					m.v.saved, m.note = true, "Token saved."
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.in, cmd = m.in.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "d", "u", "enter":
			if m.v.hosting == "self-hosted" {
				m.note = "Your relay runs from ptln. Restart its service after an update; Cloudflare deployment is not used."
				return m, nil
			}
			m.next = "deploy"
			return m, tea.Quit
		case "t":
			if m.v.hosting == "self-hosted" {
				return m, nil
			}
			m.edit, m.note = true, ""
			m.scroll = 100000
			return m, m.in.Focus()
		case "x":
			if m.v.saved {
				if err := os.Remove(relayTokenPath()); err != nil {
					m.note = err.Error()
				} else {
					m.v.saved, m.note = false, "Saved token deleted."
				}
			}
		case "s":
			m.next = "setup"
			return m, tea.Quit
		case "g":
			if m.v.otherGateway {
				m.next = "gateway"
				return m, tea.Quit
			}
		case "p":
			if m.v.set && !m.v.serverOnly {
				m.next = "pair"
				return m, tea.Quit
			}
		case "a":
			if m.v.owner {
				m.next = "approve"
				return m, tea.Quit
			}
		case "r":
			if m.v.localHost {
				m.confirm = "restart"
				m.scroll = 100000
				return m, nil
			}
		}
	default:
		if m.edit {
			var cmd tea.Cmd
			m.in, cmd = m.in.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m relayModel) fullView() tea.View {
	st := gateStyle{tmuxTheme()}
	w := min(max(m.w-4, 30), 76)
	wrap := func(s string) string { return lipgloss.NewStyle().Width(w).Render(s) }
	var b strings.Builder
	b.WriteString(st.heading("Phone relay", w) + "\n\n")
	if m.v.hosting == "self-hosted" {
		b.WriteString(wrap(st.muted("Phones reach registered computers through your private gateway. It forwards encrypted content; it cannot read your sessions. Its host must remain awake. Relay membership and phone pairing are separate approvals.")) + "\n\n")
	} else {
		b.WriteString(wrap(st.muted("Phones reach this machine through a relay that runs in your own Cloudflare account. Nothing listens on this machine, and partyline runs no relay of its own.")) + "\n\n")
	}
	saved := "not saved: deploys ask for it, and a new partyline cannot update the relay by itself"
	if m.v.saved {
		saved = "saved in ~/.partyline/remote, readable only by you"
	}
	if !m.v.set {
		b.WriteString(st.row("Relay", "not deployed", w) + "\n")
		b.WriteString(st.row("Token", saved, w) + "\n\n")
	} else {
		app := "current"
		if m.v.hosting == "self-hosted" {
			app = "served by the gateway's ptln binary"
		}
		if m.v.outdated && m.v.hosting != "self-hosted" {
			app = st.warn("older than this partyline: press u to update it")
		}
		b.WriteString(st.row("Relay", m.v.url, w) + "\n")
		if m.v.hosting == "self-hosted" {
			health := "unknown · HTTPS did not answer"
			if m.v.healthy {
				health = "HTTPS answered · authenticated status received"
			}
			b.WriteString(st.row("Transport", health, w) + "\n")
			b.WriteString(st.row("This PC", m.v.connected, w) + "\n")
			if m.v.gatewayName != "" {
				b.WriteString(st.row("Gateway", relayScreenText(m.v.gatewayName), w) + "\n")
			}
			if m.v.discovery != "" {
				b.WriteString(st.row("Discovery", m.v.discovery, w) + "\n")
			}
		} else {
			b.WriteString(st.row("Connected", m.v.connected, w) + "\n")
		}
		b.WriteString(st.row("Phone app", app, w) + "\n")
		if m.v.hosting != "self-hosted" {
			b.WriteString(st.row("Token", saved, w) + "\n")
		}
		if m.v.owner {
			pending := "unknown · gateway not reachable"
			if m.v.pending >= 0 {
				pending = fmt.Sprintf("%d waiting · a to compare codes", m.v.pending)
			}
			b.WriteString(st.row("Requests", pending, w) + "\n")
		}
		if m.v.proxy != "" {
			mode := map[string]string{"direct": "TCP peer addresses · forwarded headers not used", "forwarded": "forwarded by trusted proxy", "shared": "proxy does not send client IPs; configure X-Forwarded-For"}[m.v.proxy]
			if mode == "" {
				mode = "unknown"
			}
			b.WriteString(st.row("Client IPs", mode, w) + "\n")
		}
		b.WriteString(st.row("Updates", m.v.auto, w) + "\n")
		phones := "none paired"
		if m.v.phones > 0 {
			phones = fmt.Sprintf("%d %s paired", m.v.phones, plural(m.v.phones, "phone", "phones"))
		}
		if !m.v.serverOnly {
			b.WriteString(st.row("Phones", phones, w) + "\n")
		}
		b.WriteString("\n")
	}
	if m.edit {
		m.in.SetWidth(max(w-14, 10))
		b.WriteString(st.row("New token", m.in.View(), w) + "\n\n")
	}
	if m.v.hosting != "self-hosted" {
		b.WriteString(wrap(st.muted("Deploying needs a Cloudflare API token: dash.cloudflare.com → My Profile → API Tokens → Create Token → \"Edit Cloudflare Workers\". It needs Node.js 22 or newer on this machine.")) + "\n")
	} else {
		b.WriteString(wrap(st.muted("If the gateway cannot be reached, this screen cannot tell whether it is asleep, disconnected or blocked. Local sessions may still be running. Check the gateway and its HTTPS/VPN route.")) + "\n")
	}
	if m.note != "" {
		b.WriteString("\n" + st.text(m.note) + "\n")
	}

	if m.confirm != "" {
		b.WriteString("\n" + wrap(st.warn("Restart this private gateway? Every connection through it will briefly stop. Settings and pairings are kept.")) + "\n")
	}
	b.WriteString("\n" + relayKeyRows(m.footerKeys(st), w))
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

func (m relayModel) View() tea.View {
	v := m.fullView()
	v.Content, _ = relayViewport(v.Content, m.h, m.scroll)
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// relaySettings runs the screen until it is closed; a deploy runs between showings, and pairing
// opens its own popup once this one has closed.
func relaySettings() error {
	gateway := false
	if _, _, err := loadRelay(); err != nil {
		if _, _, ownerErr := loadRelayOwner(); ownerErr != nil {
			return relaySetup()
		}
		gateway = true
	}
	for {
		var view relayView
		if gateway {
			c, token, err := loadRelayOwner()
			view = loadRelayViewFor(c, token, err)
		} else {
			view = loadRelayView()
		}
		final, err := tea.NewProgram(newRelayModel(view)).Run()
		if err != nil {
			return err
		}
		switch final.(relayModel).next {
		case "setup":
			if err := relaySetup(); err != nil {
				return err
			}
		case "gateway":
			gateway = true
		case "approve":
			if err := relayApprove(); err != nil {
				return err
			}
		case "restart":
			if err := relayServiceMain([]string{"restart"}); err != nil {
				return err
			}
		case "deploy":
			cmd := exec.Command(selfExe(), "relay", "deploy")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if cmd.Run() != nil {
				os.Stdout.WriteString("\nThe relay was not deployed. Press ⏎ to go back.\n")
			} else {
				os.Stdout.WriteString("\nDone. Press ⏎ to go back.\n")
			}
			_, _ = os.Stdin.Read(make([]byte, 64))
		case "pair":
			if insidePtlnTmux() {
				return tmuxCmd("run-shell", "-b", shQuote(selfExe())+" remote pair").Run()
			}
			return nil
		default:
			return nil
		}
	}
}
