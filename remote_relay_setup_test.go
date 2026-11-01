package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"partyline.sh/partyline/internal/gatewayjoin"
	"partyline.sh/partyline/internal/relayserver"
)

func setupKey(m relaySetupModel, key tea.KeyPressMsg) relaySetupModel {
	next, _ := m.Update(key)
	return next.(relaySetupModel)
}
func TestRelaySetupSelectsWithoutWritingAndMouseMatchesKeys(t *testing.T) {
	m := newRelaySetupModel()
	m = setupKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = setupKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.stage != "name" || m.action.Kind != "" {
		t.Fatal("selection did not enter local gateway setup")
	}
	m = newRelaySetupModel()
	_, hits := m.layout()
	for _, hit := range hits {
		if hit.Index == 1 {
			next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: hit.Top})
			m = next.(relaySetupModel)
			break
		}
	}
	if m.stage != "name" {
		t.Fatal("mouse target differs from visible row")
	}
}
func TestRelayJoinCodeAppearsOnceAndCancelNeverSaves(t *testing.T) {
	m := newRelaySetupModel()
	m.stage = "verify"
	m.channel = &gatewayjoin.Channel{Code: "482917", ID: "request-a"}
	m.joinOrigin = "https://relay.test"
	m.joinMachine = strings.Repeat("1", 32)
	if got := strings.Count(gateANSI.ReplaceAllString(m.View().Content, ""), "482917"); got != 1 {
		t.Fatalf("code shown %d times", got)
	}
	m = setupKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.action.Kind != "" {
		t.Fatal("cancel returned a save action")
	}
	m.stage = "verify"
	m.channel = &gatewayjoin.Channel{Code: "482917", ID: "request-a"}
	m.joinResult = &gatewayjoin.Result{Approved: true, Token: strings.Repeat("2", 64)}
	m = setupKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.action.Kind != "" || !m.verified {
		t.Fatal("gateway approval skipped local comparison")
	}
	m = setupKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.action.Kind != "join" || len(m.action.Token) != 64 {
		t.Fatal("verified join did not finish")
	}
}
func TestGatewayApprovalCodeNotDuplicatedAndTokenNeverRendered(t *testing.T) {
	m := relayApprovalModel{w: 80, requests: []relayserver.JoinRequest{{ID: "request", Code: "482917", Name: "Workstation"}}}
	view := gateANSI.ReplaceAllString(m.View().Content, "")
	if strings.Count(view, "482917") != 1 {
		t.Fatal("approval code duplicated")
	}
	s := newRelaySetupModel()
	s.stage = "verify"
	s.channel = &gatewayjoin.Channel{Code: "482917", ID: "a"}
	s.joinResult = &gatewayjoin.Result{Approved: true, Token: "sensitive-registration-token"}
	if strings.Contains(s.View().Content, "sensitive-registration-token") {
		t.Fatal("token rendered in setup")
	}
}
func TestStaleJoinResponseCannotApproveAnotherAttempt(t *testing.T) {
	m := newRelaySetupModel()
	m.stage = "verify"
	m.channel = &gatewayjoin.Channel{ID: "current", Code: "482917"}
	m.joinOrigin = "https://current.test"
	m.joinMachine = strings.Repeat("1", 32)
	next, _ := m.Update(relayJoinResultMsg{ID: "old", Result: gatewayjoin.Result{Approved: true, Token: strings.Repeat("2", 64)}})
	m = next.(relaySetupModel)
	if m.joinResult != nil {
		t.Fatal("old attempt supplied credentials to new join")
	}
}

func TestVerifiedJoinReadyUnlocksComparisonAndIgnoresOldDiscovery(t *testing.T) {
	m := newRelaySetupModel()
	m.stage = "discover"
	m.busy = true
	m.generation = 2
	next, _ := m.Update(relayJoinReadyMsg{Attempt: 2, Channel: &gatewayjoin.Channel{ID: "current", Code: "482917"}, Origin: "https://relay.test", Machine: strings.Repeat("1", 32)})
	m = next.(relaySetupModel)
	if m.busy || m.stage != "verify" {
		t.Fatal("join-ready left the verification screen locked")
	}
	m = setupKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.verified {
		t.Fatal("matching-code confirmation blocked")
	}
	next, _ = m.Update(relayDiscoveryMsg{Attempt: 1})
	m = next.(relaySetupModel)
	if m.stage != "verify" || !m.verified {
		t.Fatal("old discovery reset the verified join")
	}
}
func TestSetupViewportScrollAndUntrustedErrorText(t *testing.T) {
	m := newRelaySetupModel()
	m.w, m.h = 60, 16
	m.stage = "review"
	m.name = "Studio gateway"
	m.origin = "https://relay.example.net"
	m.note = "network error\x1b[2J"
	view := m.View().Content
	if strings.Contains(view, "\x1b[2J") {
		t.Fatal("untrusted terminal control reached renderer")
	}
	if len(strings.Split(view, "\n")) > 16 {
		t.Fatal("setup overflowed terminal height")
	}
	before, _ := m.visibleLayout()
	m.scroll = 1000
	after, _ := m.visibleLayout()
	if before == after || !strings.Contains(gateANSI.ReplaceAllString(after, ""), "Create gateway") {
		t.Fatal("review confirmation is not reachable by scrolling")
	}
}

func TestApprovalPollNeverSelectsAnotherRequestAfterExpiry(t *testing.T) {
	m := relayApprovalModel{w: 80, h: 30, selection: 0, requests: []relayserver.JoinRequest{{ID: "first", Name: "First", Code: "123456"}}}
	next, _ := m.Update(relayApprovalsMsg{Requests: []relayserver.JoinRequest{{ID: "second", Name: "Second", Code: "654321"}}})
	m = next.(relayApprovalModel)
	if m.selection != -1 {
		t.Fatal("expired selection silently switched to another computer")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter approved an unselected replacement request")
	}
	if strings.Contains(m.View().Content, "654321") {
		t.Fatal("replacement code displayed before explicit selection")
	}
}
