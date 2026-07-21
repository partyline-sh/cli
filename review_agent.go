// Review agent (R2) — the daemon side of the on-demand, ADVISORY review a human requests on a
// finished run from the board's Review column (preset "review"). It runs on the ORIGINAL machine
// (the one that built the run, so the branches are local), diffs each of the run's task branches, and
// has a fresh independent engine grade the work against its task. The result — a quality GRADE (A–F),
// a summary, and an issues list — is posted back to run_reviews and shown on the run's card + detail.
//
// It reuses the T2b reviewer machinery (verify.go): the same claude-fresh, tool-less, no-thread engine
// invocation (verifier ≠ producer) and the same bounded-diff approach — but emits a STRUCTURED graded
// verdict (like describe's fenced JSON) instead of PASS/FAIL, and is ADVISORY: it never gates Accept.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

const reviewTimeout = 4 * time.Minute

// runReviewJob is the daemon handler for a preset "review" run. It mirrors runDescribeJob: resolve the
// label→path (the same chokepoint — nothing server-supplied becomes a path), stream progress to
// run_logs, do ONE local engine turn, record the structured result, set a terminal status. Everything
// it needs about the TARGET run arrives as DATA via ReviewTarget (reference-not-command).
func runReviewJob(d daemonDevice, ev api.RunEvent) error {
	fail := func(stage string, e error) error {
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", stage+": "+e.Error())
		return fmt.Errorf("%s: %w", stage, e)
	}
	reg := loadDaemonRegistry()
	_, dir, err := resolveRun(reg, runRefFromEvent(ev)) // validate label→path; argv is unused for review
	if err != nil {
		return fail("resolve", err)
	}
	if ev.ReviewOf == "" {
		return fail("review", fmt.Errorf("no target run to review"))
	}

	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "running", "")
	logger := newRunLoggerWith(d.Base, d.Token, ev.RunID)
	defer logger.close()
	sink := logger.sink(0)
	logln := func(s string) { // nil-safe: sink() is nil when the logger can't post
		if sink != nil {
			sink(s)
		}
	}

	targetID, tasks, err := api.ReviewTarget(d.Base, d.Token, ev.RunID)
	if err != nil {
		return fail("review", err)
	}
	logln(fmt.Sprintf("collecting changes across %d task branch(es)…", len(tasks)))

	// Assemble each reviewable task's branch diff (only tasks that produced a branch), bounded overall
	// so a huge run can't blow the reviewer's context/cost. The branch always persists even after the
	// worktree is gone, so we diff the branch ref directly from the base repo — no worktree needed.
	var b strings.Builder
	reviewed := 0
	for _, t := range tasks {
		if strings.TrimSpace(t.Branch) == "" {
			continue
		}
		diff := branchDiff(dir, t.Branch)
		if strings.TrimSpace(diff) == "" {
			continue
		}
		logln(fmt.Sprintf("⎇ %s — %d change lines", t.Branch, strings.Count(diff, "\n")))
		fmt.Fprintf(&b, "\n=== TASK %d: %s ===\n", t.Idx+1, t.Task)
		b.WriteString(diff)
		b.WriteString("\n")
		reviewed++
	}
	if reviewed == 0 {
		return fail("review", fmt.Errorf("this run produced no reviewable changes"))
	}
	logln(fmt.Sprintf("sending %d change(s) to the reviewer — grading against each task…", reviewed))

	// Engine (Epic #73): the run's server-resolved review engine when valid, else this machine's
	// per-project engine, else claude — same pecking order as resolveLaunch. The notice (an
	// override or an ignored unknown value) goes to the run log so the choice is auditable.
	engineName, note := resolveRunEngine(reg, ev.ProjectLabel, ev.Engine)
	if note != "" {
		logln(note)
	}
	grade, summary, issues, err := runGradedReview(dir, engineName, headString(b.String(), maxDiffBytes), ev.Model, reviewTimeout, logln)
	if err != nil {
		return fail("review", err)
	}
	logln(fmt.Sprintf("done — grade %s, %d issue(s)", grade, len(issues)))
	if err := api.RecordReview(d.Base, d.Token, targetID, grade, summary, engineName, issues); err != nil {
		return fail("record", err)
	}
	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "done", "graded "+grade)
	return nil
}

// branchDiff returns a task branch's change vs the commit it forked from. crank commits each task once
// on a branch off fresh origin/<default>, so `<branch>^..<branch>` is that task's diff — the same
// single-commit view the T2b reviewer judged during the build. Works from the base repo without a
// worktree (the branch ref persists after the run). Falls back to showing the branch tip if it has no
// parent (rare).
func branchDiff(repo, branch string) string {
	out, err := exec.Command("git", "-C", repo, "diff", branch+"^", branch).Output()
	if err != nil {
		out, _ = exec.Command("git", "-C", repo, "show", "--format=", branch).Output()
	}
	return string(out)
}

// reviewGradePrompt frames the same adversarial, independent review as T2b but asks for a STRUCTURED
// graded verdict as a single fenced JSON block (parsed by jsonBlockRe, like describe). Advisory: the
// grade informs the human, it doesn't gate anything.
func reviewGradePrompt(tasksAndDiffs string) string {
	var b strings.Builder
	b.WriteString("You are an INDEPENDENT, adversarial code reviewer. You did NOT write this code. For each task below, judge how correctly and completely its diff satisfies the task, and grade the run as a whole.\n\n")
	b.WriteString("Assess: does the change do what the task asked? Are there bugs, missing requirements, unhandled cases, or sloppy/unsafe code? Be skeptical; do not give the benefit of the doubt.\n\n")
	b.WriteString("TASKS AND THEIR DIFFS:\n")
	b.WriteString(tasksAndDiffs)
	b.WriteString("\n\nReply with ONE fenced JSON block and nothing else, in exactly this shape:\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"grade\": \"A|B|C|D|F\",  // A excellent · B good · C acceptable with concerns · D weak · F broken or wrong\n")
	b.WriteString("  \"summary\": \"2-4 sentences: what was built and your overall judgment\",\n")
	b.WriteString("  \"issues\": [ { \"severity\": \"high|med|low\", \"text\": \"one concrete problem\" } ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n")
	b.WriteString("Grade honestly against the task. If it's fully correct with no material problems, that's an A and issues may be empty.\n")
	return b.String()
}

// gradedReview is the parsed shape of the reviewer's JSON.
type gradedReview struct {
	Grade   string            `json:"grade"`
	Summary string            `json:"summary"`
	Issues  []api.ReviewIssue `json:"issues"`
}

// runGradedReview runs the fresh reviewer on the given engine (verifier ≠ producer, no thread
// wiring — same independence as T2b) and parses its graded JSON. Tool posture is the strongest the
// engine can enforce: tool-less on claude; engines with no tool-less mode run read-only (the one
// sanctioned downgrade, logged by reviewerOneShot); antigravity can enforce neither and errors, so
// the review run fails closed. Errors on timeout, engine failure, or an unparseable/invalid reply
// so the review run fails loudly (retryable) rather than recording a misleading grade.
func runGradedReview(dir, engineName, tasksAndDiffs, model string, timeout time.Duration, logf func(string)) (string, string, []api.ReviewIssue, error) {
	spec, ok := engineSpecFor(engineName)
	if !ok {
		return "", "", nil, fmt.Errorf("unknown engine %q", engineName)
	}
	if !modelRe.MatchString(model) { // model selection: the project's review-phase model (validated)
		model = ""
	}
	argv, stdinPrompt, err := reviewerOneShot(spec, reviewGradePrompt(tasksAndDiffs), model, logf)
	if err != nil {
		return "", "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runOneShot(ctx, dir, argv, stdinPrompt, spec.OneShotEnv(eng.ToolsNone)...)
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", nil, fmt.Errorf("reviewer timed out (>%s)", timeout)
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("couldn't run the reviewer engine (%s)", spec.Name)
	}
	return parseGradedReview(oneShotText(spec, out))
}

// parseGradedReview extracts the graded verdict and validates the grade. It prefers describe's fenced
// JSON block; failing that (the model forgot the fence) it scans for the first balanced {…} object that
// parses into a valid graded verdict — so stray braces in surrounding prose don't derail it. Returns an
// error if nothing parseable is found, so the review run fails closed rather than recording a bad grade.
func parseGradedReview(reply string) (string, string, []api.ReviewIssue, error) {
	if m := jsonBlockRe.FindStringSubmatch(reply); len(m) > 1 {
		if g, s, iss, ok := decodeGraded(m[1]); ok {
			return g, s, iss, nil
		}
	}
	for _, cand := range balancedObjects(reply) {
		if g, s, iss, ok := decodeGraded(cand); ok {
			return g, s, iss, nil
		}
	}
	return "", "", nil, fmt.Errorf("reviewer returned no parseable graded verdict")
}

// decodeGraded unmarshals one candidate object and validates the grade (A–F, case-insensitive). ok is
// false if it isn't valid JSON or the grade is missing/out of range.
func decodeGraded(raw string) (string, string, []api.ReviewIssue, bool) {
	var gr gradedReview
	if json.Unmarshal([]byte(raw), &gr) != nil {
		return "", "", nil, false
	}
	grade := strings.ToUpper(strings.TrimSpace(gr.Grade))
	if len(grade) != 1 || !strings.Contains("ABCDF", grade) {
		return "", "", nil, false
	}
	return grade, strings.TrimSpace(gr.Summary), gr.Issues, true
}

// balancedObjects returns every top-level {…} substring in s (brace-depth matched, ignoring braces
// inside JSON strings), in order. Lets us try each candidate when the model emits prose around bare JSON.
func balancedObjects(s string) []string {
	var out []string
	depth, start := 0, -1
	inStr, esc := false, false
	for i, r := range s {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"':
			inStr = !inStr
		case inStr:
			// braces inside strings don't count toward depth
		case r == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case r == '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, s[start:i+1])
				}
			}
		}
	}
	return out
}
