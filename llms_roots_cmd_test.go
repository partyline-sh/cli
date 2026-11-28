package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// llms_roots_test.go already covers ADOPTION itself (idempotence, resume env, tagging, corrupt
// files). What was missing was a DOOR: the capability existed and the only way in was a modal that
// appears when you have ZERO sessions, so anyone with a session of their own could never reach it.
// These cover the door — the end-to-end path a person actually walks, and the doctor line that tells
// them the door is there.

// The exact reported case: a session started as `HOME=/home/acr claude --resume <id>` lives under
// /home/acr/.claude and is invisible to a manager running as someone else.
func TestASessionUnderAnotherHomeIsFoundOnceAdopted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	other := t.TempDir()
	dir := filepath.Join(other, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"hi"},"cwd":"/tmp/proj"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "aaaaaaaa-1111-2222-3333-444444444444.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	// Invisible first — otherwise the assertion below proves nothing.
	if n := len(collectSessions()); n != 0 {
		t.Fatalf("the foreign home was already visible (%d sessions); this test cannot show adoption did anything", n)
	}
	if err := addSessionRoot(other); err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, s := range collectSessions() {
		if s.root == other {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("after adopting, the session is still not listed (%d found) — the door leads nowhere", seen)
	}
}

// The doctor line is how someone whose session is missing LEARNS this exists. Silent when there is
// nothing to say, so an ordinary machine gets no noise.
func TestAdoptedRootsLineIsQuietUntilThereIsSomethingToSay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := adoptedRootsLine(); got != "" {
		t.Errorf("reported adopted roots when there are none: %q", got)
	}

	other := t.TempDir()
	if err := addSessionRoot(other); err != nil {
		t.Fatal(err)
	}
	got := adoptedRootsLine()
	if !strings.Contains(got, filepath.Base(other)) {
		t.Errorf("the adopted root is not named in the doctor line (%q) — nothing on screen says where it looks", got)
	}
	// The primary home is always searched and is not an adopted root; listing it would imply someone
	// configured it, and mask whether anything was actually added.
	if home, _ := os.UserHomeDir(); strings.Contains(got, home) {
		t.Errorf("the process's own home leaked into the adopted list: %q", got)
	}
}
