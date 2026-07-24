// Trust · T2a — executable verify gate. After a task's worker commits its branch, run the
// project's own acceptance checks (build / test / smoke) IN THE WORKTREE before the branch is
// eligible to merge. This is the objective layer of the verify gate; T2b adds an independent
// reviewer agent for the judgment layer.
//
// Checks are the TEAM'S OWN DATA (reference-not-command): they live in the BASE repo at
// `.partyline/verify` (one shell command per line), and we read them from the base repo — NOT the
// agent's worktree — so a task can't weaken its own gate by editing the file it's judged against.
// No checks file → the gate is a no-op, reported honestly as SKIPPED (not a pass).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	eng "partyline.sh/partyline/internal/engine"
)

const verifyFile = ".partyline/verify"

// readChecks returns the acceptance-check commands from the BASE repo (the version the team
// committed, not the agent's edited worktree). One command per non-empty, non-comment (#) line;
// nil when there's no verify file.
func readChecks(baseRepo string) []string {
	b, err := os.ReadFile(filepath.Join(baseRepo, verifyFile))
	if err != nil {
		return nil
	}
	var cmds []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" && !strings.HasPrefix(ln, "#") {
			cmds = append(cmds, ln)
		}
	}
	return cmds
}

// verifyResult is the outcome of the acceptance checks against one task's worktree.
type verifyResult struct {
	ran     bool   // false → no checks defined (gate skipped, not a pass)
	ok      bool   // all checks passed
	reasons string // on failure: which check failed + a bounded output tail (for the human)
	// warn is a non-failing advisory surfaced on the task note. T2d uses it for "visual verify on
	// (web toggle) but no renderer resolved" — the gate must NOT fail the run or execute anything
	// web-supplied, so it degrades to an honest warning rather than a quarantine.
	warn string
}

// runChecks executes each check via `sh -c` in the worktree, in order, stopping at the FIRST
// failure (a later check is meaningless once the build fails). Each check is bounded by timeout.
// The failure's output tail is captured (bounded) so a chatty check can't bloat the ledger/detail.
func runChecks(wtPath string, checks []string, timeout time.Duration) verifyResult {
	if len(checks) == 0 {
		return verifyResult{ran: false}
	}
	for _, cmd := range checks {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		c := exec.CommandContext(ctx, "sh", "-c", cmd)
		c.Dir = wtPath
		out, err := c.CombinedOutput()
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("check timed out (>%s): %s", timeout, cmd)}
		}
		if err != nil {
			return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("check failed: %s\n%s", cmd, tailString(string(out), 1200))}
		}
	}
	return verifyResult{ran: true, ok: true}
}

// tailString trims to the last n bytes (with a leading ellipsis) so a long check log stays bounded.
func tailString(s string, n int) string {
	if s = strings.TrimSpace(s); len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// headString trims to the first n bytes (with a trailing ellipsis) — for the diff we hand the
// reviewer, where the top of the change is the most informative.
func headString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(diff truncated)…"
}

// ---- Trust · T2b: independent reviewer gate (the judgment layer) ----
//
// After the objective checks (T2a) pass, an INDEPENDENT reviewer judges whether the diff actually
// satisfies the task — a fresh engine with ONLY the spec + diff, no thread memory, no tools, framed
// adversarially (verifier ≠ producer). Opt-in per repo via `.partyline/review` (presence enables;
// its contents are extra review guidance appended to the prompt). FAIL-CLOSED: if the reviewer says
// no, can't be parsed, or can't run, the task is quarantined — a trust gate defaults to needs-human,
// never to auto-merge.
const reviewFile = ".partyline/review"

const maxDiffBytes = 120_000 // bound the diff we send the reviewer (cost + context)

// readReview reports whether the reviewer gate is enabled for this repo (the file exists) and any
// extra rubric the team wrote in it.
func readReview(baseRepo string) (enabled bool, rubric string) {
	b, err := os.ReadFile(filepath.Join(baseRepo, reviewFile))
	if err != nil {
		return false, ""
	}
	return true, strings.TrimSpace(string(b))
}

// acceptanceMarker is the exact header workItemTaskText (web) writes above a work item's
// definition-of-done. Its presence in a task means the task carries explicit acceptance criteria —
// which, on their own, turn the reviewer ON even without a repo .partyline/review file. Spec-driven
// posture: if the team bothered to write a definition-of-done, it gets verified. A task with no
// criteria and no review file still skips the reviewer (the fast path is preserved).
const acceptanceMarker = "Acceptance criteria (definition of done):"

func taskHasAcceptanceCriteria(task string) bool {
	return strings.Contains(task, acceptanceMarker)
}

// gitDiff returns the task branch's WHOLE committed change — from where it forked (merge-base against
// the base ref), NOT the last commit alone. An auto-repaired task adds a commit per repair round, so
// `HEAD^..HEAD` shows only the final small fix and the real work in earlier commits goes invisible:
// that's how a retried run got FAILed on a 3-line lint tail while its actual fix sat in an earlier
// commit (the same class of bug #513 fixed for the review-agent's branchDiff, never applied here).
// The merge-base is robust to the base advancing after the fork. Falls back to the single-commit view
// only when no base ref resolves (a bare local repo) — unchanged from before.
func gitDiff(wtPath, base string) string {
	base = strings.TrimSpace(base)
	// Fork-point candidates, in order: the configured base on origin, the base as a bare ref (chain
	// predecessor / local branch), then origin/HEAD (the default branch — covers an unset base).
	var refs []string
	if base != "" && !strings.HasPrefix(base, "-") {
		refs = append(refs, "origin/"+base, base)
	}
	refs = append(refs, "origin/HEAD")
	for _, ref := range refs {
		mb, err := exec.Command("git", "-C", wtPath, "merge-base", ref, "HEAD").Output()
		if err != nil {
			continue
		}
		if out, err := exec.Command("git", "-C", wtPath, "diff", strings.TrimSpace(string(mb)), "HEAD").Output(); err == nil && len(out) > 0 {
			return string(out)
		}
	}
	out, err := exec.Command("git", "-C", wtPath, "diff", "HEAD^", "HEAD").Output()
	if err != nil { // no parent commit (rare) → show HEAD itself
		out, _ = exec.Command("git", "-C", wtPath, "show", "--format=", "HEAD").Output()
	}
	return string(out)
}

// reviewerPrompt frames the adversarial review + the exact VERDICT line we parse back.
func reviewerPrompt(task, diff, rubric string) string {
	var b strings.Builder
	b.WriteString("You are an ADVERSARIAL code reviewer. Find reasons this change does NOT correctly and completely satisfy the task. Be skeptical; do not give the benefit of the doubt.\n\n")
	b.WriteString("TASK (the spec the change must satisfy):\n")
	b.WriteString(task)
	b.WriteString("\n\nDIFF (the change to review):\n")
	b.WriteString(headString(diff, maxDiffBytes))
	if rubric != "" {
		b.WriteString("\n\nADDITIONAL REVIEW GUIDANCE (from the team):\n")
		b.WriteString(rubric)
	}
	// #2b: when the task states an explicit definition-of-done, hold the diff to EACH criterion. An
	// unmet or unverifiable criterion is a hard FAIL — this is what makes acceptance criteria a gate
	// rather than decoration the builder happened to read.
	if taskHasAcceptanceCriteria(task) {
		b.WriteString("\n\nThe task lists explicit ACCEPTANCE CRITERIA (a definition of done). Check EACH criterion against the diff individually. Any criterion that is not fully met — or that you cannot confirm is met from the diff alone — is an automatic FAIL; name the criterion and why. Do not pass unless every criterion is satisfied.\n")
	}
	b.WriteString("\n\nList concrete problems: missing requirements, bugs, incorrect logic, unhandled cases. Judge ONLY whether the diff satisfies the task. If it fully and correctly does, with no material problems, it passes.\n")
	b.WriteString("\nEnd your reply with EXACTLY one line:\nVERDICT: PASS\nor\nVERDICT: FAIL — <one-line reason>\n")
	return b.String()
}

// parseReviewVerdict reads the reviewer's reply for the trailing VERDICT line. FAIL-CLOSED: a reply
// with no parseable verdict is treated as a failure (needs a human), never a silent pass.
func parseReviewVerdict(reply string) (pass bool, reasons string) {
	lines := strings.Split(strings.TrimSpace(reply), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		up := strings.ToUpper(ln)
		if strings.HasPrefix(up, "VERDICT:") {
			v := strings.TrimSpace(ln[len("VERDICT:"):])
			if strings.HasPrefix(strings.ToUpper(v), "PASS") {
				return true, ""
			}
			// FAIL — carry the reviewer's reason + a bounded tail of the analysis above it.
			return false, "reviewer: " + tailString(reply, 1500)
		}
	}
	return false, "reviewer: no parseable VERDICT in the reply (fail-closed)\n" + tailString(reply, 800)
}

// runReviewer runs the independent reviewer gate on the given engine. ran=false when the gate is
// off (no .partyline/review). Otherwise it always yields a verdict — fail-closed on empty diff,
// engine error, timeout, or an unparseable reply.
func runReviewer(baseRepo, wtPath, base, task, engineName string, timeout time.Duration) verifyResult {
	enabled, rubric := readReview(baseRepo)
	// Spec-driven (#2a): a task that carries a definition-of-done gets the reviewer even when the repo
	// has no .partyline/review file — the acceptance criteria are the opt-in. Off + no criteria → the
	// gate stays a no-op (fast path).
	if !enabled && !taskHasAcceptanceCriteria(task) {
		return verifyResult{ran: false}
	}
	diff := gitDiff(wtPath, base)
	if strings.TrimSpace(diff) == "" {
		return verifyResult{ran: true, ok: false, reasons: "reviewer: the task produced no diff to review"}
	}
	// A fresh engine with NO tools (or the engine's strongest enforceable posture — read-only for
	// engines with no tool-less mode, the one sanctioned downgrade) and NO thread — it only reasons
	// over the text we give it, so it can't edit the branch and shares no context with the producer.
	spec, okSpec := engineSpecFor(engineName)
	if !okSpec {
		return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("reviewer: unknown engine %q — quarantined (fail-closed)", engineName)}
	}
	argv, stdinPrompt, err := reviewerOneShot(spec, reviewerPrompt(task, diff, rubric), "", nil)
	if err != nil {
		return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("reviewer: %v — quarantined (fail-closed)", err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runOneShot(ctx, wtPath, argv, stdinPrompt, spec.OneShotEnv(eng.ToolsNone)...)
	if ctx.Err() == context.DeadlineExceeded {
		return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("reviewer: timed out (>%s) — quarantined", timeout)}
	}
	if err != nil {
		return verifyResult{ran: true, ok: false, reasons: "reviewer: couldn't run the reviewer engine — quarantined (fail-closed)"}
	}
	pass, reasons := parseReviewVerdict(oneShotText(spec, out))
	return verifyResult{ran: true, ok: pass, reasons: reasons}
}

// verifyTask runs the full gate for a committed task, cheapest layer first so an early failure never
// spends the next layer's budget: T2a objective executable checks, then T2b independent textual
// reviewer, then T2d the visual reviewer (render the changed UI + look at it — see visual.go). Any
// layer failing quarantines the task (returns a failed result). If none is enabled the result is a
// no-op (ran=false → merge proceeds). T2d is last because rendering a browser + a vision review is
// the priciest layer — a broken build or a wrong-on-paper diff should stop before we ever render.
// engineName is the BUILD engine (crank's) — the textual reviewer runs on it at its strongest
// enforceable posture; the visual reviewer always runs local claude (it needs vision-in-CLI,
// which only claude has — and a different engine from the producer is stronger anyway).
func verifyTask(baseRepo, wtPath, base, task, engineName string, timeout time.Duration, vc visualCfg) verifyResult {
	if checks := runChecks(wtPath, readChecks(baseRepo), timeout); checks.ran && !checks.ok {
		return checks
	} else if rev := runReviewer(baseRepo, wtPath, base, task, engineName, timeout); rev.ran && !rev.ok {
		return rev
	} else if vis := runVisualReview(baseRepo, wtPath, task, timeout, vc); vis.ran && !vis.ok {
		return vis
	} else {
		// A visual WARN (toggle on, no renderer) is advisory — carry it up on an otherwise-clean
		// result so the caller can surface it without quarantining.
		return verifyResult{ran: checks.ran || rev.ran || vis.ran, ok: true, warn: vis.warn}
	}
}
