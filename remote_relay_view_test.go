package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The relay screen says what is set up and what to do next, saves a typed token without showing
// it, and fits an 80-column popup.
func TestRelaySettingsScreen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	view := func(m relayModel) string { return gateANSI.ReplaceAllString(m.View().Content, "") }
	none := newRelayModel(relayView{})
	if v := view(none); !strings.Contains(v, "not deployed") || !strings.Contains(v, "Edit Cloudflare Workers") || !strings.Contains(v, "d deploy") {
		t.Errorf("no relay:\n%s", v)
	}
	m := newRelayModel(relayView{set: true, url: "https://ptln-relay.example.workers.dev", connected: "yes", outdated: true, phones: 1})
	v := view(m)
	for _, want := range []string{"ptln-relay.example.workers.dev", "older than this partyline", "1 phone paired", "not saved"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if len([]rune(line)) > 80 {
			t.Errorf("line wider than 80: %q", line)
		}
	}
	key := func(m relayModel, k tea.KeyPressMsg) relayModel { nm, _ := m.Update(k); return nm.(relayModel) }
	m = key(m, tea.KeyPressMsg{Code: 't', Text: "t"})
	for _, r := range "cf-secret-123" {
		m = key(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if strings.Contains(view(m), "cf-secret-123") {
		t.Error("the token is shown while typed")
	}
	m = key(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if savedRelayAPIToken() != "cf-secret-123" || !m.v.saved || strings.Contains(view(m), "cf-secret-123") {
		t.Errorf("token not saved (or shown): %q", savedRelayAPIToken())
	}
	if m = key(m, tea.KeyPressMsg{Code: 'x', Text: "x"}); savedRelayAPIToken() != "" || m.v.saved {
		t.Error("x did not forget the token")
	}
	if m = key(m, tea.KeyPressMsg{Code: 'u', Text: "u"}); m.next != "deploy" {
		t.Error("u did not deploy")
	}
}
