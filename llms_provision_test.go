package main

import (
	"path/filepath"
	"testing"
)

// P3 of provisioned workers (docs/plans/provisioned-workers.md): daemon/crank engine sessions run
// inside ~/.partyline/-managed dirs and would otherwise pollute the `ptln llms` launcher. The
// path-prefix filter hides them — and, because it keys on the cwd PATH not the prompt title, it also
// closes the pre-existing leak where a skill-injected crank session defeats the isAgentSession title
// heuristic.
func TestFilterManagedSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	managed := filepath.Join(home, ".partyline", "daemon", "repos", "acme", "web")
	worktree := filepath.Join(home, ".partyline", "daemon", "repos", "acme", "web--crank-01-x")
	userDir := filepath.Join(home, "dev", "myapp")

	in := []aiSession{
		{Tool: "claude", ID: "1", Cwd: userDir},  // a real user session — KEEP
		{Tool: "claude", ID: "2", Cwd: managed},  // provisioned clone — HIDE
		{Tool: "claude", ID: "3", Cwd: worktree}, // crank worktree under the managed clone — HIDE
		{Tool: "claude", ID: "4", Cwd: ""},       // no recorded cwd — KEEP (can't attribute it)
		{Tool: "codex", ID: "5", Cwd: userDir},   // another user session — KEEP
	}
	out := filterManagedSessions(in)

	kept := map[string]bool{}
	for _, s := range out {
		kept[s.ID] = true
	}
	for _, id := range []string{"1", "4", "5"} {
		if !kept[id] {
			t.Errorf("session %s should have been KEPT", id)
		}
	}
	for _, id := range []string{"2", "3"} {
		if kept[id] {
			t.Errorf("session %s (under ~/.partyline/) should have been HIDDEN", id)
		}
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 sessions kept, got %d", len(out))
	}
}
