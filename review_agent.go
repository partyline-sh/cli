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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
	"partyline.sh/partyline/internal/gitwt"
)

const reviewTimeout = 8 * time.Minute // tooled verification (read-only grep/read passes) needs headroom beyond the old tool-less 4m

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

	targetID, tasks, criteria, err := api.ReviewTarget(d.Base, d.Token, ev.RunID)
	if err != nil {
		return fail("review", err)
	}
	logln(fmt.Sprintf("collecting changes across %d task branch(es)…", len(tasks)))
	// The base the branches forked from — bounds each diff to the WHOLE branch (merge-base..tip).
	base := strings.TrimSpace(ev.BaseBranch)
	if base == "" {
		base = gitwt.DefaultBaseName(dir)
	}

	// Assemble each reviewable task's branch diff (only tasks that produced a branch), bounded overall
	// so a huge run can't blow the reviewer's context/cost. The branch always persists even after the
	// worktree is gone, so we diff the branch ref directly from the base repo — no worktree needed.
	var b strings.Builder
	reviewed := 0
	for _, t := range tasks {
		if strings.TrimSpace(t.Branch) == "" {
			continue
		}
		diff := branchDiff(dir, t.Branch, base)
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
	// Put the grader INSIDE the code under review: a throwaway detached checkout of the first
	// reviewed branch, so its read-only tools answer "does X exist?" about the actual branch — never
	// about whatever the base repo happens to have checked out. Multi-branch runs get the first
	// branch's tree (the diffs carry the rest); on checkout failure we fall back to the base repo dir
	// and the prompt SAYS so, so the grader can't mistake an arbitrary tree for the branch.
	reviewDir, treeNote := dir, "YOUR WORKING TREE: the project repository at an arbitrary checkout — it may NOT reflect the branch under review. Verify only what the diffs themselves show; mark everything else unverified.\n"
	for _, t := range tasks {
		br := strings.TrimSpace(t.Branch)
		if br == "" {
			continue
		}
		if wt, cleanup := reviewCheckout(dir, br); wt != "" {
			defer cleanup()
			reviewDir = wt
			treeNote = "YOUR WORKING TREE: a read-only checkout of branch `" + br + "` at its tip — the code under review. USE your tools (Read/Grep/Glob) to verify claims: the acceptance criteria's verify hints, any \"already exists\" assertion, imports/exports the diff relies on.\n"
			logln("grader working tree: " + br + " (detached checkout)")
		}
		break
	}
	grade, summary, issues, usage, err := runGradedReview(reviewDir, engineName, headString(b.String(), maxDiffBytes), ev.Model, treeNote, criteria, reviewTimeout, logln)
	if err != nil {
		return fail("review", err)
	}
	logln(fmt.Sprintf("done — grade %s, %d issue(s)", grade, len(issues)))
	if err := api.RecordReview(d.Base, d.Token, targetID, grade, summary, engineName, issues, usage.fresh, usage.cacheRead, usage.cost); err != nil {
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
//
// FAILOVER: when the branch isn't local — the review was dispatched to a machine that didn't build
// the run (its builder went offline / rate-limited, but the branch was PUSHED for its PR) — fetch the
// ref from origin and diff that. This is what lets a review run on any node with the repo, not only
// the builder. The branch name is DATA from our own store; it's never a flag (guarded) and reaches
// git only as a ref name.
func branchDiff(repo, branch, base string) string {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return ""
	}
	// The WHOLE branch vs where it forked from — merge-base against origin/<base> — never the last
	// commit alone. `branch^..branch` graded run 05203e73 an F on a docs-only tail commit while the
	// actual feature sat two commits down the same branch (repair rounds and retries legitimately
	// produce multi-commit branches). Fall back to the single-commit view only when no base resolves.
	ref := branch
	if exec.Command("git", "-C", repo, "rev-parse", "--verify", branch).Run() != nil {
		// Not local: fetch the pushed ref (no local branch created, nothing mutated).
		if exec.Command("git", "-C", repo, "fetch", "origin", "refs/heads/"+branch).Run() != nil {
			return ""
		}
		ref = "FETCH_HEAD"
	}
	if base != "" && !strings.HasPrefix(base, "-") {
		_ = exec.Command("git", "-C", repo, "fetch", "origin", base).Run()
		if mb, err := exec.Command("git", "-C", repo, "merge-base", "origin/"+base, ref).Output(); err == nil {
			if out, err := exec.Command("git", "-C", repo, "diff", strings.TrimSpace(string(mb)), ref).Output(); err == nil && len(out) > 0 {
				return string(out)
			}
		}
	}
	out, err := exec.Command("git", "-C", repo, "diff", ref+"^", ref).Output()
	if err != nil {
		out, _ = exec.Command("git", "-C", repo, "show", "--format=", ref).Output()
	}
	return string(out)
}

// reviewCheckout materializes the branch under review in a THROWAWAY detached worktree, so the
// grader's read-only tools inspect the ACTUAL code being judged — not whatever happens to be checked
// out in the base repo (an arbitrary tree that produced fabricated "verified" findings). Returns the
// worktree dir + a cleanup, or ("", nil) when it can't (caller falls back to the base repo dir and
// the prompt says so honestly). Nothing is mutated: detached HEAD, temp dir, removed after.
func reviewCheckout(repo, branch string) (string, func()) {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return "", nil
	}
	// Freshen the ref best-effort; the local branch (builder machine) or origin ref (failover) wins.
	_ = exec.Command("git", "-C", repo, "fetch", "origin", "refs/heads/"+branch).Run()
	ref := branch
	if exec.Command("git", "-C", repo, "rev-parse", "--verify", branch).Run() != nil {
		if exec.Command("git", "-C", repo, "rev-parse", "--verify", "origin/"+branch).Run() != nil {
			return "", nil
		}
		ref = "origin/" + branch
	}
	tmp, err := os.MkdirTemp("", "ptln-review-")
	if err != nil {
		return "", nil
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", tmp, ref).CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		_ = out
		return "", nil
	}
	return tmp, func() {
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", tmp).Run()
		_ = os.RemoveAll(tmp)
	}
}

// reviewGradePrompt frames the same adversarial, independent review as T2b but asks for a STRUCTURED
// graded verdict as a single fenced JSON block (parsed by jsonBlockRe, like describe). Advisory: the
// grade informs the human, it doesn't gate anything.
func reviewGradePrompt(tasksAndDiffs string, criteria []api.ReviewCriterion, treeNote string) string {
	var b strings.Builder
	b.WriteString("You are an INDEPENDENT, adversarial code reviewer. You did NOT write this code. For each task below, judge how correctly and completely its diff satisfies the task, and grade the run as a whole.\n\n")
	b.WriteString("Assess: does the change do what the task asked? Are there bugs, missing requirements, unhandled cases, or sloppy/unsafe code? Be skeptical; do not give the benefit of the doubt.\n")
	b.WriteString("If the change ASSERTS that existing code already provides something (a spec or doc claiming fields, endpoints, or components exist), verify those claims — an unverified assertion is a finding, not a fact.\n\n")
	if treeNote != "" {
		b.WriteString(treeNote)
		b.WriteString("\n")
	}
	b.WriteString("VERIFICATION HONESTY (hard rule): only claim to have checked something if you actually did it with your tools in this session. If you have no tools, or a claim can't be settled from what you can see, write \"unverified\" — a review that FABRICATES verification (\"confirmed via grep\" without running one) is worse than no review, and past fabricated findings have wrongly failed correct work.\n\n")
	if len(criteria) > 0 {
		// The plan's own definition of done — grade against THIS checklist, not inferred scope. This is
		// what turns grading from "guess what done means" into "check the checklist".
		b.WriteString("ACCEPTANCE CRITERIA — the task is complete ONLY if every one of these holds. Judge each explicitly; an unmet criterion is at least a med issue:\n")
		for _, c := range criteria {
			b.WriteString("  - " + strings.TrimSpace(c.Text))
			if strings.TrimSpace(c.Verify) != "" {
				b.WriteString(" (verify: " + strings.TrimSpace(c.Verify) + ")")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
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
// reviewUsage is the review one-shot's token/cost accounting — the display fields (fresh + cache-read
// tokens, and claude's own dollar cost), so the run detail can total build + review. Zero = the engine
// reported nothing (only claude fills these today), which the web stores as null, not a fake 0.
type reviewUsage struct {
	fresh     int
	cacheRead int
	cost      float64
}

func runGradedReview(dir, engineName, tasksAndDiffs, model, treeNote string, criteria []api.ReviewCriterion, timeout time.Duration, logf func(string)) (string, string, []api.ReviewIssue, reviewUsage, error) {
	spec, ok := engineSpecFor(engineName)
	if !ok {
		return "", "", nil, reviewUsage{}, fmt.Errorf("unknown engine %q", engineName)
	}
	if !modelRe.MatchString(model) { // model selection: the project's review-phase model (validated)
		model = ""
	}
	argv, stdinPrompt, err := graderOneShot(spec, reviewGradePrompt(tasksAndDiffs, criteria, treeNote), model, logf)
	if err != nil {
		return "", "", nil, reviewUsage{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runOneShot(ctx, dir, argv, stdinPrompt, spec.OneShotEnv(eng.ToolsNone)...)
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", nil, reviewUsage{}, fmt.Errorf("reviewer timed out (>%s)", timeout)
	}
	if err != nil {
		// Carry the engine's OWN words up to the run detail — "couldn't run the reviewer engine" with
		// no cause left the web guessing ("the machine may have gone offline") when the truth was e.g.
		// a provider rate limit sitting right in stderr. Bounded tail; the web renders it verbatim.
		return "", "", nil, reviewUsage{}, fmt.Errorf("couldn't run the reviewer engine (%s): %s", spec.Name, oneShotErrDetail(err, out))
	}
	// Parse once for BOTH the graded text and the token/cost usage. ParseResult reads claude's json
	// envelope (result + usage + total_cost_usd); a malformed envelope errors, so fall back to the raw
	// output for the grade parse (mirrors oneShotText) with zero usage.
	var usage reviewUsage
	text := string(out)
	if res, perr := spec.ParseResult(out); perr == nil {
		text = res.Text
		usage = reviewUsage{fresh: res.Usage.Fresh(), cacheRead: res.Usage.CacheReadInputTokens, cost: res.CostUSD}
	}
	grade, summary, issues, err := parseGradedReview(text)
	return grade, summary, issues, usage, err
}

// oneShotErrDetail digs the most human-useful line out of a failed one-shot: the engine's stderr tail
// (exec.Cmd.Output stores it on ExitError — that's where "usage limit reached / resets 10:30" lives),
// else the stdout tail, else the bare error. Single line, bounded, safe to store in run detail.
func oneShotErrDetail(err error, out []byte) string {
	tail := func(b []byte) string {
		s := strings.TrimSpace(string(b))
		if s == "" {
			return ""
		}
		lines := strings.Split(s, "\n")
		s = strings.TrimSpace(lines[len(lines)-1])
		if r := []rune(s); len(r) > 300 {
			s = string(r[:300]) + "…"
		}
		return s
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if s := tail(ee.Stderr); s != "" {
			return fmt.Sprintf("%v — %s", err, s)
		}
	}
	if s := tail(out); s != "" {
		return fmt.Sprintf("%v — %s", err, s)
	}
	return err.Error()
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
