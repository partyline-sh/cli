package api

import (
	"os"
	"path/filepath"
	"testing"
)

// withHome points HOME at a scratch dir so these tests never read or write the operator's real
// ~/.partyline — the file under test is the one that says which partyline this machine belongs to.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PARTYLINE_API", "")
	return home
}

// The bug this exists to prevent: a machine signed in to its own instance still sent every
// un-prefixed command to partyline.sh, which serves no API. `ptln whoami` said "not logged in" on a
// box that was perfectly logged in, and the daemon (which had PARTYLINE_API set) and the
// interactive CLI (which did not) disagreed about which partyline this machine belonged to.
func TestBaseUsesTheRememberedInstance(t *testing.T) {
	withHome(t)

	if got := Base(); got != prodBase {
		t.Fatalf("with nothing recorded, Base() = %q, want the historical default", got)
	}

	if err := SaveInstance("https://192.168.1.170:8443"); err != nil {
		t.Fatal(err)
	}
	if got := Base(); got != "https://192.168.1.170:8443" {
		t.Fatalf("Base() = %q, want the remembered instance", got)
	}
	if Unconfigured() {
		t.Fatal("a machine with a recorded instance is configured")
	}
}

// An explicit env var is the caller being specific about one command, and must outrank what the
// machine remembers — that is how a dev points a single invocation at staging.
func TestExplicitEnvVarOutranksTheRememberedInstance(t *testing.T) {
	withHome(t)
	if err := SaveInstance("https://remembered.example"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PARTYLINE_API", "https://explicit.example")

	if got := Base(); got != "https://explicit.example" {
		t.Fatalf("Base() = %q, want the explicit env var to win", got)
	}
}

func TestForgetInstanceFallsBackToTheDefault(t *testing.T) {
	withHome(t)
	if err := SaveInstance("https://box.example"); err != nil {
		t.Fatal(err)
	}
	ForgetInstance()

	if got := LoadInstance(); got != "" {
		t.Fatalf("LoadInstance() = %q after forgetting", got)
	}
	if got := Base(); got != prodBase {
		t.Fatalf("Base() = %q after forgetting, want the default", got)
	}
	ForgetInstance() // idempotent: logging out twice must not error
}

// Trailing slashes and stray whitespace are normalised on the way in, so the recorded value can be
// compared against, and used to name a config dir, without every caller re-trimming it.
func TestSaveInstanceNormalises(t *testing.T) {
	withHome(t)
	if err := SaveInstance("  https://box.example:8443/  "); err != nil {
		t.Fatal(err)
	}
	if got := LoadInstance(); got != "https://box.example:8443" {
		t.Fatalf("LoadInstance() = %q, want it trimmed", got)
	}
}

func TestSaveInstanceIgnoresEmpty(t *testing.T) {
	home := withHome(t)
	if err := SaveInstance("   "); err != nil {
		t.Fatalf("saving nothing should be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".partyline", "instance")); !os.IsNotExist(err) {
		t.Fatal("an empty instance must not create the file")
	}
}

// The file lives at the ROOT of ~/.partyline on purpose: ConfigDir() is derived FROM the endpoint,
// so the file naming the endpoint cannot live inside it without a chicken-and-egg.
func TestInstanceFileIsOutsideTheEndpointConfigDir(t *testing.T) {
	withHome(t)
	if err := SaveInstance("https://box.example:8443"); err != nil {
		t.Fatal(err)
	}
	dir := ConfigDir()
	if filepath.Dir(instancePath()) == dir {
		t.Fatalf("instance file sits inside the per-endpoint config dir %q — it cannot be found before the endpoint is known", dir)
	}
	if _, err := os.Stat(instancePath()); err != nil {
		t.Fatalf("instance file not written: %v", err)
	}
}

// Credentials stay namespaced per endpoint. Recording a default must not collapse two instances'
// tokens onto one path.
func TestRememberedInstanceStillNamespacesCredentials(t *testing.T) {
	withHome(t)
	if err := SaveInstance("https://box.example:8443"); err != nil {
		t.Fatal(err)
	}
	scoped := tokenPath()

	if err := SaveInstance("https://other.example:9443"); err != nil {
		t.Fatal(err)
	}
	if tokenPath() == scoped {
		t.Fatal("two instances shared one token path — signing in to one would overwrite the other")
	}
}
