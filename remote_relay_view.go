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
)

// The relay settings screen (ctrl-\ r, or `ptln relay` in a terminal): where the relay is, whether
// this machine is connected, whether the phone app is current, whether a Cloudflare token is saved,
// and the keys to deploy or update it, save or forget the token, and pair a phone. A deploy runs in
// this terminal, so its hidden prompt works here too.

type relayView struct {
	set       bool // a relay is configured
	url       string
	connected string
	outdated  bool
	saved     bool
	phones    int
	auto      string
}

func loadRelayView() relayView {
	var v relayView
	c, token, err := loadRelay()
	if err != nil {
		return v
	}
	v.set, v.url, v.outdated, v.saved = true, c.URL, relayOutdated(c), savedRelayAPIToken() != ""
	v.auto = relayAutoLine(c)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var result struct {
		Connected bool `json:"connected"`
	}
	v.connected = "unknown: the relay did not answer"
	if relayPost(ctx, c.URL, "/status", map[string]string{"id": c.MachineID, "token": token}, &result) == nil {
		v.connected = map[bool]string{true: "yes", false: "no: the watcher starts with partyline's tmux server"}[result.Connected]
	}
	phones, _ := remotePhones()
	v.phones = len(phones)
	return v
}

type relayModel struct {
	v    relayView
	next string // deploy, pair, or "" to close
	edit bool   // typing a token
	in   textinput.Model
	note string
	w, h int
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
	case tea.KeyPressMsg:
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
			m.next = "deploy"
			return m, tea.Quit
		case "t":
			m.edit, m.note = true, ""
			return m, m.in.Focus()
		case "x":
			if m.v.saved {
				if err := os.Remove(relayTokenPath()); err != nil {
					m.note = err.Error()
				} else {
					m.v.saved, m.note = false, "Saved token deleted."
				}
			}
		case "p":
			if m.v.set {
				m.next = "pair"
				return m, tea.Quit
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

func (m relayModel) View() tea.View {
	st := gateStyle{tmuxTheme()}
	w := min(max(m.w-4, 30), 76)
	wrap := func(s string) string { return lipgloss.NewStyle().Width(w).Render(s) }
	var b strings.Builder
	b.WriteString(st.heading("Phone relay", w) + "\n\n")
	b.WriteString(wrap(st.muted("Phones reach this machine through a relay that runs in your own Cloudflare account. Nothing listens on this machine, and partyline runs no relay of its own.")) + "\n\n")
	saved := "not saved: deploys ask for it, and a new partyline cannot update the relay by itself"
	if m.v.saved {
		saved = "saved in ~/.partyline/remote, readable only by you"
	}
	if !m.v.set {
		b.WriteString(st.row("Relay", "not deployed", w) + "\n")
		b.WriteString(st.row("Token", saved, w) + "\n\n")
	} else {
		app := "current"
		if m.v.outdated {
			app = st.warn("older than this partyline: press u to update it")
		}
		b.WriteString(st.row("Relay", m.v.url, w) + "\n")
		b.WriteString(st.row("Connected", m.v.connected, w) + "\n")
		b.WriteString(st.row("Phone app", app, w) + "\n")
		b.WriteString(st.row("Token", saved, w) + "\n")
		b.WriteString(st.row("Updates", m.v.auto, w) + "\n")
		phones := "none paired"
		if m.v.phones > 0 {
			phones = fmt.Sprintf("%d %s paired", m.v.phones, plural(m.v.phones, "phone", "phones"))
		}
		b.WriteString(st.row("Phones", phones, w) + "\n\n")
	}
	if m.edit {
		m.in.SetWidth(max(w-14, 10))
		b.WriteString(st.row("New token", m.in.View(), w) + "\n\n")
	}
	b.WriteString(wrap(st.muted("Deploying needs a Cloudflare API token: dash.cloudflare.com → My Profile → API Tokens → Create Token → \"Edit Cloudflare Workers\". It needs Node.js 22 or newer on this machine.")) + "\n")
	if m.note != "" {
		b.WriteString("\n" + st.text(m.note) + "\n")
	}
	keys := []string{st.key("d", "deploy")}
	if m.v.set {
		keys = []string{st.key("u", "update the relay"), st.key("p", "pair a phone")}
	}
	keys = append(keys, st.key("t", "save a token"))
	if m.v.saved {
		keys = append(keys, st.key("x", "forget it"))
	}
	keys = append(keys, st.key("esc", "close"))
	if m.edit {
		keys = []string{st.key("⏎", "save"), st.key("esc", "cancel")}
	}
	b.WriteString("\n" + strings.Join(keys, "   "))
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// relaySettings runs the screen until it is closed; a deploy runs between showings, and pairing
// opens its own popup once this one has closed.
func relaySettings() error {
	for {
		final, err := tea.NewProgram(newRelayModel(loadRelayView())).Run()
		if err != nil {
			return err
		}
		switch final.(relayModel).next {
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
