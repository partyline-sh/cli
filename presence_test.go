package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// Presence is decoration on a status bar, so every failure mode has to degrade to SILENCE. A bar
// that claims a teammate is present when they are not is worse than a bar that says nothing.
func TestPresenceSaysNothingRatherThanSomethingWrong(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := readPresence("acr"); got != nil {
		t.Errorf("no file at all should read as nothing, got %v", got)
	}

	if err := os.MkdirAll(presenceDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(presencePath("acr"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readPresence("acr"); got != nil {
		t.Errorf("an unreadable file should read as nothing, got %v", got)
	}

	// A file the watcher stopped updating — it crashed, or the bus went away. Its contents are
	// no longer evidence about anything.
	stale := presenceFile{
		Peers: map[string]time.Time{"matt:laptop": time.Now()},
		At:    time.Now().Add(-10 * time.Minute),
	}
	b, _ := json.Marshal(stale)
	if err := os.WriteFile(presencePath("acr"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readPresence("acr"); got != nil {
		t.Errorf("a file nobody is maintaining should read as nothing, got %v", got)
	}

	// A fresh file whose individual entries have aged out.
	mixed := presenceFile{
		Peers: map[string]time.Time{
			"matt:laptop":   time.Now(),
			"jo:oldmachine": time.Now().Add(-5 * time.Minute),
		},
		At: time.Now(),
	}
	b, _ = json.Marshal(mixed)
	if err := os.WriteFile(presencePath("acr"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := readPresence("acr"), []string{"matt:laptop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("readPresence = %v, want %v (an entry past its TTL must drop out)", got, want)
	}
}

// The roster the server sends includes this machine. Counting it would tell someone working
// alone that one other person is here.
func TestATrackerNeverCountsItself(t *testing.T) {
	tr := newPresenceTracker()
	tr.self("darcy:laptop-ab12")
	tr.saw("darcy:laptop-ab12")
	tr.saw("matt:mbp-99ff")
	if got := tr.live(); len(got) != 1 || got["matt:mbp-99ff"].IsZero() {
		t.Fatalf("live = %v; want only the teammate", got)
	}
}

// A departure must take effect at once. Waiting for the TTL would show someone as present for a
// minute and a half after they closed their laptop.
func TestADepartureDropsAPeerImmediately(t *testing.T) {
	tr := newPresenceTracker()
	tr.saw("matt:mbp")
	tr.left("matt:mbp")
	if got := tr.live(); len(got) != 0 {
		t.Fatalf("live = %v; want empty after a departure", got)
	}
}

// One person with a laptop and a server is one person, not two.
func TestPeopleAreCountedNotConnections(t *testing.T) {
	got := peopleOf([]string{"darcy:laptop-1", "darcy:monolith-2", "matt:mbp-3"})
	if want := []string{"darcy", "matt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("peopleOf = %v, want %v", got, want)
	}
}
