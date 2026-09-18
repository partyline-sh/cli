//go:build darwin

package main

import (
	"strings"
	"testing"
)

// Split out of daemon_service_test.go because the tray is a macOS menubar app: trayLabel and
// trayAppName live in tray_service.go / tray_bundle.go, both //go:build darwin. Without this tag
// the root package DOES NOT COMPILE on Linux — `go vet ./...` and `go test ./...` both fail with
// "undefined: trayLabel".
//
// That was true on main and nobody knew, because Go was never vetted or tested on Linux in CI:
// `go test ./...` ran only from release.yml on a tag. The gate added in ci.yml caught it on its
// first run. The daemon tests it was extracted from stay untagged on purpose — they assert the
// systemd unit names, which is the half that matters most on Linux.

// The tray is per control plane for the same reason the daemon is — and with the same hard rule
// that production names never move. A renamed bundle orphans the running app and the Login Items
// entry; a renamed label orphans the LaunchAgent.
func TestTrayNamesPerEnvironment(t *testing.T) {
	isolateInstance(t)
	for _, v := range []string{"", "https://partyline.sh"} {
		t.Setenv("PARTYLINE_API", v)
		if got := trayLabel(); got != "sh.partyline.tray" {
			t.Errorf("PARTYLINE_API=%q: tray label moved: %s", v, got)
		}
		if got := trayAppName(); got != "Partyline" {
			t.Errorf("PARTYLINE_API=%q: bundle renamed: %s", v, got)
		}
	}

	t.Setenv("PARTYLINE_API", "https://staging.partyline.sh")
	if got, want := trayLabel(), "sh.partyline.tray.staging"; got != want {
		t.Errorf("staging tray label = %s, want %s", got, want)
	}
	if got, want := trayAppName(), "Partyline-staging"; got != want {
		t.Errorf("staging bundle = %s, want %s", got, want)
	}
	// The bundle name becomes a `pgrep -f` pattern (tray_wake.go). Regex metacharacters there would
	// silently match nothing, and the reap would quietly stop working.
	if strings.ContainsAny(trayAppName(), "()[]{}.*+?|^$\\ ") {
		t.Errorf("bundle name %q contains a regex metacharacter — pgrep -f would misbehave", trayAppName())
	}
}
