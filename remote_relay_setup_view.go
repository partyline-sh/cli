package main

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type relaySetupHit struct{ Top, Bottom, Index int }

func (m relaySetupModel) layout() (string, []relaySetupHit) {
	st := gateStyle{tmuxTheme()}
	width := min(max(m.w-4, 26), 88)
	var b strings.Builder
	hits := []relaySetupHit{}
	wrap := func(text string) string { return lipgloss.NewStyle().Width(width).Render(text) }
	line := func(text string) { b.WriteString(wrap(text) + "\n") }
	title := func(text, description string) {
		line(st.heading(text, width))
		line("")
		line(st.muted(description))
		line("")
	}
	option := func(index int, label, description string) {
		top := strings.Count(b.String(), "\n") + 1
		prefix := "  ○ "
		if m.selection == index {
			prefix = "  › "
			label = st.code(label)
		}
		line(prefix + label)
		line(st.muted("    " + description))
		line("")
		hits = append(hits, relaySetupHit{top, strings.Count(b.String(), "\n") + 1, index})
	}
	field := func(label string) {
		line(st.text(label))
		m.input.SetWidth(max(width-4, 20))
		line(m.input.View())
		line("")
	}
	button := func(label string) {
		top := strings.Count(b.String(), "\n") + 1
		line(st.code("[ " + label + " ]"))
		hits = append(hits, relaySetupHit{top, strings.Count(b.String(), "\n") + 1, -1})
	}
	switch m.stage {
	case "choose":
		title("Connect your computers", "Choose a relay. Your phone pairs separately with each computer; the relay cannot read encrypted session content.")
		option(0, "Join a nearby private gateway", "Discover it on this network, then compare a code before joining.")
		option(1, "Make this computer the private gateway", "Run the relay in ptln. This computer must stay awake.")
		option(2, "Connect to my server", "Use a trusted HTTPS relay already running on your server.")
		option(3, "Use my Cloudflare account", "Deploy to your account; no computer needs to host the relay.")
	case "discover":
		title("Find a private gateway", "Discovery suggests a name and address. It never grants access or establishes trust.")
		if m.scanning {
			line(st.muted("Looking on local multicast networks…"))
		} else {
			if len(m.entries) == 0 {
				line(st.warn("No private gateways found."))
				line(st.muted("The gateway may be offline, or guest Wi-Fi/VLAN isolation may block discovery."))
				line("")
			}
			for i, entry := range m.entries {
				option(i, entry.Name, entry.Origin+" · identity unverified")
			}
			option(len(m.entries), "Use an invite instead", "Paste a one-use invite from your gateway or public relay.")
			option(len(m.entries)+1, "Scan again", "Nothing is joined automatically.")
		}
	case "join-origin":
		title("Connect to your server", "Use its trusted HTTPS origin. On the server, ptln relay setup configures the Go relay. The server must stay on.")
		field("Relay HTTPS address")
		line(st.muted("This requests a verified join. For an invite, go back → nearby gateway → use an invite."))
		button("Request to join")
	case "invite":
		title("Join with an invite", "On an already registered computer, run ptln relay invite. It is one use and expires after ten minutes.")
		field("One-use invite (hidden)")
		line(st.muted("The invite authorizes relay membership, not phone access to sessions."))
		button("Redeem invite")
	case "verify":
		title("Verify the private gateway", "Compare this code with the approval screen on the gateway. Do not continue if they differ.")
		if m.channel != nil {
			line(st.code("    " + m.channel.Code))
			line("")
		}
		line(st.muted("On the gateway: ptln relay approve"))
		line("")
		if m.verified {
			line(st.text("✓ Code comparison confirmed here."))
		} else {
			line(st.warn("Code comparison not yet confirmed here."))
		}
		if m.joinResult != nil {
			line(st.text("✓ Gateway approved. Your registration is not saved until you continue."))
		} else {
			line(st.muted("Waiting for the gateway to approve this computer…"))
		}
		line("")
		if !m.verified {
			button("I checked the code and it matches")
		} else if m.joinResult != nil {
			button("Save this verified connection")
		} else {
			line(st.muted("You can cancel safely with Esc."))
		}
	case "name":
		title("Name your private gateway", "Other computers will see this name on your local network. They still need approval to join.")
		field("Gateway name")
		line(st.warn("Keep this host awake. Its remote routes stop when it sleeps."))
		button("Choose how the phone connects")
	case "route":
		title("Choose a reachable route", "The phone needs trusted HTTPS, including when it connects through a private VPN.")
		option(0, "Use Tailscale privately", "Requires Tailscale connected here and on the phone. HTTPS stays private to your tailnet.")
		option(1, "Use my existing HTTPS proxy", "For Caddy, a server reverse proxy, Headscale/NetBird with your certificates, or a public DNS route.")
		line(st.muted("Dynamic DNS alone does not make a home connection reachable through carrier-grade NAT."))
	case "origin":
		title("Set your stable HTTPS address", "Configure your proxy to forward this origin to 127.0.0.1:8787, including binary WebSockets. It must append or overwrite X-Forwarded-For with the real client address.")
		field("HTTPS origin")
		line(st.muted("An outside connection still needs reachable inbound ports or your own tunnel. No router or DNS settings are changed automatically."))
		button("Review setup")
	case "review":
		title("Review private gateway setup", "Nothing has been installed yet. Confirm these changes before starting the service.")
		line(st.row("Name", m.name, width))
		line(st.row("HTTPS", m.origin, width))
		line(st.row("Listen", "127.0.0.1:8787", width))
		line(st.row("Discovery", "On · names visible locally, joins require approval", width))
		line(st.row("Data", relayHostDir(), width))
		if m.existing {
			line(st.row("This PC", "existing relay connection preserved; gateway runs server-only", width))
		}
		line("")
		if plan, err := relayServiceSpecCurrent(); err == nil {
			line(st.muted("Installs " + plan.Path))
			line(st.muted(plan.Why))
		}
		if m.route == 0 {
			line(st.warn("Also configures tailscale serve to forward private HTTPS to this relay."))
		} else {
			line(st.warn("You must configure the HTTPS proxy yourself. Setup checks the address afterward."))
		}
		line("")
		button("Create gateway and start background service")
	case "cloudflare":
		title("Review Cloudflare deployment", "Deploy the existing relay into your own account. The next screen asks for your token without echoing it.")
		line(st.muted("Requires Node.js 22+ and npm. Deployment can change the relay in your account; existing connections will reconnect afterward."))
		line("")
		button("Continue to Cloudflare deployment")
	}
	if m.busy {
		line("")
		line(st.muted("Connecting over trusted HTTPS… Esc cancels."))
	}
	if m.note != "" {
		line("")
		line(st.warn(relayScreenText(m.note)))
	}
	line("")
	line(st.muted("↑/↓ select   Enter continue   Esc back/cancel   Ctrl-C close"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String()), hits
}
func (m relaySetupModel) View() tea.View {
	content, _ := m.visibleLayout()
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor = lipgloss.Color(tmuxTheme().Surface)
	return v
}

func relayScreenText(text string) string {
	out := []rune{}
	for _, r := range text {
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
		if len(out) >= 2048 {
			break
		}
	}
	return string(out)
}
func (m relaySetupModel) visibleLayout() (string, int) {
	content, _ := m.layout()
	return relayViewport(content, m.h, m.scroll)
}
func relayViewport(content string, height, scroll int) (string, int) {
	lines := strings.Split(content, "\n")
	if height <= 0 {
		height = 32
	}
	height = max(height, 8)
	if len(lines) <= height {
		return content, 0
	}
	room := height - 2
	offset := min(max(scroll, 0), len(lines)-room)
	return strings.Join(lines[offset:offset+room], "\n") + "\n" + (gateStyle{tmuxTheme()}).muted(fmt.Sprintf("  PgUp/PgDn or wheel to scroll · %d/%d", offset+1, len(lines))), offset
}

func (m *relaySetupModel) keepSelectionVisible() {
	_, hits := m.layout()
	room := max(m.h-2, 6)
	for _, hit := range hits {
		if hit.Index == m.selection {
			if hit.Top < m.scroll {
				m.scroll = hit.Top
			}
			if hit.Bottom > m.scroll+room {
				m.scroll = max(0, hit.Bottom-room)
			}
			break
		}
	}
}
