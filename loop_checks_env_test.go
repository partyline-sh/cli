package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Checks that could not run (a blocked .envrc) pause the run with what to fix, instead of counting
// as "already failing" at the baseline or failing a round; resume runs the baseline again.
func TestChecksThatCannotRunPause(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blocked := loopCheckRound{Checks: []loopCheck{{Cmd: "go build ./...", Exit: 1, Tail: "\x1b[31mdirenv: error /w/.envrc is blocked. Run `direnv allow` to approve its content\x1b[0m"}}}
	if p := checksEnvProblem("/w", blocked); !strings.Contains(p, "direnv allow /w") {
		t.Fatalf("blocked .envrc not recognised: %q", p)
	}
	for _, c := range []loopCheck{{Cmd: "make test", Exit: 2, Tail: "FAIL"}, {Cmd: "go vet", Exit: 0}} {
		if p := checksEnvProblem("/w", loopCheckRound{Checks: []loopCheck{c}}); p != "" {
			t.Errorf("an ordinary result %+v read as an environment problem: %q", c, p)
		}
	}
	if p := checksEnvProblem("/w", loopCheckRound{Checks: []loopCheck{{Cmd: "pnpm test", Exit: 127, Tail: "zsh: command not found: pnpm"}}}); !strings.Contains(p, "command not found") {
		t.Errorf("exit 127 not recognised: %q", p)
	}

	// The baseline itself: a repository whose .envrc direnv refuses.
	repo, _ := loopRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".envrc"), []byte("export X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\necho 'direnv: error " + repo + "/.envrc is blocked. Run `direnv allow` to approve its content' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "direnv"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	r := testLoopRun(t, repo)
	r.Checks = []string{"true"}
	_ = r.set(loopBaseline, "", "approved", nil)
	loopBaselineRound(r)
	r, _ = loadLoopRun(r.ID)
	if r.State != loopPaused || r.PausedFrom != loopBaseline || !strings.Contains(r.Note, "direnv allow") {
		t.Fatalf("baseline with a blocked .envrc: %s from %q, %q", r.State, r.PausedFrom, r.Note)
	}
	var spawned []string
	old := spawnRound
	t.Cleanup(func() { spawnRound = old })
	spawnRound = func(id string, n int) error { spawned = append(spawned, id); return nil }
	if err := loopResumeRun(r.ID, "test"); err != nil || len(spawned) != 1 {
		t.Fatalf("resume: %v, spawned %v", err, spawned)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopBaseline {
		t.Fatalf("after resume: %s", r.State)
	}
}
