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
	"partyline.sh/partyline/internal/gate"
	"partyline.sh/partyline/internal/surface"
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
	// code is the surface.GateCode this outcome carries (Epic G.0). Set at each return site rather
	// than inferred later: verifyTask assembles a typed gate.Report from these, and deriving the
	// cause by grepping the prose we just wrote would be exactly the untyped reporting G exists to
	// remove. Empty on a skip.
	code string
	// name identifies the lane in the report ("build", "reviewer", "visual").
	name string
	// report is the typed outcome (Epic G.0). Populated only by verifyTask, which is the only
	// place that can see all three lanes at once. The ran/ok/reasons/warn fields above are a
	// PROJECTION of it — kept because crank.go reads them in a dozen places, and changing the
	// contract and its dozen call sites in one commit would make both harder to review.
	report *gate.Report
}

// runChecks executes each check via `sh -c` in the worktree, in order, stopping at the FIRST
// REGRESSION (a later check is meaningless once the build fails). Each check is bounded by timeout.
// The failure's output tail is captured (bounded) so a chatty check can't bloat the ledger/detail.
//
// BASELINE-AWARE: a check that fails in the worktree is re-run at the BASE repo. Fails there too →
// pre-existing debt this diff did not introduce (e.g. a repo-wide lint that's been red for weeks) —
// recorded as a WARN, never a quarantine. A gate that enforces an already-red check rejects perfect
// work every time (the run-dbf167b5 lesson: 37 pre-existing lint errors FAILed a clean diff).
// Pragmatic caveat, accepted: the base repo's checkout approximates the fork point (it may have
// advanced) — close enough to separate "this task broke it" from "it was already broken".
func runChecks(baseRepo, wtPath string, checks []string, timeout time.Duration) verifyResult {
	if len(checks) == 0 {
		return verifyResult{ran: false}
	}
	var warns []string
	for _, cmd := range checks {
		out, timedOut, err := runCheck(wtPath, cmd, timeout)
		if timedOut {
			return verifyResult{ran: true, ok: false, code: surface.CodeCheckTimeout, name: "checks", reasons: fmt.Sprintf("check timed out (>%s): %s", timeout, cmd)}
		}
		if err == nil {
			continue
		}
		if _, bTimedOut, bErr := runCheck(baseRepo, cmd, timeout); bErr != nil && !bTimedOut {
			warns = append(warns, fmt.Sprintf("check fails on the BASE too — pre-existing, not this task's regression: %s", cmd))
			continue
		}
		return verifyResult{ran: true, ok: false, code: surface.CodeCheckFailed, name: "checks", reasons: fmt.Sprintf("check failed: %s\n%s", cmd, tailString(out, 1200))}
	}
	return verifyResult{ran: true, ok: true, name: "checks", code: checksCode(warns), warn: strings.Join(warns, "; ")}
}

// runCheck runs one acceptance check via `sh -c` in dir, bounded by timeout.
func runCheck(dir, cmd string, timeout time.Duration) (out string, timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	b, err := c.CombinedOutput()
	return string(b), ctx.Err() == context.DeadlineExceeded, err
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
// baselineNotes are HARNESS-VERIFIED pre-existing failures (checks that fail on the base too) —
// ground truth the reviewer must not blame on this diff.
func reviewerPrompt(task, diff, rubric, baselineNotes string) string {
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
	if baselineNotes != "" {
		b.WriteString("\n\nHARNESS-VERIFIED BASELINE FACTS (the gate ran these itself — treat as ground truth):\n")
		b.WriteString(baselineNotes)
		b.WriteString("\n")
	}
	// #2b: when the task states an explicit definition-of-done, hold the diff to EACH criterion. An
	// unmet or unverifiable criterion is a hard FAIL — this is what makes acceptance criteria a gate
	// rather than decoration the builder happened to read. ONE carve-out (the run-dbf167b5 lesson):
	// a criterion failing for reasons ALREADY TRUE on the base branch is authored-wrong, not built-
	// wrong — judge the diff's own regressions, never pre-existing repo debt.
	if taskHasAcceptanceCriteria(task) {
		b.WriteString("\n\nThe task lists explicit ACCEPTANCE CRITERIA (a definition of done). Check EACH criterion against the diff individually. Any criterion that is not fully met — or that you cannot confirm is met from the diff alone — is an automatic FAIL; name the criterion and why. EXCEPTION: a criterion whose failure is PRE-EXISTING on the base branch (it was already failing before this diff — e.g. a repo-wide check listed in the baseline facts above, or evidence in the task/log that the failure predates the change) is a defect in the CRITERION, not the work: note it as \"pre-existing on base, criterion needs rescoping\" and do NOT fail the task for it. Hold the diff strictly to regressions it introduces and to the new behavior it promises. Do not pass unless every criterion is satisfied or explicitly noted as pre-existing.\n")
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
func runReviewer(baseRepo, wtPath, base, task, engineName string, timeout time.Duration, baselineNotes string) verifyResult {
	enabled, rubric := readReview(baseRepo)
	// Spec-driven (#2a): a task that carries a definition-of-done gets the reviewer even when the repo
	// has no .partyline/review file — the acceptance criteria are the opt-in. Off + no criteria → the
	// gate stays a no-op (fast path).
	if !enabled && !taskHasAcceptanceCriteria(task) {
		return verifyResult{ran: false}
	}
	diff := gitDiff(wtPath, base)
	if strings.TrimSpace(diff) == "" {
		return verifyResult{ran: true, ok: false, code: surface.CodeReviewerNoDiff, name: "reviewer", reasons: "reviewer: the task produced no diff to review"}
	}
	// A fresh engine with NO tools (or the engine's strongest enforceable posture — read-only for
	// engines with no tool-less mode, the one sanctioned downgrade) and NO thread — it only reasons
	// over the text we give it, so it can't edit the branch and shares no context with the producer.
	spec, okSpec := engineSpecFor(engineName)
	if !okSpec {
		return verifyResult{ran: true, ok: false, code: surface.CodeEngineUnknown, name: "reviewer", reasons: fmt.Sprintf("reviewer: unknown engine %q — quarantined (fail-closed)", engineName)}
	}
	argv, stdinPrompt, err := reviewerOneShot(spec, reviewerPrompt(task, diff, rubric, baselineNotes), "", nil)
	if err != nil {
		return verifyResult{ran: true, ok: false, code: surface.CodeEngineLaunchFailed, name: "reviewer", reasons: fmt.Sprintf("reviewer: %v — quarantined (fail-closed)", err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runOneShot(ctx, wtPath, argv, stdinPrompt, spec.OneShotEnv(eng.ToolsNone)...)
	if ctx.Err() == context.DeadlineExceeded {
		return verifyResult{ran: true, ok: false, code: surface.CodeReviewerTimeout, name: "reviewer", reasons: fmt.Sprintf("reviewer: timed out (>%s) — quarantined", timeout)}
	}
	if err != nil {
		return verifyResult{ran: true, ok: false, code: surface.CodeEngineLaunchFailed, name: "reviewer", reasons: "reviewer: couldn't run the reviewer engine — quarantined (fail-closed)"}
	}
	pass, reasons := parseReviewVerdict(oneShotText(spec, out))
	return verifyResult{ran: true, ok: pass, name: "reviewer", code: reviewerCode(pass, reasons), reasons: reasons}
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
	// Lanes that never ran stay zero-valued and are recorded as an explicit `skip` in the report,
	// which is how "we stopped early because the build failed" stays distinguishable from "this
	// repo has no reviewer configured".
	var checks, rev, vis verifyResult
	started := time.Now()
	finish := func(out verifyResult) verifyResult {
		out.report = buildReport(checks, rev, vis)
		out.report.Started = started
		out.report.Millis = time.Since(started).Milliseconds()
		return out
	}

	checks = runChecks(baseRepo, wtPath, readChecks(baseRepo), timeout)
	if checks.ran && !checks.ok {
		return finish(checks)
	}
	// checks.warn = harness-verified pre-existing failures (red on base too). Fed to the reviewer
	// as ground truth so it never fails the task for debt the diff didn't introduce.
	rev = runReviewer(baseRepo, wtPath, base, task, engineName, timeout, checks.warn)
	if rev.ran && !rev.ok {
		return finish(rev)
	}
	vis = runVisualReview(baseRepo, wtPath, task, timeout, vc)
	if vis.ran && !vis.ok {
		return finish(vis)
	}
	// WARNs (pre-existing check failures; visual toggle with no renderer) are advisory —
	// carried up on an otherwise-clean result so the caller surfaces them without quarantining.
	warn := checks.warn
	if vis.warn != "" {
		if warn != "" {
			warn += "; "
		}
		warn += vis.warn
	}
	return finish(verifyResult{ran: checks.ran || rev.ran || vis.ran, ok: true, warn: warn})
}

// ---- Epic G.0: the typed report ----
//
// verifyTask still returns the legacy verifyResult, because crank.go reads ran/ok/reasons/warn in
// a dozen places and rewriting that in the same change as introducing the contract would make both
// harder to review. The Report is built ALONGSIDE it and carried on the result; the legacy fields
// become a projection of the report rather than a separate source of truth.

// checksCode names the outcome of a passing check run: clean, or clean-but-carrying advisories
// because something was already red on the base branch.
func checksCode(warns []string) string {
	if len(warns) > 0 {
		return surface.CodeCheckBaselineRed
	}
	return surface.CodeOK
}

// reviewerCode distinguishes the two ways a reviewer says no. "Rejected" is a judgment about the
// diff; "unparseable" is a failure to answer at all, and only the second is a harness problem.
func reviewerCode(pass bool, reasons string) string {
	switch {
	case pass:
		return surface.CodeOK
	case strings.Contains(reasons, "no parseable VERDICT"):
		return surface.CodeReviewerUnparseable
	default:
		return surface.CodeReviewerRejected
	}
}

// lane converts one sub-result into a report lane. A sub-result that never ran becomes an explicit
// `skip` rather than being dropped: "this repo has no reviewer" is a fact worth recording, and it
// is what stops a gate with nothing enabled reading as a pass.
func lane(kind, name string, v verifyResult, blocking bool) gate.CheckResult {
	c := gate.CheckResult{Name: name, Kind: kind, Blocking: blocking, Code: v.code}
	switch {
	case !v.ran:
		c.Status, c.Code = gate.StatusSkip, surface.CodeSkipped
	case v.ok && v.warn != "":
		c.Status, c.Detail = gate.StatusWarn, gate.Truncate(v.warn, 1200)
	case v.ok:
		c.Status = gate.StatusPass
	default:
		c.Status, c.Detail = gate.StatusFail, gate.Truncate(v.reasons, 1500)
	}
	if c.Code == "" {
		c.Code = surface.CodeOK
	}
	return c
}

// buildReport assembles the three lanes into one typed report and finalises the verdict.
func buildReport(checks, rev, vis verifyResult) *gate.Report {
	r := gate.New()
	r.Add(lane(gate.KindCheck, "checks", checks, true))
	r.Add(lane(gate.KindReviewer, "reviewer", rev, true))
	r.Add(lane(gate.KindVisual, "visual", vis, true))
	// A warn lane is advisory by construction: it must never quarantine, and marking it
	// non-blocking here is what makes that true in the verdict rather than only in prose.
	for i := range r.Checks {
		if r.Checks[i].Status == gate.StatusWarn {
			r.Checks[i].Blocking = false
		}
	}
	r.Finalize()
	return r
}
