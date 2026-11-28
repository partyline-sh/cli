package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"partyline.sh/partyline/internal/relayserver"
)

func TestServerOnlyGatewayPreservesClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client, err := newRelayConfig("https://existing.workers.dev", strings.Repeat("1", 32))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("2", 64)
	if err := saveRelay(client, token); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(relayDir(), "relay.json"))
	if err != nil {
		t.Fatal(err)
	}
	host, err := initializeRelayHost(relayHostDir(), relayHostConfig{Origin: "https://private.test", RelayOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(relayDir(), "relay.json"))
	if string(after) != string(original) {
		t.Fatal("changed existing client configuration")
	}
	primary, primaryToken, err := loadRelay()
	if err != nil || primary.URL != client.URL || primaryToken != token {
		t.Fatal("changed existing client credentials", err)
	}
	owner, ownerToken, err := loadRelayOwner()
	if err != nil || owner.MachineID != host.Machine || owner.URL != host.Origin || relayserver.Digest(ownerToken) != host.Hash {
		t.Fatal("invalid separate owner credential", err)
	}
	info, err := os.Stat(filepath.Join(relayHostDir(), "owner-token"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("owner token must be private", err)
	}
}

func TestServerOnlyGatewayCannotPairUsingAnotherClient(t *testing.T) {
	m := newRelayModel(relayView{set: true, hosting: "self-hosted", serverOnly: true, localHost: true, owner: true})
	view := gateANSI.ReplaceAllString(m.fullView().Content, "")
	if strings.Contains(view, "p pair a phone") {
		t.Fatal("server-only view offers pairing through another relay")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if next.(relayModel).next != "" {
		t.Fatal("server-only view allows pairing")
	}
	primary := newRelayModel(relayView{set: true, otherGateway: true})
	next, _ = primary.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if next.(relayModel).next != "gateway" {
		t.Fatal("separate gateway is not reachable from settings")
	}
}

func TestRelaySettingsOffersGatewaySetup(t *testing.T) {
	m := newRelayModel(relayView{set: true, url: "https://existing.workers.dev"})
	if !strings.Contains(gateANSI.ReplaceAllString(m.fullView().Content, ""), "s setup gateway") {
		t.Fatal("setup action missing from relay settings")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	if next.(relayModel).next != "setup" {
		t.Fatal("setup action did not open wizard")
	}
}

func TestRelaySettingsWrappedFooterActionsAreClickable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newRelayModel(relayView{set: true, hosting: "self-hosted", owner: true, localHost: true, otherGateway: true})
	m.scroll = 100000
	for _, tc := range []struct{ label, next string }{{"p pair a phone", "pair"}, {"g private gateway", "gateway"}, {"s setup gateway", "setup"}} {
		t.Run(tc.next, func(t *testing.T) {
			lines := strings.Split(gateANSI.ReplaceAllString(m.View().Content, ""), "\n")
			for y, line := range lines {
				if x := strings.Index(line, tc.label); x >= 0 {
					next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					if next.(relayModel).next != tc.next {
						t.Fatalf("clicked %q but opened %q", tc.label, next.(relayModel).next)
					}
					return
				}
			}
			t.Fatalf("footer action %q is not visible", tc.label)
		})
	}
}
