package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The opt-in marker is the whole safety story for self-update: absent = this daemon never modifies
// itself. Assert the default is OFF and that the toggle round-trips, including the off-when-already-
// off case (a missing file must not error).
func TestAutoUpdateOptIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if autoUpdateEnabled() {
		t.Fatal("auto-update defaults to ON — it must be opt-in")
	}
	if err := setAutoUpdateEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !autoUpdateEnabled() {
		t.Error("enable didn't take")
	}
	if err := setAutoUpdateEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if autoUpdateEnabled() {
		t.Error("disable didn't take")
	}
	if err := setAutoUpdateEnabled(false); err != nil {
		t.Errorf("disabling an already-off node errored: %v", err)
	}
}

// The marker lives under the daemon state dir (alongside provision.on) and never in the registry or
// device.json — capability toggles are separate from project bindings and identity.
func TestAutoUpdateMarkerLocation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := setAutoUpdateEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	want := filepath.Join(dir, ".partyline", "daemon", "autoupdate.on")
	if got := autoUpdateStatePath(); got != want {
		t.Errorf("marker at %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("marker file not written: %v", err)
	}
}

// runsInFlight is the idle guard — the thing standing between "swap the binary" and "swap the binary
// while a build is running". It must reflect the same registry the kill path uses.
func TestRunsInFlightTracksCrankChildren(t *testing.T) {
	runProcsMu.Lock()
	runProcs = map[string]int{}
	runProcsMu.Unlock()

	if n := runsInFlight(); n != 0 {
		t.Fatalf("fresh daemon reports %d runs in flight, want 0", n)
	}
	trackRun("run-a", 4242)
	if n := runsInFlight(); n != 1 {
		t.Errorf("after one spawn: %d, want 1", n)
	}
	trackRun("run-b", 4243)
	if n := runsInFlight(); n != 2 {
		t.Errorf("after two spawns: %d, want 2", n)
	}
	untrackRun("run-a")
	untrackRun("run-b")
	if n := runsInFlight(); n != 0 {
		t.Errorf("after both exited: %d, want 0 (an upgrade would be blocked forever)", n)
	}
}

// Both upgrade paths — interactive `ptln upgrade` and the daemon's silent one — must run the SAME
// command, or the two drift and auto-update starts installing something the manual path doesn't.
func TestUpgradeCommandIsInstallAppropriate(t *testing.T) {
	cmd := upgradeCommand()
	if cmd == nil || len(cmd.Args) == 0 {
		t.Fatal("upgradeCommand returned nothing to run")
	}
	// Whichever branch this machine takes, it must be one of the two known installers — never an
	// arbitrary or empty command.
	switch cmd.Args[0] {
	case "brew", "sh":
	default:
		t.Errorf("upgrade runs %q, want brew or sh (the two supported install paths)", cmd.Args[0])
	}
}
