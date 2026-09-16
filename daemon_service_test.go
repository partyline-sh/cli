package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The same guarantee api.TestProdPathsUnchanged makes for credentials, made for the always-on unit:
// production names are FROZEN. If this fails, an upgrade orphans every installed daemon — the old
// unit keeps running under the old label while ptln looks for a new one, so `daemon status` reports
// nothing installed and `uninstall` cannot reach it.
func TestProdServiceNamesUnchanged(t *testing.T) {
	isolateInstance(t)
	for _, v := range []string{"", "https://partyline.sh", "https://partyline.sh/"} {
		t.Setenv("PARTYLINE_API", v)
		if got := serviceLabel(); got != "sh.partyline.daemon" {
			t.Errorf("PARTYLINE_API=%q: launchd label moved: %s", v, got)
		}
		if got := systemdUnitName(); got != "partyline-daemon" {
			t.Errorf("PARTYLINE_API=%q: systemd unit moved: %s", v, got)
		}
		if got := envSuffix(); got != "" {
			t.Errorf("PARTYLINE_API=%q: production must have no suffix, got %q", v, got)
		}
	}
}

// Pointing at another control plane must produce a DIFFERENT unit. Before this, `ptln daemon
// install` on staging rewrote production's plist with a staging-pointed one: the node stayed up,
// the fleet still listed it, and it silently served the wrong environment.
func TestNonProdServiceIsIsolated(t *testing.T) {
	isolateInstance(t)
	cases := map[string]struct{ label, unit string }{
		"https://staging.partyline.sh": {"sh.partyline.daemon.staging", "partyline-daemon-staging"},
		"http://localhost:3111":        {"sh.partyline.daemon.localhost-3111", "partyline-daemon-localhost-3111"},
		"https://ptln.example.com":     {"sh.partyline.daemon.ptln-example-com", "partyline-daemon-ptln-example-com"},
	}
	for api, want := range cases {
		t.Setenv("PARTYLINE_API", api)
		if got := serviceLabel(); got != want.label {
			t.Errorf("%s: label = %s, want %s", api, got, want.label)
		}
		if got := systemdUnitName(); got != want.unit {
			t.Errorf("%s: unit = %s, want %s", api, got, want.unit)
		}
		if serviceLabel() == "sh.partyline.daemon" || systemdUnitName() == "partyline-daemon" {
			t.Errorf("%s: MUST NOT collide with the production unit", api)
		}
	}
}

// The label is also a filename (~/Library/LaunchAgents/<label>.plist) and a systemd unit name, so a
// hostile or merely awkward PARTYLINE_API must not escape the directory or emit an illegal name.
func TestServiceNamesAreSafe(t *testing.T) {
	isolateInstance(t)
	home, _ := os.UserHomeDir()
	agents := filepath.Join(home, "Library", "LaunchAgents")
	for _, v := range []string{
		"https://../../etc",
		"https://a/../../../b",
		"https://x%2F..%2F..",
		"http://host:99/weird path",
	} {
		t.Setenv("PARTYLINE_API", v)
		for _, name := range []string{serviceLabel(), systemdUnitName()} {
			if strings.ContainsAny(name, `/\:`+"\x00 ") || strings.Contains(name, "..") {
				t.Errorf("%s: unsafe unit name %q", v, name)
			}
		}
		if got := filepath.Clean(filepath.Join(agents, serviceLabel()+".plist")); filepath.Dir(got) != agents {
			t.Errorf("%s: plist escaped LaunchAgents: %s", v, got)
		}
	}
}
