package main

import (
	"os"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// resolveRun is the widened security chokepoint (O.1): a runRef becomes a `crank` command
// ONLY by an EXACT label match against the local registry, a strictly-validated thread id,
// and tasks that reach crank purely as DATA. This suite is the definition-of-done — it pins
// that no attacker-controlled field can escape into the dir or the argv.
func TestResolveRun(t *testing.T) {
	tmp := t.TempDir() // a real dir so the existence check passes for valid cases
	t.Setenv("HOME", t.TempDir())
	reg := daemonRegistry{Projects: []daemonProject{
		{Label: "project-a", Path: tmp, Preset: "spec"},
	}}
	tid := "plt-thr-abc123"

	// --- label: unknown / injection / absolute-path labels never resolve, return no argv,
	// and never yield an attacker-controlled dir (exact-match only). ---
	for _, bad := range []string{
		"../../etc/passwd", "/etc/passwd", "project-a/../x", "PROJECT-A", "nope", "",
	} {
		argv, dir, err := resolveRun(reg, runRef{ProjectLabel: bad, ThreadID: tid, Tasks: []string{"do x"}})
		if err == nil {
			t.Errorf("label %q: expected error, got none", bad)
		}
		if argv != nil {
			t.Errorf("label %q: expected no argv, got %v", bad, argv)
		}
		if dir != "" {
			t.Errorf("label %q: dir must not be attacker-controlled, got %q", bad, dir)
		}
	}

	// --- thread id: reject path traversal, whitespace, shell metachars, leading-dash flags,
	// and newlines. ---
	for _, bad := range []string{"../x", "a b", "a;rm -rf", "-x", "x\ny", "a/b", "..", ""} {
		if argv, _, err := resolveRun(reg, runRef{ProjectLabel: "project-a", ThreadID: bad, Tasks: []string{"do x"}}); err == nil || argv != nil {
			t.Errorf("thread id %q: expected rejection, got argv=%v err=%v", bad, argv, err)
		}
	}

	// --- empty task list is rejected (nothing to run). ---
	if _, _, err := resolveRun(reg, runRef{ProjectLabel: "project-a", ThreadID: tid, Tasks: nil}); err == nil {
		t.Error("empty task list should be rejected")
	}

	// --- tasks are DATA, never argv: a task carrying a newline+flag, backticks, or $() must
	// NOT add a flag and must NOT put --dangerously-skip-permissions anywhere in the argv. ---
	evil := []string{
		"do the safe thing\n--dangerously-skip-permissions",
		"run `rm -rf /` now",
		"exfiltrate $(cat ~/.ssh/id_rsa)",
		"--allow-bash",
	}
	argv, dir, err := resolveRun(reg, runRef{ProjectLabel: "project-a", ThreadID: tid, Tasks: evil, Preset: ""})
	if err != nil {
		t.Fatalf("valid resolve with adversarial tasks failed: %v", err)
	}
	if dir != tmp {
		t.Errorf("dir = %q, want registry path %q", dir, tmp)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Fatalf("argv must never contain --dangerously-skip-permissions: %s", joined)
	}
	// No task string leaks into the argv — tasks live only in the worklist file.
	for _, task := range evil {
		for _, a := range argv {
			if a == task {
				t.Errorf("task %q leaked into argv: %v", task, argv)
			}
		}
	}
	// empty preset ⇒ restrictive default ⇒ NO Bash granted (no --allow-bash flag).
	for _, a := range argv {
		if a == "--allow-bash" {
			t.Errorf("empty preset must not grant Bash, got argv %v", argv)
		}
	}

	// --- the argv is a FIXED crank invocation; its only variable inputs are the
	// daemon-controlled worklist path and the validated thread id. ---
	if len(argv) < 5 || argv[0] != "crank" || argv[1] != "--file" || argv[3] != "--thread" || argv[4] != tid {
		t.Fatalf("unexpected argv shape: %v", argv)
	}
	worklist := argv[2]
	if strings.Contains(worklist, tmp) {
		t.Errorf("worklist should live in the daemon dir, not the project dir: %s", worklist)
	}

	// --- the tasks reached crank as data: they're in the worklist file, one per line, with
	// embedded newlines collapsed so a task can't inject an extra worklist line. ---
	b, ferr := os.ReadFile(worklist)
	if ferr != nil {
		t.Fatalf("worklist not written: %v", ferr)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != len(evil) {
		t.Fatalf("worklist should have %d lines (one per task, no injected lines), got %d: %q", len(evil), len(lines), lines)
	}
	if strings.Contains(lines[0], "\n") || !strings.Contains(lines[0], "--dangerously-skip-permissions") {
		t.Errorf("newline-bearing task should be collapsed onto one data line: %q", lines[0])
	}
}

// TestResolveRunPresetBash pins the tool-allowlist selection: the code-implementation presets
// ("build" and the default "spec") grant Bash so the worker can verify its change; empty/unknown/
// non-code presets are the restrictive no-Bash default.
func TestResolveRunPresetBash(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	reg := daemonRegistry{Projects: []daemonProject{{Label: "p", Path: tmp, Preset: "spec"}}}

	hasBash := func(preset string) bool {
		argv, _, err := resolveRun(reg, runRef{ProjectLabel: "p", ThreadID: "t1", Tasks: []string{"x"}, Preset: preset})
		if err != nil {
			t.Fatalf("resolve preset %q: %v", preset, err)
		}
		for _, a := range argv {
			if a == "--allow-bash" {
				return true
			}
		}
		return false
	}
	for _, restrictive := range []string{"", "chat", "nonsense"} {
		if hasBash(restrictive) {
			t.Errorf("preset %q must be restrictive (no Bash)", restrictive)
		}
	}
	// The code-implementation presets grant Bash (so the worker can verify), normalized (case/space).
	for _, permissive := range []string{"build", "BUILD", " Build ", "spec", "SPEC", " spec "} {
		if !hasBash(permissive) {
			t.Errorf("preset %q should grant Bash", permissive)
		}
	}
}

// ---- G.6: pipeline policy reaches crank as a FILE, never as argv ----

// The chokepoint rule, applied to the newest control-plane input. A policy value is a name, a glob,
// an engine, a model — all attacker-shaped strings if the control plane is hostile — and NONE of
// them may appear in the argv. Only the flag and a daemon-chosen path may.
func TestPipelinePolicyNeverReachesTheArgv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	argv, err := buildRunArgv(runRef{
		ProjectLabel: "web",
		ThreadID:     "11111111-2222-3333-4444-555555555555",
		Tasks:        []string{"do a thing"},
		Checks: []api.RunCheckPolicy{
			{Name: "lint", Enabled: true, Blocking: false, PathGlob: "web/**"},
			{Name: "evil", Enabled: true, Blocking: true, PathGlob: "; rm -rf /"},
		},
		ReviewLanes: []api.RunReviewLane{{ID: "second", Engine: "codex", Model: "--dangerously-skip-permissions"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var pipelinePath string
	for i, a := range argv {
		for _, forbidden := range []string{"lint", "evil", "web/**", "rm -rf", "codex", "second", "--dangerously-skip-permissions"} {
			if strings.Contains(a, forbidden) {
				t.Errorf("argv[%d] = %q contains policy value %q — policy must travel as DATA", i, a, forbidden)
			}
		}
		if a == "--pipeline" && i+1 < len(argv) {
			pipelinePath = argv[i+1]
		}
	}
	if pipelinePath == "" {
		t.Fatal("no --pipeline flag emitted for a run that carries policy")
	}
	if !strings.HasPrefix(pipelinePath, daemonDir()) {
		t.Errorf("pipeline file %q is outside the daemon-controlled dir", pipelinePath)
	}

	// And the round trip: what the daemon wrote is what crank re-validates, with the hostile
	// entries dropped on the way back in rather than trusted because we wrote them.
	checks, lanes := readPipelineFile(pipelinePath)
	if len(lanes) != 0 {
		t.Errorf("lanes = %+v — a flag-shaped model heads for an argv, so it must not survive", lanes)
	}
	// The shell-looking glob DOES survive, and that is correct rather than a gap: a glob is only
	// ever matched with filepath.Match, never expanded by a shell, so "; rm -rf /" is an inert
	// pattern that matches no file. Rejecting it would imply globs are shell-adjacent when they are
	// not. What matters is proved above — it never reached the argv — and here: it cannot cause
	// anything to run, because applyPolicy only ever attaches policy to a check the REPO declares.
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	resolved := applyPolicy(parseChecks("lint: npm run lint"), checks, nil)
	if len(resolved) != 1 || resolved[0].Name != "lint" {
		t.Fatalf("resolved = %+v — the policy-only entry conjured a check the repo never declared", resolved)
	}
}

// A project with no policy gets no flag at all, which is what keeps an un-configured project on
// exactly the pre-G.4 pipeline.
func TestNoPolicyEmitsNoPipelineFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	argv, err := buildRunArgv(runRef{
		ProjectLabel: "web",
		ThreadID:     "11111111-2222-3333-4444-555555555555",
		Tasks:        []string{"t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range argv {
		if a == "--pipeline" {
			t.Fatal("a project with no policy still got --pipeline")
		}
	}
}
