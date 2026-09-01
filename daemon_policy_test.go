package main

import (
	"os"
	"path/filepath"
	"testing"
)

// EVERY approval gate defaults to AUTO. Registration is the consent.
//
// These three gates are asked by the same daemon about the same registered directory, and they used
// to disagree: launches and consults defaulted to auto, RUNS required an explicit opt-in. So a
// project the owner had already registered would launch party agents unattended and then park every
// crank run behind a console prompt.
//
// That prompt is unreachable in the normal install. It renders only in the daemon's interactive
// console or the mux TUI banner, and the always-on service has neither — pending runs sat in an
// in-memory map that nothing displayed, no notification mentioned, and a restart dropped. Observed
// 2026-08-14: 14 runs sat at "Starting…" with nothing anywhere to approve them.
//
// A default is exactly the kind of thing that regresses silently, because the wrong value still
// compiles, still passes every other test, and only shows up as work that never runs.

func withRegistry(t *testing.T, json string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(daemonDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir(), "registry.json"), []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEveryGateDefaultsToAutoForARegisteredProject(t *testing.T) {
	// No "policy" key at all — the shape `ptln daemon add-project` actually writes.
	withRegistry(t, `{"projects":[{"label":"proj","path":"/tmp/proj","preset":"spec"}]}`)

	p := projectByLabel(loadDaemonRegistry(), "proj")
	if p == nil {
		t.Fatal("registered project not found — the rest of this test proves nothing")
	}

	if got := p.launchPolicy(); got != "auto" {
		t.Errorf("launchPolicy = %q, want auto", got)
	}
	if got := p.consultPolicy(); got != "auto" {
		t.Errorf("consultPolicy = %q, want auto", got)
	}
}

// "ask" still works — the escape hatch has to survive, or the default becomes a policy nobody can
// opt out of. Asserted per-gate so a change that flattens one of them to a constant is caught.
func TestAskStillOptsAProjectBackIn(t *testing.T) {
	withRegistry(t, `{"projects":[{"label":"proj","path":"/tmp/proj","policy":"ask","consults":"ask"}]}`)

	p := projectByLabel(loadDaemonRegistry(), "proj")
	if p == nil {
		t.Fatal("registered project not found")
	}
	if got := p.launchPolicy(); got != "ask" {
		t.Errorf("launchPolicy = %q, want ask", got)
	}
	if got := p.consultPolicy(); got != "ask" {
		t.Errorf("consultPolicy = %q, want ask", got)
	}
}
