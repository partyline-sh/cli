package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoWithHook(t *testing.T, event, script string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, hooksDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if event != "" {
		if err := os.WriteFile(filepath.Join(repo, hooksDir, event), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// The default is nothing. A repo with no hooks directory must cost nothing and report nothing —
// silence here is correct, and it is the state almost every repo is in.
func TestNoHookIsNotAFailure(t *testing.T) {
	r := runHook(t.TempDir(), HookPostMerge, nil)
	if r.Ran || r.Failed {
		t.Errorf("an absent hook reported ran=%v failed=%v; both must be false", r.Ran, r.Failed)
	}
	if r.note() != "" {
		t.Errorf("an absent hook wrote %q to the log; it must be silent", r.note())
	}
}

// The facts of the event reach the script, prefixed so they cannot collide with the environment the
// customer's own tooling expects.
func TestAHookReceivesTheEventsFacts(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen")
	repo := repoWithHook(t, string(HookPostMerge), "echo \"$PARTYLINE_BRANCH $PARTYLINE_PR_URL\" > "+out+"\n")

	r := runHook(repo, HookPostMerge, map[string]string{"BRANCH": "crank-x-01", "PR_URL": "https://example/pr/1"})
	if !r.Ran || r.Failed {
		t.Fatalf("hook ran=%v failed=%v, want ran and not failed: %s", r.Ran, r.Failed, r.Out)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "crank-x-01 https://example/pr/1" {
		t.Errorf("the hook saw %q", got)
	}
}

// A FAILING HOOK MUST NOT FAIL THE RUN. The run already did what partyline promised; the hook is the
// customer's opinion about what happens next. But it must be LOUD — a deploy script that exited 1
// and said nothing is the worst possible outcome here.
func TestAFailingHookIsReportedAndNotFatal(t *testing.T) {
	repo := repoWithHook(t, string(HookRunAccepted), "echo 'deploy blew up' >&2; exit 3\n")
	r := runHook(repo, HookRunAccepted, nil)
	if !r.Ran || !r.Failed {
		t.Fatalf("ran=%v failed=%v, want both true", r.Ran, r.Failed)
	}
	n := r.note()
	if !strings.Contains(n, "non-zero") || !strings.Contains(n, "the run is unaffected") {
		t.Errorf("the note does not say what happened and what it means:\n%s", n)
	}
	if !strings.Contains(n, "deploy blew up") {
		t.Errorf("the hook's own output is missing, so nobody can tell why it failed:\n%s", n)
	}
}

// A task must not be able to install or edit the hook that runs on its own work — the same rule as
// .partyline/verify, and for the same reason. Hooks are read from the BASE repo.
func TestHooksAreReadFromTheBaseRepoNotTheWorktree(t *testing.T) {
	base := t.TempDir()
	worktree := repoWithHook(t, string(HookPreMerge), "exit 0\n") // the agent "wrote" one here
	if hookPath(base, HookPreMerge) != "" {
		t.Error("a hook resolved from a repo that has none")
	}
	if hookPath(worktree, HookPreMerge) == "" {
		t.Fatal("precondition: the worktree copy should exist for this test to mean anything")
	}
}

// The likeliest failure this feature produces is a MISNAMED hook: someone writes `post-merge`, it
// never fires, and nothing says why. Silence would read as "hooks are broken".
func TestAMisnamedHookIsNamed(t *testing.T) {
	repo := repoWithHook(t, "post-merge", "exit 0\n") // a plausible typo for post.merge
	stray := strayHooks(repo)
	if len(stray) != 1 || stray[0] != "post-merge" {
		t.Fatalf("strayHooks = %v, want the misnamed file reported", stray)
	}
	for _, ev := range hookEvents() {
		if strayHooks(repoWithHook(t, string(ev), "exit 0\n")) != nil {
			t.Errorf("a correctly-named hook %q was reported as stray", ev)
		}
	}
}

// The vocabulary mirrors the outbound webhook kinds on purpose: a team already subscribed to an
// event should not have to learn a second set of words to run a script on it instead.
func TestEveryEventIsRunnableAndNamed(t *testing.T) {
	seen := map[HookEvent]bool{}
	for _, ev := range hookEvents() {
		if seen[ev] {
			t.Errorf("duplicate event %q", ev)
		}
		seen[ev] = true
		if !strings.Contains(string(ev), ".") {
			t.Errorf("event %q is not dotted like the webhook kinds it mirrors", ev)
		}
	}
	if !seen[HookRunAccepted] {
		t.Error("run.accepted is missing — it is the event the whole feature exists for")
	}
}
