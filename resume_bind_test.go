package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/ptymux"
)

// Reopened sessions must honor the repo bind exactly like `ptln new` does — a resumed spec whose
// Dir sits in a bound repo gets the bound thread + MCP wiring at (re)launch. This was the founder's
// "has to work with existing sessions, after closing and re-opening" requirement: before this fix,
// only fresh `ptln new` inherited .partyline.json, so every reopen silently dropped context wiring.
func TestInheritRepoBindSpec(t *testing.T) {
	repo := t.TempDir()
	git := exec.Command("git", "-C", repo, "init", "-q")
	if out, err := git.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"t-bind-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "web", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// A threadless claude resume-spec in the bound repo (even a subdir) gets wired.
	got := inheritRepoBindSpec(ptymux.Spec{Label: "x", Key: "k", Argv: []string{"claude", "--resume", "abc"}, Dir: sub})
	if got.Thread != "t-bind-1" {
		t.Fatalf("thread not inherited: %+v", got)
	}
	joined := strings.Join(got.Argv, " ")
	if !strings.Contains(joined, "--mcp-config") {
		t.Fatalf("claude argv not wired with MCP config: %v", got.Argv)
	}
	if !strings.Contains(joined, "--resume abc") {
		t.Fatalf("the conversation resume must survive wiring: %v", got.Argv)
	}

	// A spec that already carries a thread is untouched.
	pre := ptymux.Spec{Argv: []string{"claude"}, Dir: sub, Thread: "explicit"}
	if out := inheritRepoBindSpec(pre); out.Thread != "explicit" {
		t.Fatalf("explicit thread must win: %+v", out)
	}

	// Outside any bound repo → STILL wired (zero-config MCP: the context server exists in
	// every engine session; the thread stays empty and resolves lazily in cg-mcp).
	unb := inheritRepoBindSpec(ptymux.Spec{Argv: []string{"claude", "--resume", "abc"}, Dir: t.TempDir()})
	if unb.Thread != "" {
		t.Fatalf("unbound dir must stay threadless: %+v", unb)
	}
	uj := strings.Join(unb.Argv, " ")
	if !strings.Contains(uj, "--mcp-config") || !strings.Contains(uj, "partyline-context-threads") {
		t.Fatalf("unbound claude must still get the context MCP wired: %v", unb.Argv)
	}
	if !strings.Contains(uj, "--resume abc") {
		t.Fatalf("the conversation resume must survive zero-config wiring: %v", unb.Argv)
	}
	if strings.Contains(uj, "--append-system-prompt") {
		t.Fatalf("no thread → no primer: %v", unb.Argv)
	}

	// A non-wireable engine still gets the thread (rides the env), argv untouched.
	g := inheritRepoBindSpec(ptymux.Spec{Argv: []string{"gemini"}, Dir: sub})
	if g.Thread != "t-bind-1" || len(g.Argv) != 1 {
		t.Fatalf("gemini should carry the thread via env with untouched argv: %+v", g)
	}
}
