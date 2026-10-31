package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"partyline.sh/partyline/internal/relayserver"
)

var relayJoinCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

type relayApprovalsMsg struct {
	Requests []relayserver.JoinRequest
	Err      error
}
type relayApprovalDoneMsg struct{ Err error }
type relayApprovalTick time.Time
type relayApprovalModel struct {
	h, scroll    int
	requests     []relayserver.JoinRequest
	selection, w int
	note         string
	busy         bool
}

func readRelayApprovals() tea.Msg {
	c, token, err := loadRelayOwner()
	if err != nil {
		return relayApprovalsMsg{Err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var result struct {
		Requests []relayserver.JoinRequest `json:"requests"`
	}
	err = relayPost(ctx, c.URL, "/join-requests", map[string]string{"id": c.MachineID, "token": token}, &result)
	if err == nil {
		for _, request := range result.Requests {
			if !relayJoinCodePattern.MatchString(request.Code) {
				return relayApprovalsMsg{Err: fmt.Errorf("invalid gateway verification code")}
			}
		}
	}
	sort.Slice(result.Requests, func(i, j int) bool { return result.Requests[i].Expires < result.Requests[j].Expires })
	return relayApprovalsMsg{result.Requests, err}
}
func relayApprovalClock() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return relayApprovalTick(t) })
}
func (m relayApprovalModel) Init() tea.Cmd {
	return tea.Batch(readRelayApprovals, relayApprovalClock())
}
func (m relayApprovalModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case relayApprovalsMsg:
		previous := ""
		if m.selection >= 0 && m.selection < len(m.requests) {
			previous = m.requests[m.selection].ID
		}
		m.requests = msg.Requests
		m.selection = -1
		for i, request := range m.requests {
			if request.ID == previous {
				m.selection = i
				break
			}
		}
		if msg.Err != nil {
			m.note = "Cannot read join requests. This requires the private gateway owner's registration and reachable HTTPS."
		} else if previous != "" && m.selection < 0 {
			m.note = "The selected request left or expired. Choose another request before comparing its code."
		} else {
			m.note = ""
		}

	case relayApprovalTick:
		return m, tea.Batch(readRelayApprovals, relayApprovalClock())
	case relayApprovalDoneMsg:
		m.busy = false
		if msg.Err != nil {
			m.note = "Not approved: " + msg.Err.Error()
		} else {
			m.note = "Request handled."
		}
		return m, readRelayApprovals
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelDown {
			m.scroll += 3
		} else if msg.Button == tea.MouseWheelUp {
			m.scroll = max(0, m.scroll-3)
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || m.busy {
			return m, nil
		}
		content, hits := m.layout()
		_, offset := relayViewport(content, m.h, m.scroll)
		y := msg.Y + offset
		if msg.Y >= max(m.h-2, 1) {
			return m, nil
		}
		for _, hit := range hits {
			if y >= hit.Top && y < hit.Bottom && msg.X >= 2 && msg.X < m.w-2 {
				if hit.Index >= 0 {
					m.selection = hit.Index
					return m, nil
				}
				if hit.Index == -1 {
					return m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
				return m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
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
		if m.busy && msg.String() != "esc" && msg.String() != "ctrl+c" {
			return m, nil
		}
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if len(m.requests) > 0 {
				if m.selection < 0 {
					m.selection = len(m.requests) - 1
				} else {
					m.selection = (m.selection + len(m.requests) - 1) % len(m.requests)
				}
			}
		case "down", "j", "tab":
			if len(m.requests) > 0 {
				m.selection = (m.selection + 1) % len(m.requests)
			}
		case "enter", "d":
			if m.busy || m.selection < 0 || m.selection >= len(m.requests) {
				return m, nil
			}
			request := m.requests[m.selection]
			approved := msg.String() == "enter"
			m.busy = true
			return m, func() tea.Msg {
				c, token, err := loadRelayOwner()
				if err != nil {
					return relayApprovalDoneMsg{err}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				var result struct {
					OK bool `json:"ok"`
				}
				err = relayPost(ctx, c.URL, "/join-approve", map[string]any{"id": c.MachineID, "token": token, "request": request.ID, "code": request.Code, "approved": approved}, &result)
				if err == nil && !result.OK {
					err = fmt.Errorf("gateway did not confirm approval")
				}
				return relayApprovalDoneMsg{err}
			}
		}
	}
	return m, nil
}
func (m relayApprovalModel) layout() (string, []relaySetupHit) {
	st := gateStyle{tmuxTheme()}
	w := min(max(m.w-4, 26), 84)
	wrap := func(text string) string { return lipgloss.NewStyle().Width(w).Render(text) }
	var b strings.Builder
	hits := []relaySetupHit{}
	b.WriteString(st.heading("Approve a computer on this private gateway", w) + "\n\n")
	if len(m.requests) == 0 {
		b.WriteString(wrap(st.muted("No pending joins. On the other computer, open ptln relay setup and choose this private gateway. Discovery grants no membership.")) + "\n\n")
	} else {
		start := max(0, m.selection-2)
		end := min(len(m.requests), start+6)
		b.WriteString(wrap(st.muted(fmt.Sprintf("%d pending · select a computer to compare its code", len(m.requests)))) + "\n\n")
		for i := start; i < end; i++ {
			prefix := "  "
			if i == m.selection {
				prefix = "› "
			}
			top := strings.Count(b.String(), "\n") + 1
			b.WriteString(wrap(st.text(prefix+relayScreenText(m.requests[i].Name))) + "\n")
			hits = append(hits, relaySetupHit{top, strings.Count(b.String(), "\n") + 1, i})
		}
		if m.selection >= 0 && m.selection < len(m.requests) {
			request := m.requests[m.selection]
			b.WriteString("\n" + st.code(request.Code) + "\n\n" + wrap(st.text("Does "+relayScreenText(request.Name)+" show this same code?")) + "\n\n" + wrap(st.muted("Compare on that computer before approving. This grants relay membership; phone pairing is separate.")) + "\n\n")
			if !m.busy {
				for _, action := range []struct {
					label string
					index int
				}{{"Approve matching code", -1}, {"Decline", -2}} {
					top := strings.Count(b.String(), "\n") + 1
					b.WriteString(st.code("[ "+action.label+" ]") + "\n")
					hits = append(hits, relaySetupHit{top, strings.Count(b.String(), "\n") + 1, action.index})
				}
			}
		} else {
			b.WriteString("\n" + wrap(st.muted("Choose a request to show its code. Nothing is approved automatically.")) + "\n")
		}
	}
	if m.note != "" {
		b.WriteString("\n" + wrap(st.warn(relayScreenText(m.note))) + "\n")
	}
	if m.busy {
		b.WriteString("\n" + st.muted("Handling the selected request…") + "\n")
	}
	b.WriteString("\n" + wrap(st.muted("↑/↓ choose   Enter approve   d decline   Esc close")))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String()), hits
}
func (m relayApprovalModel) View() tea.View {
	content, _ := m.layout()
	content, _ = relayViewport(content, m.h, m.scroll)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor = lipgloss.Color(tmuxTheme().Surface)
	return v
}

func relayApprove() error {
	_, err := tea.NewProgram(relayApprovalModel{w: 88, h: 32, selection: -1}).Run()
	return err
}
