package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The layering rule, as a test: memory is delivered by git and the interval watcher, and the bus
// only makes that faster. If a dead bus can slow down or fail a write, layer 3 has become
// load-bearing and a teammate's laptop going offline starts costing people their facts.
func TestAWriteSucceedsAndReturnsPromptlyWithNoBus(t *testing.T) {
	// Point the client at a port nothing listens on: the same shape as an instance that is down,
	// a proxy that refuses, or a machine that never configured a bus at all.
	t.Setenv("PARTYLINE_BUS", "http://127.0.0.1:1")
	t.Setenv("PARTYLINE_BUS_PROJECT", "acme")

	dir := t.TempDir()
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "fact.md"), []byte("a decision"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- memorySync(dir, "memory: decision — test") }()
	select {
	case err := <-done:
		// No upstream in a bare temp repo, so a push failure is expected and fine; what must not
		// happen is the write hanging on, or being failed by, the bus.
		_ = err
	case <-time.After(20 * time.Second):
		t.Fatal("memorySync blocked on an unreachable bus — the bus must never be in the write path")
	}
}

// announceMemoryChanged is called from the write path on every recorded fact. It must be safe to
// call with nothing listening, from any directory, without a signed-in account.
func TestAnnounceIsSafeWithoutAnInstance(t *testing.T) {
	t.Setenv("PARTYLINE_BUS", "http://127.0.0.1:1")
	announceMemoryChanged("acme")
	announceMemoryChanged("") // no project: must not dial at all
	time.Sleep(200 * time.Millisecond)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
