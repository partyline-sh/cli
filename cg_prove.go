package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// PROVING A CRITERION BEFORE IT IS FILED.
//
// The specificity gate checks that a criterion EXISTS and carries a command. It cannot check that the
// command means anything, and that turned out to be the whole problem: four tasks in one evening were
// filed with acceptance commands that could not fail, each one authored confidently and none of them
// run before filing.
//
//	go test -run TestBoot ./...            exits 0 when NOTHING matches the filter
//	... | grep -qiE 'Test.*[Bb]oot'        matched the unrelated TestBootstrap* already in the tree
//	wc -l | grep -q '^0$'                  never matches: BSD wc pads its output with spaces
//	test -f llms_boot_report_test.go       a filename invented by the planner, not a deliverable
//
// Every one passed review by a human reading it. The only thing that separates a real acceptance
// check from a decorative one is RUNNING IT, so finalize runs it.
//
// The contract is simple and mechanical:
//   - an ACCEPTANCE command must FAIL on the base branch. If it passes, it cannot tell whether the
//     work happened — it would report success either way.
//   - a GUARD command must PASS on the base branch. A guard that is already red is not protecting
//     anything; it is a broken command that will fail the run for reasons unrelated to the task.
//
// This runs the planner's OWN commands, in the user's own repo, with the user present — the same
// commands crank will run later on the same machine. Nothing is escalated: a command that would be
// unsafe to prove here is a command that was going to run unattended in a worker anyway.
type criterionProof struct {
	Command string
	Want    string // "fail today" | "pass today"
	Got     int
	Err     error
}

func (p criterionProof) ok() bool {
	if p.Err != nil {
		return false
	}
	if p.Want == "fail today" {
		return p.Got != 0
	}
	return p.Got == 0
}

// proveCriteria runs every criterion that carries a command and reports the ones whose result
// contradicts their direction. dir is the repo to run in; a command is given its own process group
// and a hard timeout so a hung check cannot wedge planning.
func proveCriteria(dir string, crits []api.WorkItemCriterion, timeout time.Duration) []criterionProof {
	var out []criterionProof
	for _, c := range crits {
		cmd := strings.TrimSpace(c.Command)
		if cmd == "" {
			continue // prose-only criteria (adversarial / behavior review) have nothing to run
		}
		want := "pass today"
		if strings.TrimSpace(c.Direction) == "acceptance" {
			want = "fail today"
		}
		code, err := runProbe(dir, cmd, timeout)
		out = append(out, criterionProof{Command: cmd, Want: want, Got: code, Err: err})
	}
	return out
}

func runProbe(dir, command string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "bash", "-c", command)
	c.Dir = dir
	// Discard output: this is a verdict, not a build log. What matters is the exit code, and a
	// criterion that prints megabytes must not land in a tool result.
	c.Stdout, c.Stderr = nil, nil
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return -1, fmt.Errorf("timed out after %s", timeout)
	}
	if ee, isExit := err.(*exec.ExitError); isExit {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// proofReport renders the failures as something the planner can act on. Returns "" when every
// criterion behaved, which is the only case that files.
func proofReport(dir string, proofs []criterionProof) string {
	var bad []criterionProof
	for _, p := range proofs {
		if !p.ok() {
			bad = append(bad, p)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("NOT FILED — a criterion does not do what it claims. Each was RUN just now, in " + dir + ":\n\n")
	for _, p := range bad {
		b.WriteString("  " + p.Command + "\n")
		switch {
		case p.Err != nil:
			b.WriteString("    could not run it: " + p.Err.Error() + "\n")
		case p.Want == "fail today":
			b.WriteString("    marked ACCEPTANCE but it PASSES on the base branch (exit 0), before any work.\n" +
				"    So it cannot tell whether the work happened — it would report success either way.\n" +
				"    Either the work is already done, or this is a GUARD and the task still needs an\n" +
				"    acceptance check that fails today.\n")
		default:
			b.WriteString(fmt.Sprintf("    marked GUARD but it FAILS on the base branch (exit %d), before any work.\n", p.Got) +
				"    A guard that is already red protects nothing and will fail the run for reasons\n" +
				"    that have nothing to do with this task. Fix the command, or drop it.\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("If that directory is not the repository this plan is about, the verdicts above are\n" +
		"meaningless and the fix is the plan's project label, not the commands.\n\n" +
		"ASSERT THE OUTCOME, NOT THE SHAPE. Do not require a particular file, test name or symbol —\n" +
		"where the worker puts its code is its choice, and a task that fails because a file was named\n" +
		"differently has tested nothing about the result. Your draft is KEPT; fix the commands and call\n" +
		"planning_finalize again.\n\n" +
		"Traps that have already cost a run each:\n" +
		"  · `go test -run X` exits 0 when X matches NOTHING — it passes before the test exists.\n" +
		"  · a substring filter matches its neighbours (`Boot` matched the unrelated `TestBootstrap*`).\n" +
		"  · `wc -l | grep -q '^0$'` never matches on macOS — BSD wc pads with spaces.\n")
	return b.String()
}

// repoDirForProof is where the probes run — and getting this wrong is worse than not probing at all.
//
// IT IS NOT os.Getwd(). That was the first version and it was wrong within an hour of shipping: the
// MCP server inherits the SESSION's working directory, which is frequently not the repository the
// plan is about. A session rooted in one directory while its work targets another ran the guard
// `go build ./...` in a directory with no go.mod, got exit 1, and refused a perfectly good plan on
// the grounds that its guard was "already red". The refusal was correct; the directory was not.
//
// So: resolve the project the plan belongs to through THIS MACHINE's own daemon registry, which maps
// a project label to an absolute local path. That keeps the reference-not-command posture intact —
// the path is local knowledge, never something the control plane supplied — and it is the same
// mapping crank itself uses to decide where a run happens.
//
// Falls back to the working directory when there is no registry entry, because a plain CLI session
// inside a repo IS its own answer. Returns "" only when neither is usable, and the caller then skips
// probing rather than proving something in the wrong place.
// proofDirHow says how the directory was resolved, because "where did this run?" turned out to be
// the difference between a proof and a rumour.
type proofDirHow string

const (
	proofDirProject    proofDirHow = "project"    // the plan named a project and it is registered here
	proofDirCwd        proofDirHow = "cwd"        // no project named; a CLI session in its own repo
	proofDirUnresolved proofDirHow = "unresolved" // a project WAS named and this machine does not have it
)

func repoDirForProof(label string) string {
	dir, _ := resolveProofDir(label)
	return dir
}

// resolveProofDir is repoDirForProof with the provenance attached.
//
// THE FALLBACK IS NOT UNIFORM, and that asymmetry is the whole point:
//
//   - No label at all → the working directory is a fair guess. A plain CLI session inside a repo IS
//     its own answer, and that case has always worked.
//   - A label that does NOT resolve → resolve to nothing. Falling back here was silently catastrophic:
//     the probes ran in whatever repo the session happened to sit in, where an acceptance command
//     fails because the code is absent rather than because the work is undone — and failing is
//     exactly what the gate wants to see before the work. Two plans were certified that way, their
//     commands never once run against the code they targeted. Proving nothing and saying so is
//     strictly better than proving something somewhere else.
func resolveProofDir(label string) (string, proofDirHow) {
	if l := strings.TrimSpace(label); l != "" {
		for _, p := range loadDaemonRegistry().Projects {
			if !strings.EqualFold(strings.TrimSpace(p.Label), l) {
				continue
			}
			if dir := strings.TrimSpace(p.Path); dir != "" && isGitRepoDir(dir) {
				return dir, proofDirProject
			}
		}
		return "", proofDirUnresolved
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", proofDirCwd
	}
	return wd, proofDirCwd
}

// isGitRepoDir keeps a stale registry entry — a project whose directory was moved or deleted — from
// sending the probes somewhere that no longer exists.
func isGitRepoDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}
