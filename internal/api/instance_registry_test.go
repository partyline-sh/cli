package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The regression these tests exist for: an instance that changes address used to orphan every
// machine enrolled with it. Credentials lived under envs/<host>/, so a new hostname meant a new,
// empty directory — no token, no device enrolment, and daemons retrying an endpoint that had
// stopped listening. Each test below names the property that has to hold instead.

func TestConfigDirFallsBackToHostWhenNothingIsKnown(t *testing.T) {
	withHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")

	got := ConfigDir()
	want := filepath.Join(os.Getenv("HOME"), ".partyline", "envs", "ptln.example.com")
	if got != want {
		t.Fatalf("unprobed host should keep the historical directory\n got %s\nwant %s", got, want)
	}
}

// The whole point. Same instance, new address, SAME directory — so the token and the device
// enrolment written under the old hostname are still the ones found.
func TestMovedInstanceKeepsItsConfigDirectory(t *testing.T) {
	withHome(t)
	const id = "11111111-2222-3333-4444-555555555555"

	t.Setenv("PARTYLINE_API", "https://192.168.1.170:8443")
	RememberInstance("https://192.168.1.170:8443", id, "monolith")
	before := ConfigDir()

	// Write a credential the way login would, then move the instance.
	if err := os.MkdirAll(before, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(before, "token"), []byte("plt_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PARTYLINE_API", "https://partyline.example.com")
	RememberInstance("https://partyline.example.com", id, "monolith")
	after := ConfigDir()

	if after != before {
		t.Fatalf("a moved instance must resolve to the directory holding its credentials\n before %s\n after  %s", before, after)
	}
	if got := LoadToken(); got != "plt_secret" {
		t.Fatalf("the token written before the move must still load after it; got %q", got)
	}
}

// The inverse, and the reason identity is not just "whatever answers at this URL": a fresh
// database served at an address a DIFFERENT instance used must not inherit its credentials.
func TestRebuiltInstanceAtTheSameAddressGetsItsOwnDirectory(t *testing.T) {
	withHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")

	oldDir := RememberInstance("https://ptln.example.com", "aaaaaaaa-0000-0000-0000-000000000000", "old")
	newDir := RememberInstance("https://ptln.example.com", "bbbbbbbb-0000-0000-0000-000000000000", "rebuilt")

	if oldDir == newDir {
		t.Fatalf("two instances at one address must not share a credential directory (both %s)", newDir)
	}
	if got := ConfigDir(); got != filepath.Join(os.Getenv("HOME"), ".partyline", "envs", newDir) {
		t.Fatalf("the host should now resolve to the instance currently serving it; got %s", got)
	}
}

// An existing install has files under envs/<host>/ and no registry. The first probe must adopt
// that exact directory — moving or renaming it would strand a running machine.
func TestFirstSightingAdoptsTheExistingHostDirectory(t *testing.T) {
	withHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")
	existing := ConfigDir()

	dir := RememberInstance("https://ptln.example.com", "cccccccc-0000-0000-0000-000000000000", "")
	if got := filepath.Join(os.Getenv("HOME"), ".partyline", "envs", dir); got != existing {
		t.Fatalf("first sighting must adopt the directory already in use\n got %s\nwant %s", got, existing)
	}
}

// A server too old to answer, or one whose database is down, must change nothing.
func TestUnknownIdentityLeavesTheRegistryAlone(t *testing.T) {
	withHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")
	const id = "dddddddd-0000-0000-0000-000000000000"

	RememberInstance("https://ptln.example.com", id, "real")
	want := ConfigDir()

	if got := RememberInstance("https://ptln.example.com", "", ""); got != "" {
		t.Fatalf("an empty id is not an instance; got dir %q", got)
	}
	if got := ConfigDir(); got != want {
		t.Fatalf("a failed probe must not repoint config\n got %s\nwant %s", got, want)
	}
}

// A hand-edited or corrupted registry must not be able to walk out of the config tree.
func TestRegistryCannotEscapeTheConfigTree(t *testing.T) {
	home := withHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")

	reg := instanceRegistry{
		Instances: map[string]InstanceRecord{"x": {Dir: "../../../../etc"}},
		Hosts:     map[string]string{"ptln.example.com": "x"},
	}
	b, _ := json.MarshalIndent(reg, "", "  ")
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".partyline", "instances.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	got := ConfigDir()
	if !filepath.HasPrefix(got, filepath.Join(home, ".partyline", "envs")) {
		t.Fatalf("config dir escaped the envs tree: %s", got)
	}
}

func TestCorruptRegistryFallsBackToHostname(t *testing.T) {
	home := withHome(t)
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".partyline", "instances.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, ".partyline", "envs", "ptln.example.com")
	if got := ConfigDir(); got != want {
		t.Fatalf("an unreadable registry must degrade to the historical layout\n got %s\nwant %s", got, want)
	}
}

// Production's layout is load-bearing for machines that logged in when there was a hosted service:
// its files are at the root, not under envs/.
func TestProductionLayoutIsUntouched(t *testing.T) {
	home := withHome(t)
	t.Setenv("PARTYLINE_API", prodBase)
	if got := ConfigDir(); got != filepath.Join(home, ".partyline") {
		t.Fatalf("production must keep ~/.partyline; got %s", got)
	}
}

func TestProbeReadsIdentityAndAdoptReportsAMove(t *testing.T) {
	withHome(t)
	const id = "eeeeeeee-0000-0000-0000-000000000000"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/partyline" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(Identity{InstanceID: id, Name: "monolith", BaseURL: "https://elsewhere.example.com"})
	}))
	defer srv.Close()

	// Enrolled at one address...
	RememberInstance("https://old.example.com", id, "monolith")

	got, moved, err := AdoptInstance(srv.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got.InstanceID != id {
		t.Fatalf("instance id: got %q want %q", got.InstanceID, id)
	}
	if !moved {
		t.Fatal("finding a known instance at a new address must report a move")
	}

	// ...and the second sighting at the same address is not a move.
	if _, moved, _ := AdoptInstance(srv.URL); moved {
		t.Fatal("re-probing the same address must not report a move")
	}
}

func TestProbeTreatsAnOldInstanceAsNoAnswer(t *testing.T) {
	withHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // an instance predating the endpoint
	}))
	defer srv.Close()

	if _, _, err := AdoptInstance(srv.URL); err == nil {
		t.Fatal("a 404 should surface as an error the caller can ignore, not a silent success")
	}
	if len(KnownInstances()) != 0 {
		t.Fatal("a server that cannot vouch for itself must not be recorded")
	}
}
