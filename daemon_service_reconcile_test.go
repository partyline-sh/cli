package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The observed failure: an instance that changed address left its old always-on unit installed and
// running, retrying a host that had stopped listening. These cover the sweep that removes it — and,
// more importantly, the cases where it must NOT remove anything.

func TestUnitSuffixMatchesEnvSuffixForTheCurrentHost(t *testing.T) {
	t.Setenv("PARTYLINE_API", "https://192.168.1.170:8443")
	// The sweep reconstructs a unit name from a registry host string; envSuffix() derives it from
	// the live endpoint. If these ever disagree the sweep either misses the orphan or, worse,
	// removes the unit currently in use.
	if got, want := unitSuffixFor("192.168.1.170:8443"), envSuffix(); got != want {
		t.Fatalf("suffix derivation disagrees: sweep %q, installer %q", got, want)
	}
}

func TestServiceNamesMatchTheInstallersForEveryShape(t *testing.T) {
	for _, host := range []string{"192.168.1.170:8443", "partyline.example.com", "localhost:3111", "staging.partyline.sh"} {
		t.Setenv("PARTYLINE_API", "https://"+host)
		suffix := unitSuffixFor(host)
		if got, want := serviceLabelFor(suffix), serviceLabel(); got != want {
			t.Errorf("%s: launchd label mismatch: sweep %q, installer %q", host, got, want)
		}
		if got, want := systemdUnitNameFor(suffix), systemdUnitName(); got != want {
			t.Errorf("%s: systemd unit mismatch: sweep %q, installer %q", host, got, want)
		}
	}
}

// Without a confirmed identity there is no proof of ownership, so nothing may be removed.
func TestSweepDoesNothingWithoutAConfirmedIdentity(t *testing.T) {
	withTestHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")

	if got := reconcileStaleServices(); len(got) != 0 {
		t.Fatalf("an unidentified instance must not remove any unit; removed %v", got)
	}
}

// The unit for the address currently in use must survive its own sweep.
func TestSweepNeverRemovesTheCurrentUnit(t *testing.T) {
	home := withTestHome(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no service manager on this platform")
	}
	const id = "aaaaaaaa-1111-2222-3333-444444444444"
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")
	api.RememberInstance("https://ptln.example.com", id, "")

	current := writeFakeUnit(t, home, envSuffix())
	if got := reconcileStaleServices(); len(got) != 0 {
		t.Fatalf("swept the live unit: %v", got)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("the current unit must still exist: %v", err)
	}
}

// A second, genuinely different partyline on the same machine is a supported setup. Its unit
// belongs to another instance id and must be left alone.
func TestSweepLeavesAnotherInstancesUnitAlone(t *testing.T) {
	home := withTestHome(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no service manager on this platform")
	}
	// A different instance, at its own address.
	t.Setenv("PARTYLINE_API", "https://other.example.com")
	api.RememberInstance("https://other.example.com", "bbbbbbbb-0000-0000-0000-000000000000", "other")
	others := writeFakeUnit(t, home, envSuffix())

	// Ours, which has moved.
	const id = "cccccccc-0000-0000-0000-000000000000"
	t.Setenv("PARTYLINE_API", "https://old.example.com")
	api.RememberInstance("https://old.example.com", id, "ours")
	t.Setenv("PARTYLINE_API", "https://new.example.com")
	api.RememberInstance("https://new.example.com", id, "ours")

	reconcileStaleServices()

	if _, err := os.Stat(others); err != nil {
		t.Fatalf("another instance's unit was removed: %v", err)
	}
}

// The move itself: the unit installed for the previous address goes away.
func TestSweepRemovesThePreviousAddressUnit(t *testing.T) {
	home := withTestHome(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no service manager on this platform")
	}
	const id = "dddddddd-0000-0000-0000-000000000000"

	t.Setenv("PARTYLINE_API", "https://192.168.1.170:8443")
	api.RememberInstance("https://192.168.1.170:8443", id, "monolith")
	stale := writeFakeUnit(t, home, envSuffix())

	t.Setenv("PARTYLINE_API", "https://partyline.example.com")
	api.RememberInstance("https://partyline.example.com", id, "monolith")
	live := writeFakeUnit(t, home, envSuffix())

	removed := reconcileStaleServices()
	if len(removed) != 1 {
		t.Fatalf("expected exactly the old unit to be removed, got %v", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the previous address's unit is still on disk (%v)", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the current unit must survive: %v", err)
	}
	// Swept once: the address is gone from the index, so a second login does not re-report it.
	if again := reconcileStaleServices(); len(again) != 0 {
		t.Fatalf("the sweep repeated itself: %v", again)
	}
}

// A hand-edited registry must not turn into a path traversal or an arbitrary launchctl argument.
func TestSweepRefusesAnUnsafeSuffix(t *testing.T) {
	if removeServiceFor("../../etc/cron.d/x") {
		t.Fatal("a suffix with path separators must be refused")
	}
	if removeServiceFor("a b; rm -rf /") {
		t.Fatal("a suffix with shell metacharacters must be refused")
	}
}

// withTestHome isolates HOME so a test never touches the operator's real services or registry.
func withTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// writeFakeUnit creates a unit FILE only — no launchctl/systemctl registration — which is all the
// sweep needs to find and remove. Returns its path.
func writeFakeUnit(t *testing.T, home, suffix string) string {
	t.Helper()
	var p string
	switch runtime.GOOS {
	case "darwin":
		p = filepath.Join(home, "Library", "LaunchAgents", serviceLabelFor(suffix)+".plist")
	case "linux":
		p = filepath.Join(home, ".config", "systemd", "user", systemdUnitNameFor(suffix)+".service")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("fake unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
