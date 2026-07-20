package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// crank.go — E4.8, the worklist loop: drive a backlog of tasks one at a time through the
// worker atom (E4.1), each in ITS OWN worktree, sharing ONE context thread. The brakes are
// the point (see docs/E4-CONDUCTOR-PLAN.md) — this prepares N reviewable branches, it does
// NOT ship anything:
//
//	ptln crank --file backlog.txt [--thread <id>] [--max N] [--max-tokens N]
//	           [--halt-on-fail K] [--timeout 20m] [--allow-bash] [--no-commit] [--resume]
//
// Each non-blank, non-# line of the file is one task. Sequential (not parallel) so each item
// sees what the previous ones recorded on the shared thread — the moat applied to autonomy.
// Stops on: list exhausted · --max reached · --max-tokens exceeded (O.5, Claude-first; other
// engines report no usage → no token halt) · K consecutive failures · (per-item) time budget.

type crankOpts struct {
	file          string
	thread        string
	run           string // O.3: run id (UUID) — when set, crank self-reports per-task lifecycle
	max           int    // 0 = all
	maxTokens     int    // O.5: crude token ceiling for the whole worklist; 0 = unbounded (off)
	haltOnFail    int
	timeout       time.Duration
	allowBash     bool
	commit        bool
	resume        bool           // #81 slice 3a: when set (and run != ""), skip tasks already `done` in the run store
	resumeSkip    map[int]bool   // original indices to skip this run (built from the run store); nil = skip nothing
	resumeHandles map[int]string // Slice 2: idx → engine resume token for not-done tasks (resume-in-place); nil = none
	// idx → why the previous attempt STOPPED (for a quarantined task: the reviewer's verdict).
	// This is what closes the review loop: without it, "Continue" re-ran the same task text blind —
	// the agent never learned WHY it failed, produced the same work, failed the same review, forever.
	// The owner's words for that experience: "fundamentally broken". The findings ride the worker's
	// PROMPT only — the stored task text, emits, and logs keep the original.
	resumeFindings map[int]string
	restart        bool        // "Restart" CTA: start the run OVER — fresh worktree+branch per task, ignore prior state
	claim          bool        // #77 slice 2: claim tasks from the run store (fleet mode) instead of a static file
	workers        int         // #77 slice 2: concurrent claim-loop workers (claim mode only); <1 → env/default 1
	mergePolicy    string      // #77 slice 3: per-task branch handling after commit — manual (default) | pr | auto
	globals        string      // Phase B3: the project's globals document, written into each task's worktree as CLAUDE.md
	skills         []api.Skill // org skill library: injected into each task's worktree (.agents/.claude skills) + named in the worker prompt
	skillsDir      string      // the daemon's --skills-dir: holds skills.json + <name>.zip bundles (read at materialize time)
	model          string      // model selection: the build model passed to each task's worker (--model); "" = engine default
	branch         string      // CHAIN branch (--branch): every task in this run builds on THIS branch
	base           string      // BASE branch (--base): the project's configured fork point AND PR target
	// instead of deriving its own. A chain is one deliverable: its members share one
	// branch + worktree in series, so each step sees the previous step's work and the
	// whole chain reviews as a single PR. "" = the per-task derived name (unchained).
	engine string // Epic #73: the build engine for every task's worker; "" = claude
	// visual (T2d) turns on the visual verify gate for this run — the WEB toggle, delivered by the
	// daemon as --visual (or PARTYLINE_VISUAL=1). It enables the gate WITHOUT a repo `.partyline/visual`
	// file; the renderer still resolves to the repo-trusted script or a daemon-hardcoded preset.
	visual bool
	// visualRoutes are SAFE app paths (DATA) the daemon's framework preset screenshots when the repo
	// has no `.partyline/visual` script. Read from the --visual-routes file; never executed.
	visualRoutes []string
	// gitProvider is the org's active repo provider (gitlab | bitbucket) for pr/auto runs; "" = github
	// (the default). GitHub gets the brokered PR path; gitlab/bitbucket push the branch and the merge
	// step emits a provider-correct "open the MR/PR" note instead of attempting/ mentioning `gh`.
	gitProvider string
}

type crankResult struct {
	task             string
	branch           string
	ok               bool
	note             string
	tokens           int          // O.5: tokens this task's worker reported (0 = unknown / no usage seen)
	prURL            string       // #212: the PR opened by merge_policy pr/auto (empty otherwise)
	summary          string       // #263: the worker's own "what I changed / what to review" summary (run legibility)
	durationMs       int          // #263: wall-clock the task took, in milliseconds (0 = not measured)
	verify           verifyResult // Trust · T2a: acceptance-check outcome (ran/ok/reasons); zero value = no checks
	noPR             bool         // merge_policy pr/auto committed but opened NO PR (push/gh failed) → route to review, never silent-ship
	rateLimitResetAt time.Time    // when the quota window resets; zero = none GIVEN (not "not blocked")
	rateLimited      bool         // the provider refused this task — see workerOutcome.rateBlocked
	rateNote         string       // the provider's own wording for the block, when it gave us one
	resumeHandle     string       // Slice 2: engine's opaque resume token (Claude session id); "" = restart-only
	invokedSkills    []string     // injected skills the agent USED in this task (claude stream only); unioned + reported per run
}

func crankMain(args []string) {
	o := crankOpts{haltOnFail: 2, timeout: 20 * time.Minute, commit: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file", "-f":
			if i++; i < len(args) {
				o.file = args[i]
			}
		case "--thread":
			if i++; i < len(args) {
				o.thread = strings.TrimSpace(args[i])
			}
		case "--run":
			if i++; i < len(args) {
				o.run = strings.TrimSpace(args[i])
			}
		case "--max":
			if i++; i < len(args) {
				fmt.Sscanf(args[i], "%d", &o.max)
			}
		case "--max-tokens":
			if i++; i < len(args) {
				fmt.Sscanf(args[i], "%d", &o.maxTokens)
			}
		case "--halt-on-fail":
			if i++; i < len(args) {
				fmt.Sscanf(args[i], "%d", &o.haltOnFail)
			}
		case "--timeout":
			if i++; i < len(args) {
				if d, err := time.ParseDuration(args[i]); err == nil {
					o.timeout = d
				}
			}
		case "--allow-bash":
			o.allowBash = true
		case "--no-commit":
			o.commit = false
		case "--resume":
			o.resume = true
		case "--restart":
			o.restart = true
		case "--claim":
			o.claim = true
		case "--workers":
			if i++; i < len(args) {
				fmt.Sscanf(args[i], "%d", &o.workers)
			}
		case "--merge-policy":
			if i++; i < len(args) {
				o.mergePolicy = strings.TrimSpace(args[i])
			}
		case "--git-provider":
			// Active repo provider (gitlab | bitbucket) for the merge step; github/empty = default path.
			if i++; i < len(args) {
				o.gitProvider = strings.TrimSpace(args[i])
			}
		case "--branch":
			// A chain's shared branch name. Server-supplied but shape-validated by the daemon before it
			// reaches this argv; re-slugged by gitwt before it becomes a ref.
			if i+1 < len(args) {
				i++
				o.branch = strings.TrimSpace(args[i])
			}
		case "--base":
			// The project's configured base branch: the ref new work forks FROM and the PR targets.
			// Server-supplied but shape-validated by the daemon before it reaches this argv.
			if i+1 < len(args) {
				i++
				o.base = strings.TrimSpace(args[i])
			}
		case "--model":
			// Model selection: the build model for every task's worker. The daemon validates it before
			// it reaches this argv; crank just forwards it to the worker's --model.
			if i++; i < len(args) {
				o.model = strings.TrimSpace(args[i])
			}
		case "--engine":
			// Epic #73: the build engine for every task's worker. The daemon only forwards
			// registry-valid names; re-checked below so a hand-typed value fails fast too.
			if i++; i < len(args) {
				o.engine = strings.TrimSpace(args[i])
			}
		case "--globals-file":
			// Phase B3: the daemon writes the project's globals document to a file and passes its path
			// here (the doc can be large + multi-line, so a file, not an argv value). Read best-effort —
			// a missing/unreadable file just means no injected globals, never a failed run.
			if i++; i < len(args) {
				if b, err := os.ReadFile(strings.TrimSpace(args[i])); err == nil {
					o.globals = string(b)
				}
			}
		case "--skills-dir":
			// Org skill library: the daemon stages the run's ENABLED skills to a dir and passes its
			// path here (parallel to --globals-file). Load best-effort — a missing/unreadable set just
			// means no injected skills, never a failed run.
			if i++; i < len(args) {
				o.skillsDir = strings.TrimSpace(args[i])
				o.skills = loadSkillSet(o.skillsDir)
			}
		case "--visual":
			// T2d web toggle: enable the visual verify gate for this run even without a repo
			// `.partyline/visual` file. A FIXED flag (never web text) — see resolveRun.
			o.visual = true
		case "--visual-routes":
			// SAFE render DATA: a daemon-written file of app paths to screenshot (one per line).
			// Re-validated here (defense-in-depth) via safeVisualRoutes; a route is never executed.
			if i++; i < len(args) {
				o.visualRoutes = safeVisualRoutes(readRoutesFile(args[i]))
			}
		}
	}
	// PARTYLINE_VISUAL=1 is the env fallback for the --visual toggle (mirrors PARTYLINE_MAX_TOKENS),
	// so the run path can turn the gate on without editing argv.
	if !o.visual && strings.TrimSpace(os.Getenv("PARTYLINE_VISUAL")) == "1" {
		o.visual = true
	}
	// The worker prompt names the installed skills so the engine knows when to reach for them
	// (process-global: every task in this run shares the same set). "" when the run carried none.
	workerSkillManifest = skillManifest(o.skills)
	// The same set, as bare names, for INVOCATION detection in the streaming worker (which of the
	// injected skills the agent actually used). Only valid slugs — matches what was materialized.
	workerSkillNames = injectedSkillNames(o.skills)
	// The daemon appends --run; PARTYLINE_RUN_ID is the env fallback for the same value. Resolved
	// before the claim/file branch since claim mode is keyed entirely on the run id.
	if o.run == "" {
		o.run = strings.TrimSpace(os.Getenv("PARTYLINE_RUN_ID"))
	}
	// #77 slice 2 (claim/fleet mode): tasks come from the run store, not a file — so many workers
	// (here and on other org machines) can chew one run concurrently without collision. Requires a
	// run id; --file is ignored. Falls through to the shared prep (claude, repo, thread) below.
	if o.claim && o.file != "" {
		o.file = "" // claim mode is the source of truth; a stray --file must not be read
	}
	if !o.claim && o.file == "" {
		fatal(fmt.Errorf(`usage: ptln crank --file <backlog.txt> [--thread <id>] [--max N] [--max-tokens N] [--halt-on-fail K] [--timeout 20m] [--allow-bash] [--model <m>] [--engine <e>] [--resume]
   or: ptln crank --claim --run <id> [--workers N] [flags]   (fleet mode: claim tasks from the run store)`))
	}
	if o.claim && o.run == "" {
		fatal(fmt.Errorf("claim mode needs a run id (--run <uuid> or PARTYLINE_RUN_ID)"))
	}
	// #213 (no silent caps): --max is a file-mode brake, ignored in claim mode (the fleet works the
	// whole run). Say so rather than silently dropping it.
	if o.claim && o.max > 0 {
		fmt.Fprintf(os.Stderr, "  (note: --max is ignored in claim mode — the fleet works the whole run; use --max-tokens for a spend brake)\n")
	}
	// File mode parses the worklist up front; claim mode discovers tasks by claiming them.
	var tasks []string
	if !o.claim {
		var err error
		tasks, err = parseTasks(o.file)
		if err != nil {
			fatal(err)
		}
		if len(tasks) == 0 {
			fmt.Println("no tasks in the file (lines; blank and # ignored)")
			return
		}
		// #76 task-authoring aid: nudge (never block) toward an executable acceptance criterion per task.
		warnTasksMissingAcceptanceCue(tasks)
	}
	// The effective engine must exist in the registry AND on this machine before any task runs.
	engineSpec, engineOK := engineSpecFor(o.engine)
	if !engineOK {
		fatal(fmt.Errorf("unknown engine %q — valid: claude, codex, gemini, antigravity", o.engine))
	}
	if _, err := exec.LookPath(engineSpec.Bin); err != nil {
		fatal(fmt.Errorf("%s not found on PATH — the worker runs it headless", engineSpec.Bin))
	}
	dir, _ := os.Getwd()
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		fatal(fmt.Errorf("crank runs inside a git repository (each item gets its own worktree): %w", err))
	}
	if o.thread == "" {
		o.thread = loadRepoBind(dir)
	}
	// O.5 token ceiling: the flag wins; PARTYLINE_MAX_TOKENS is the env fallback so the daemon/run
	// path can set it later. 0 = unbounded (default) — today's behavior, unchanged when unset.
	if o.maxTokens == 0 {
		if v := strings.TrimSpace(os.Getenv("PARTYLINE_MAX_TOKENS")); v != "" {
			fmt.Sscanf(v, "%d", &o.maxTokens)
		}
	}

	// #77 slice 2: claim/fleet mode. Resume is INHERENT — a re-launched crank claims only what's
	// still queued (done/failed tasks aren't re-claimable), so --resume/resumeSkip don't apply.
	if o.claim {
		runCrankClaim(repo, o)
		return
	}

	// #81 slice 3a: --resume skips tasks already `done` in the run store so a resumed (previously
	// paused, slice 2) run doesn't redo finished work. Needs a run id (the per-task store is keyed
	// by it). Best-effort — a read failure runs the FULL list; resume is never fatal. --restart is
	// the opposite intent (start over), so it deliberately skips this: no skip set, no handles — the
	// full worklist runs and realTaskExec rebuilds each worktree fresh.
	if o.resume && !o.restart && o.run != "" {
		o.resumeSkip, o.resumeHandles, o.resumeFindings = resumeStore(o.run)
	}

	runCrank(repo, tasks, o)
}

// resumeStore reads the run's per-task rows and returns (a) the set of original indices already
// `done` (to skip on a --resume) and (b) idx → resume handle for not-done tasks that carry an
// engine session id (Slice 2: resume-IN-PLACE — continue the interrupted task from its stored
// context instead of restarting it). Reuses the daemon's env credentials (token + base) exactly
// like newRunReporter. Best-effort: any missing credential or read error logs and returns nils
// (skip nothing + no handles → the full list runs fresh), so --resume can never abort a run.
func resumeStore(runID string) (skip map[int]bool, handles map[int]string, findings map[int]string) {
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	if token == "" {
		fmt.Fprintf(os.Stderr, "  (--resume: no device token in env — running the full list)\n")
		return nil, nil, nil
	}
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}
	rows, err := api.ListRunTasks(base, token, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (--resume: couldn't read run tasks: %v — running the full list)\n", err)
		return nil, nil, nil
	}
	skip = map[int]bool{}
	handles = map[int]string{}
	findings = map[int]string{}
	for _, r := range rows {
		if r.Status == "done" {
			skip[r.Idx] = true
			continue
		}
		if r.ResumeHandle != "" {
			handles[r.Idx] = r.ResumeHandle // a not-done task with a captured session → resume-in-place
		}
		if (r.Status == "blocked" || r.Status == "failed") && strings.TrimSpace(r.Detail) != "" {
			findings[r.Idx] = strings.TrimSpace(r.Detail) // the reviewer's verdict — fed to the retry
		}
	}
	msg := fmt.Sprintf("↻ resuming: skipping %d already-done task(s)", len(skip))
	if len(handles) > 0 {
		msg += fmt.Sprintf(", continuing %d in-place", len(handles))
	}
	if len(findings) > 0 {
		msg += fmt.Sprintf(", %d with reviewer findings to fix", len(findings))
	}
	fmt.Fprintln(os.Stderr, msg)
	return skip, handles, findings
}

// readRoutesFile reads the daemon-written --visual-routes DATA file: one app path per line, blank
// lines and #-comments skipped. Best-effort — a missing/unreadable file yields nil (the preset
// falls back to its default route). The routes are DATA the visual preset screenshots; the caller
// re-validates them with safeVisualRoutes before use (a route is never executed).
func readRoutesFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var routes []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		routes = append(routes, line)
	}
	return routes
}

// parseTasks reads a backlog file: one task per line, blank lines and #-comments skipped.
func parseTasks(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var tasks []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tasks = append(tasks, line)
	}
	return tasks, sc.Err()
}

// crankShouldHalt is the stop-condition decision (unit-tested): stop before the next task when
// we've hit the item cap, the token ceiling (O.5), or K consecutive failures. done = items
// completed so far; consecFails = current streak; usedTokens = worklist token total so far.
func crankShouldHalt(done, consecFails, usedTokens int, o crankOpts) (halt bool, why string) {
	if o.max > 0 && done >= o.max {
		return true, fmt.Sprintf("reached --max %d", o.max)
	}
	if o.maxTokens > 0 && usedTokens >= o.maxTokens {
		return true, fmt.Sprintf("token budget reached (%d/%d)", usedTokens, o.maxTokens)
	}
	if o.haltOnFail > 0 && consecFails >= o.haltOnFail {
		return true, fmt.Sprintf("%d consecutive failures", consecFails)
	}
	return false, ""
}

// budgetPauseExit is crank's exit code for an unattended (daemon) run that hit the token ceiling
// and can't prompt: it means "paused, needs approval" — distinct from a clean stop (0) and a
// failure (non-zero, non-3). The daemon maps it to the `needs_approval` run status (#81 slice 2).
const budgetPauseExit = 3

// verifyPauseExit is crank's exit code for an unattended run that finished but QUARANTINED one or
// more tasks (a verify gate failed — T2). Like budgetPauseExit it means "paused, needs approval,"
// NOT a failure — but distinct so the daemon can give the right reason (verification, not budget).
// Trust · T3: this is the acceptance gate — a verify failure routes the run to a human instead of
// letting it report clean success.
const verifyPauseExit = 4

// maxRepairRounds bounds the in-run repair loop (builder fixes → gate re-reviews). Two rounds
// resolves the common cases — a fixable miss, then a fix-of-the-fix — while a task the gate still
// rejects after two honest attempts is a builder↔reviewer disagreement (ambiguous task, or the
// reviewer is wrong), which no amount of retrying converges: that one goes to a human.
const maxRepairRounds = 2

// rejectedRetryPrompt wraps a task with the verify/review findings from a rejected attempt, telling
// the builder to FIX rather than rebuild. Used in two places: the in-run repair loop (a rejection is
// retried immediately, builder resumed in its live session) and a web Continue on a quarantined run
// (the resumed retry across processes — o.resumeFindings). Same wording so behavior matches.
func rejectedRetryPrompt(findings, task string) string {
	return "A previous attempt at this task was REJECTED by an independent code reviewer. " +
		"The earlier work is already committed on the current branch — FIX the findings below, " +
		"keep what already works, and do not start over blindly.\n\nREVIEWER FINDINGS:\n" + findings +
		"\n\n--- THE ORIGINAL TASK ---\n" + task
}

// rateLimitExit is crank's exit code for an unattended run the model PROVIDER throttled (rate limit)
// before it could finish — "paused, resumable when the quota window resets," not a failure. crank
// self-reports the run to needs_approval with the reset time BEFORE exiting (it has the reset time
// from the worker; the daemon doesn't), so the daemon just leaves the status alone on this code.
const rateLimitExit = 5

// approveMoreTokens sentinels (in addition to a positive "add N tokens" return):
//
//	approveRemoveLimit — lift the ceiling entirely and continue unbounded.
//	approvePauseBudget — can't prompt (no TTY): an unattended run paused at the ceiling.
//	approveUserStop    — the interactive human chose to stop (enter / unparseable input).
const (
	approveRemoveLimit = -1
	approvePauseBudget = -2
	approveUserStop    = 0
)

// approveMoreTokens is the pause-and-approve gate (#80): when the worklist hits the token ceiling
// at a task boundary, an INTERACTIVE run pauses and lets the human extend the budget instead of
// hard-stopping. Returns >0 = add that many tokens · approveRemoveLimit = remove the limit ·
// approveUserStop = the human chose to stop · approvePauseBudget = no TTY (can't prompt). The
// caller distinguishes a non-TTY pause (→ needs_approval on the daemon path) from a deliberate
// user stop; both hard-stop the loop, but only the pause is a "needs approval" signal.
func approveMoreTokens(used, limit int) int {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return approvePauseBudget
	}
	fmt.Fprintf(os.Stderr, "\n⏸  token budget reached (%d/%d).\n", used, limit)
	fmt.Fprintf(os.Stderr, "   approve more?  [enter = stop · <number> = add that many tokens · a = remove the limit] › ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch s := strings.TrimSpace(line); {
	case s == "":
		return approveUserStop
	case s == "a" || s == "all" || s == "remove" || s == "unlimited":
		return approveRemoveLimit
	default:
		n := 0
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			return n
		}
		fmt.Fprintf(os.Stderr, "   (didn't understand %q — stopping)\n", s)
		return approveUserStop
	}
}

// taskExec runs ONE task (its own worktree → worker → commit) and returns the outcome. It's the
// external-work seam: runCrankWith owns the loop, halt logic, and per-task telemetry, so a test
// can drive that logic with a fake exec instead of git + a real `claude` run.
type taskExec func(i int, task string) crankResult

func runCrank(repo string, tasks []string, o crankOpts) {
	logger := newRunLogger(o.run)
	defer logger.close()
	runCrankWith(tasks, o, realTaskExec(repo, o, logger), newRunReporter(o.run))
}

// verifyTimeout bounds each acceptance check (Trust · T2a). Reuse the worker timeout (a build/test
// suite is in the same ballpark as a worker run); default to 10m when unset.
func verifyTimeout(o crankOpts) time.Duration {
	if o.timeout > 0 {
		return o.timeout
	}
	return 10 * time.Minute
}

// quarantinedCount is how many results were QUARANTINED by a verify gate: the worker succeeded but
// verification failed, so the branch wasn't merged (task→blocked). Trust · T3 uses this to decide
// whether a finished run needs human approval.
func quarantinedCount(results []crankResult) int {
	n := 0
	for _, r := range results {
		if r.ok && ((r.verify.ran && !r.verify.ok) || r.noPR) {
			n++
		}
	}
	return n
}

// maybePauseForQuarantine is the acceptance gate (Trust · T3). If verification quarantined any task
// on an UNATTENDED (daemon) run, exit with verifyPauseExit so the daemon lands the run in
// needs_approval — actively routing the quarantined branches to a human — instead of letting it
// report clean `done`. Interactive/local runs (no run id) just show the blocked tasks in the
// summary; there's no daemon to route to. Call AFTER crankSummary.
func maybePauseForQuarantine(results []crankResult, o crankOpts) {
	if o.run == "" {
		return // interactive/local — nothing to route to
	}
	if n := quarantinedCount(results); n > 0 {
		fmt.Fprintf(os.Stderr, "\n⏸ %d task(s) need review (verify failed, or committed with no PR opened) — needs approval\n", n)
		os.Exit(verifyPauseExit)
	}
}

// maybePauseForRateLimit runs BEFORE the quarantine gate: if the model provider throttled us mid-run
// (any task carries a rate-limit reset time), the run didn't fail — it's PAUSED until the quota
// window resets. On an unattended (daemon) run we self-report needs_approval WITH the reset time
// (crank has it; the daemon doesn't) and exit rateLimitExit so the daemon leaves the status alone.
// Engine-neutral: any engine that surfaced a reset time (see parseRateLimit) flows through here.
func maybePauseForRateLimit(results []crankResult, o crankOpts) {
	if o.run == "" {
		return // interactive/local — nothing to route to; the log line already showed the limit
	}
	var reset time.Time
	blocked, note := false, ""
	for _, r := range results {
		if !r.rateLimitResetAt.IsZero() && r.rateLimitResetAt.After(reset) {
			reset = r.rateLimitResetAt
		}
		if r.rateLimited {
			blocked = true
			if note == "" {
				note = r.rateNote
			}
		}
	}
	// THE SECOND GATE that hid entitlement blocks. This used to `return` whenever reset was zero, so
	// even once the parser reported a block, a reset-less one still never reached the web — the run
	// just failed with no explanation. Pause on BLOCKED, not on "we happen to know when it resets".
	if !blocked && reset.IsZero() {
		// Nothing refused us this run: clear any stale note so the tray stops warning about a limit
		// that has since cleared.
		clearRateLimitNote()
		return
	}
	// Local breadcrumb for the tray, which reads a local snapshot rather than polling the API.
	writeRateLimitNote(rateLimitNote{At: time.Now(), ResetAt: reset, Note: note, Run: o.run})
	detail := ""
	switch {
	case !reset.IsZero():
		detail = "⏸ rate limit reached — resets " + reset.Format(time.RFC3339)
	case note != "":
		detail = "⏸ blocked by the provider — " + note
	default:
		detail = "⏸ blocked by the provider — no reset time given, which usually means this model needs usage credits or isn't enabled for your org"
	}
	// crank self-reports (it has the reset time; the daemon's exit-code handler doesn't). Best-effort:
	// reuse the daemon-exposed creds; a missing credential just means the exit code alone routes it.
	// SetRunPaused carries resume_at so the web can offer "resume at reset" (Slice 2).
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	if base != "" && token != "" {
		_ = api.SetRunPaused(base, token, o.run, detail, reset)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", detail)
	os.Exit(rateLimitExit)
}

// realTaskExec is the production per-task worker: a fresh worktree, the worker atom, then a
// commit on its own branch (never a push). Extracted from the loop so runCrankWith is testable.
func realTaskExec(repo string, o crankOpts, logger *runLogger) taskExec {
	return func(i int, task string) crankResult {
		// Unchained: one worktree/branch per task. CHAINED: every task in every member of the chain
		// uses the SAME branch, so step N opens the files step N-1 just edited (gitwt.create reuses an
		// existing worktree whose branch matches) — no forking from origin/<default> and re-colliding.
		name := fmt.Sprintf("crank-%02d-%s", i+1, gitwt.FlatSlug(firstWords(task, 4)))
		if o.branch != "" {
			name = o.branch
		}
		// Slice 2 resume-in-place: continue this task from its captured engine session ONLY when BOTH
		// the handle (from the run store) AND its partial-work worktree still exist. If the worktree
		// was pruned, the session's remembered edits wouldn't be on disk — so fall back to a fresh
		// run (empty handle) rather than resume into an inconsistent tree.
		resume := ""
		if h := o.resumeHandles[i]; h != "" && gitwt.IsLinkedWorktree(gitwt.Path(repo, name)) {
			resume = h
		}
		// "Restart" CTA: wipe any prior attempt's worktree AND its branch so this task starts clean
		// off origin/<default> instead of building on stale commits. Best-effort — a missing worktree/
		// branch just no-ops, and Create then makes them fresh. (o.restart already suppressed resume.)
		if o.restart {
			if p := gitwt.Path(repo, name); gitwt.IsLinkedWorktree(p) {
				_ = gitwt.Remove(repo, p)
			}
			_ = gitwt.DeleteBranch(repo, gitwt.Slug(name))
		}
		// The project's configured base branch (--base) is the fork point for NEW branches — the SAME
		// ref applyMergePolicy targets below. Forking from one branch and opening the PR into another
		// would fill the PR with every commit that differs between them, so both read this one value.
		// A base that isn't on origin FAILS the task rather than silently rooting at origin/<default>:
		// a branch built on the wrong base becomes a PR against the wrong target. Empty → gitwt's
		// origin/<default> (the pre-setting behavior). Existing branches win over the base either way,
		// which is what lets a chain's members 2..N continue member 1's branch.
		baseRef := ""
		if o.base != "" {
			ref, berr := gitwt.RemoteBase(repo, o.base)
			if berr != nil {
				return crankResult{task: task, branch: name, ok: false, note: "base branch: " + berr.Error()}
			}
			baseRef = ref
		}
		wtPath, branch, err := gitwt.CreateFrom(repo, name, baseRef) // reuses the existing worktree if present (see gitwt.create)
		if err != nil {
			return crankResult{task: task, branch: name, ok: false, note: "worktree: " + err.Error()}
		}
		_ = gitwt.SeedInclude(repo, wtPath)
		// Phase B3: inject the project globals into THIS worktree as CLAUDE.md (the worker's cwd), so it
		// reads the project's rules/stack/guardrails natively. Written here (not the repo root) because
		// the worktree only sees tracked / SeedInclude'd files. No-op when the run carried no globals.
		writeWorktreeGlobals(wtPath, o.globals)
		// Org skill library: inject the run's enabled skills so ANY engine can use them — claude reads
		// .claude/skills, everything else .agents/skills. Best-effort per skill (bad names skipped);
		// the injected dirs are git-excluded in the worktree so the worker never commits them.
		_ = gitwt.MaterializeSkills(wtPath, loadGitwtSkills(o.skills, o.skillsDir))
		// crank-01: live step output. A non-nil sink (run id + device token present) streams the
		// worker's stdout into run_logs as it works; nil → the buffered path, unchanged.
		if resume != "" {
			logger.note(i, "step", "↻ resuming "+firstWords(task, 12))
		} else {
			logger.note(i, "step", "▶ "+firstWords(task, 12))
		}
		out, werr := runWorker(wtPath, task, o.engine, o.model, o.thread, o.allowBash, o.timeout, logger.sink(i), resume)
		// Acceptance: "cleanly restarts if the session is gone." The worktree-exists check above covers a
		// PRUNED worktree; this covers the other half — the worktree is present but the engine REJECTED
		// the resume handle (the session expired / was cleaned up server-side, common after a 5-hour
		// rate-limit wait). Signal: a resume attempt that errored WITHOUT the engine ever emitting a
		// session frame (out.resumeHandle == "") and that wasn't a rate-limit pause — i.e. `claude
		// --resume <id>` bounced at launch, so no work was done this attempt. Retry the task ONCE fresh
		// in the SAME worktree: the prior partial edits are real files still on disk, so the fresh run
		// loses only the conversational context, not the work. A resume that DID get going (handle
		// present) or hit the rate limit is left alone — retrying those would discard real progress.
		if resume != "" && werr != nil && out.resumeHandle == "" && out.rateReset.IsZero() {
			logger.note(i, "step", "↻ resume rejected (session gone) — restarting this task fresh")
			fmt.Fprintf(os.Stderr, "  (task %d: resume handle rejected — session gone; restarting fresh in the same worktree)\n", i+1)
			out, werr = runWorker(wtPath, task, o.engine, o.model, o.thread, o.allowBash, o.timeout, logger.sink(i), "")
		}
		// #263: keep the worker's own summary (workerPrompt asks it to end with "what I changed +
		// what a reviewer should check") so the run history is legible — this used to be discarded.
		r := crankResult{task: task, branch: branch, ok: werr == nil, tokens: out.tokens, summary: strings.TrimSpace(out.text), rateLimitResetAt: out.rateReset, rateLimited: out.rateBlocked, rateNote: out.rateNote, resumeHandle: out.resumeHandle, invokedSkills: out.invokedSkills}
		if werr != nil {
			r.note = werr.Error()
		} else if !r.rateLimitResetAt.IsZero() {
			// Slice 2: the provider throttled us mid-task. Do NOT commit/verify/merge partial work as
			// if it were finished — leave the edits in the worktree so a resume continues in-place. The
			// task is reported `blocked` (not done) and the run pauses with the reset time (below).
			r.note = "⏸ rate limit reached — partial work left for resume"
		} else if o.commit {
			title := "crank: " + firstWords(task, 10)
			r.note = commitWorktree(wtPath, title)
			// Deliverability is a property of the BRANCH, not of who committed. The old gate
			// (`r.note == "committed"`) only passed when CRANK's own commit landed — but a worker
			// often commits its work itself, so commitWorktree saw a clean tree, said "no changes",
			// and a fully-built branch was silently stranded: never verified, never pushed, no PR,
			// while the task still reported done. Upgrade the note when the branch is ahead of the
			// base so agent-committed work flows into the same verify+merge path.
			if r.note == "no changes" && branchAhead(wtPath) > 0 {
				r.note = "committed (by agent)"
			}
			// #77 slice 3: only a branch with commits to deliver goes to push/PR/merge. push/pr/merge
			// operate on the shared repo (the branch ref lives in the main .git, not the worktree).
			if strings.HasPrefix(r.note, "committed") {
				// Trust · T2: VERIFY before merge — three layers. T2a: the project's executable
				// acceptance checks (.partyline/verify). T2b: an independent adversarial reviewer
				// of the diff (.partyline/review). T2d: a vision reviewer that renders the changed
				// UI and looks at it (.partyline/visual — see visual.go). Pass (or none enabled) →
				// the branch is eligible to merge per policy. Any fails → QUARANTINE: skip the
				// merge, leave the branch for a human, carry the reasons (runCrankWith flips the
				// task to `blocked`, not `done`).
				r.verify = verifyTask(repo, wtPath, task, o.engine, verifyTimeout(o), visualCfg{on: o.visual, routes: o.visualRoutes})
				// T2d: a toggle-on-but-no-renderer case WARNS (surfaced on the task note) rather than
				// failing the run or executing anything web-supplied.
				if r.verify.warn != "" {
					r.note += " · " + r.verify.warn
				}
				// The repair loop: a rejected task goes BACK to the builder before it goes to a
				// human. The builder resumes in its live engine session — it still has the full
				// task context — with the gate's findings in front of it, fixes them on the same
				// branch, and the FULL gate re-runs (checks, reviewer, visual). The reviewer stays
				// tool-less and independent: it criticizes, the builder repairs, roles never mix.
				// Bounded: unbounded builder↔reviewer ping-pong burns tokens, and a genuine
				// disagreement (ambiguous task, wrong reviewer) never converges — after
				// maxRepairRounds the task quarantines WITH its attempt history, and that is the
				// moment a human is legitimately needed.
				repairs := 0
				for round := 1; round <= maxRepairRounds && r.verify.ran && !r.verify.ok; round++ {
					logger.note(i, "step", fmt.Sprintf("🛠 verify rejected — auto-repair %d/%d", round, maxRepairRounds))
					fmt.Fprintf(os.Stderr, "  ✗ verify failed — auto-repair round %d/%d\n", round, maxRepairRounds)
					ahead := branchAhead(wtPath)
					rout, rerr := runWorker(wtPath, rejectedRetryPrompt(r.verify.reasons, task), o.engine, o.model, o.thread, o.allowBash, o.timeout, logger.sink(i), r.resumeHandle)
					r.tokens += rout.tokens
					if rout.resumeHandle != "" {
						r.resumeHandle = rout.resumeHandle
					}
					if !rout.rateReset.IsZero() {
						// Throttled mid-repair: same posture as a throttled build — leave the
						// edits for a resume, pause the run with the reset time. The blocked
						// task keeps its findings, so the resumed retry is findings-aware.
						r.rateLimitResetAt, r.rateLimited, r.rateNote = rout.rateReset, rout.rateBlocked, rout.rateNote
						r.note += " · ⏸ rate limit reached during auto-repair"
						break
					}
					if rerr != nil {
						r.note += fmt.Sprintf(" · auto-repair round %d errored", round)
						break
					}
					if c := commitWorktree(wtPath, "crank: fix review findings ("+firstWords(task, 8)+")"); c == "no changes" && branchAhead(wtPath) == ahead {
						// The builder changed nothing — re-reviewing the same diff can only
						// return the same verdict. Stop and let a human break the tie.
						r.note += " · auto-repair made no changes"
						break
					}
					repairs = round
					r.verify = verifyTask(repo, wtPath, task, o.engine, verifyTimeout(o), visualCfg{on: o.visual, routes: o.visualRoutes})
				}
				if repairs > 0 && r.verify.ran && r.verify.ok {
					logger.note(i, "step", fmt.Sprintf("✓ repaired — verify gate passes (round %d)", repairs))
					fmt.Fprintf(os.Stderr, "  ✓ repaired — verify gate now passes\n")
				} else if repairs > 0 {
					r.verify.reasons = fmt.Sprintf("(auto-repair: the builder retried %d time(s); the gate still rejects)\n%s", repairs, r.verify.reasons)
				}
				if r.verify.ran && !r.verify.ok {
					r.note += " · verify failed (quarantined, not merged)"
				} else {
					// Only the GitHub path uses a minted App token; gitlab/bitbucket push over SSH and never
					// call gh, so skip the (would-be 404) token fetch for them.
					tok := ""
					if o.gitProvider == "" || o.gitProvider == "github" {
						tok = mergeGitHubToken(o.run)
					}
					note, prURL := applyMergePolicy(realRunner(repo, tok), branch, title, o.mergePolicy, o.gitProvider, o.base)
					if note != "" {
						r.note += " · " + note
					}
					r.prURL = prURL // #212: surfaced on the run task in the web
					// A pr/auto task that committed but opened NO PR (push or `gh pr create` failed —
					// e.g. gh not authed for this repo on the daemon's machine) must NOT report clean
					// success: the branch is silently orphaned. Flag it so the acceptance gate routes
					// the run to needs_approval and it surfaces in Review with the reason (the note).
					if (o.mergePolicy == "pr" || o.mergePolicy == "auto") && prURL == "" {
						r.noPR = true
					}
				}
			}
		}
		// O.5: a token ceiling that can't see this task's usage is a blind spot — make it visible.
		if o.maxTokens > 0 && r.tokens == 0 {
			fmt.Fprintf(os.Stderr, "  (no token usage reported for this task — the ceiling can't account for it)\n")
		}
		return r
	}
}

// runCrankWith drives the worklist loop and (when a run reporter is live) self-reports each
// task's lifecycle: `queued` for the whole list up front, `running` before each attempt, and
// `done`/`failed` after — with the branch + note. Reporting is best-effort telemetry: a failed
// POST logs and the run continues (see newRunReporter). The loop/halt logic is unchanged.
func runCrankWith(tasks []string, o crankOpts, exec taskExec, report runReporter) {
	for i, task := range tasks {
		// #81 slice 3a: on a --resume, an already-`done` task keeps its stored state — don't
		// re-queue it (that would regress `done` → `queued` in the store).
		if o.resumeSkip[i] {
			continue
		}
		report.emitQueued(i, task)
	}
	var results []crankResult
	consecFails := 0
	usedTokens := 0
loop:
	for i, task := range tasks {
		// #81 slice 3a: skip tasks already `done` (resume). `i` stays the ORIGINAL backlog index,
		// so every emit/log below still aligns with run_tasks (3b + telemetry stay consistent).
		// Skipped tasks contribute nothing to the token total or the failure streak.
		if o.resumeSkip[i] {
			continue
		}
		// Token ceiling: pause-and-approve (#80) BEFORE the hard-halt check. Interactive → let the
		// human add budget or lift the limit and continue in-process (state intact, no re-run);
		// non-interactive → approveMoreTokens returns 0 and we hard-stop as before.
		if o.maxTokens > 0 && usedTokens >= o.maxTokens {
			switch add := approveMoreTokens(usedTokens, o.maxTokens); {
			case add == approveRemoveLimit:
				o.maxTokens = 0
				fmt.Fprintf(os.Stderr, "   ↻ limit removed — continuing unbounded.\n")
			case add > 0:
				o.maxTokens += add
				fmt.Fprintf(os.Stderr, "   ↻ +%d approved — ceiling now %d, continuing.\n", add, o.maxTokens)
			case add == approvePauseBudget && o.run != "":
				// Unattended daemon run (no TTY, has a run id): can't prompt in-process, so signal a
				// PAUSE — the daemon maps budgetPauseExit → `needs_approval` and notifies the operator,
				// who approves more or stops (slice 3). Distinct from a user-chosen stop (clean exit).
				fmt.Fprintf(os.Stderr, "\n⏸ paused: token budget reached (%d/%d) — needs approval (%d/%d tasks attempted)\n", usedTokens, o.maxTokens, i, len(tasks))
				crankSummary(results, len(tasks))
				os.Exit(budgetPauseExit)
			default:
				// A deliberate interactive stop, or a non-daemon non-TTY run — hard-stop cleanly (exit 0),
				// unchanged from before.
				fmt.Fprintf(os.Stderr, "\n■ stopping: token budget reached (%d/%d) (%d/%d tasks attempted)\n", usedTokens, o.maxTokens, i, len(tasks))
				break loop
			}
		}
		if halt, why := crankShouldHalt(i, consecFails, usedTokens, o); halt {
			fmt.Fprintf(os.Stderr, "\n■ stopping: %s (%d/%d tasks attempted)\n", why, i, len(tasks))
			break loop
		}
		fmt.Fprintf(os.Stderr, "\n▶ [%d/%d] %s\n", i+1, len(tasks), task)
		report.emitRunning(i, task)
		// Close the review loop: a task whose last attempt was rejected re-runs WITH the reviewer's
		// findings in front of it. Prompt-only — `task` itself (emits, logs, the stored worklist)
		// stays the original, so nothing downstream sees a mutated task string.
		execTask := task
		if f := o.resumeFindings[i]; f != "" {
			execTask = rejectedRetryPrompt(f, task)
		}
		started := time.Now()
		r := exec(i, execTask)
		r.durationMs = int(time.Since(started).Milliseconds()) // #263: how long the task took
		results = append(results, r)
		usedTokens += r.tokens
		// Slice 2: the provider throttled us mid-task — NOT done, NOT a failure. Report `blocked` so a
		// later resume re-attempts THIS task (resume-in-place via its stored handle) instead of
		// skipping it as done, then stop the loop: the whole run is throttled and the next task would
		// only hit the same wall. maybePauseForRateLimit (after the loop) pauses with the reset time.
		if !r.rateLimitResetAt.IsZero() {
			report.emitResult(i, "blocked", r)
			break loop
		}
		if r.ok {
			consecFails = 0
			// Trust · T2a: a worker success whose acceptance checks FAILED is quarantined — report
			// it `blocked` (needs a human), not `done`. A quarantine isn't a crash, so it doesn't
			// count toward the consecutive-failure halt.
			if (r.verify.ran && !r.verify.ok) || r.noPR {
				report.emitResult(i, "blocked", r)
			} else {
				report.emitResult(i, "done", r)
			}
		} else {
			consecFails++
			report.emitResult(i, "failed", r)
		}
	}
	crankSummary(results, len(tasks))
	reportInvokedSkills(report, o, results) // skill-invocation telemetry (best-effort)
	maybePauseForRateLimit(results, o)      // provider throttled us → pause with the reset time (precedence)
	maybePauseForQuarantine(results, o)     // Trust · T3: route verify failures to a human
}

// runReporter posts per-task lifecycle events to the run store (O.3). post is nil when there's
// no run id / no credentials, making every emit a no-op — self-reporting is pure telemetry and
// must never affect the run. The daemon passes the run id (--run) + device token + base to the
// crank child; a POST failure is logged and swallowed inside post.
type runReporter struct {
	post func(tr api.RunTaskUpdate)
	// reportInvoked flips a run's injected skill-usage rows to invoked=true (best-effort telemetry).
	// nil when there's no run id / credentials — same no-op posture as post.
	reportInvoked func(invoked []api.SkillRef)
}

// emitQueued/emitRunning report the lifecycle-only transitions (no result yet). emitResult
// reports the terminal state with the full per-task detail (#263: summary, tokens, duration).
func (r runReporter) emitQueued(idx int, task string) {
	r.emit(api.RunTaskUpdate{Idx: idx, Task: task, Status: "queued"})
}

func (r runReporter) emitRunning(idx int, task string) {
	r.emit(api.RunTaskUpdate{Idx: idx, Task: task, Status: "running"})
}

func (r runReporter) emitResult(idx int, status string, cr crankResult) {
	// Trust · T2a: fold the verify verdict in. On a quarantine, the failure reasons ARE the
	// actionable detail a human needs, so they take the detail slot; the verdict itself
	// (pass/fail) rides to the tamper-evident ledger via Verified.
	detail, verified := cr.note, ""
	if cr.verify.ran {
		if cr.verify.ok {
			verified = "pass"
		} else {
			verified = "fail"
			if cr.verify.reasons != "" {
				detail = cr.verify.reasons
			}
		}
	}
	r.emit(api.RunTaskUpdate{
		Idx: idx, Task: cr.task, Status: status, Branch: cr.branch, Detail: detail,
		PRURL: cr.prURL, Summary: cr.summary, Tokens: cr.tokens, DurationMs: cr.durationMs,
		Verified: verified, ResumeHandle: cr.resumeHandle,
	})
}

func (r runReporter) emit(tr api.RunTaskUpdate) {
	if r.post == nil {
		return
	}
	r.post(tr)
}

// newRunReporter wires a live reporter when crank was given a run id AND the daemon exposed the
// device token via env (PARTYLINE_DAEMON_TOKEN + PARTYLINE_API base). Missing either → a no-op
// reporter (crank still runs the worklist; it just doesn't self-report). The token is trimmed —
// a trailing newline in a secret is a silent auth failure.
func newRunReporter(runID string) runReporter {
	if runID == "" {
		return runReporter{}
	}
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	if token == "" {
		return runReporter{}
	}
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}
	// TRUST · T1: seed this daemon's hash chain from its stored head so a --resume (or relaunched
	// worker) continues the chain instead of colliding at seq 0. Best-effort — a fresh chain (0, "")
	// on error is correct for a first run and self-heals once the head route answers.
	chain := &chainState{}
	if seq, hash, err := api.LastRunEvent(base, token, runID); err == nil {
		chain.seq, chain.lastHash = seq, hash
	}
	return runReporter{post: func(tr api.RunTaskUpdate) {
		if err := api.UpsertRunTask(base, token, runID, tr); err != nil {
			fmt.Fprintf(os.Stderr, "  (run-task telemetry idx %d %s: %v)\n", tr.Idx, tr.Status, err)
		}
		// Append the same transition to the tamper-evident ledger. Independent of the projection
		// upsert above and equally best-effort: advance the chain head only on success, so a
		// dropped append leaves a gap-free chain (the lost transition is just absent) rather than
		// poisoning every later append with a seq gap.
		ev := chain.build(tr)
		if err := api.AppendRunEvent(base, token, runID, ev); err != nil {
			fmt.Fprintf(os.Stderr, "  (run-event ledger idx %d %s: %v)\n", tr.Idx, tr.Status, err)
		} else {
			chain.commit(ev)
		}
	}, reportInvoked: func(invoked []api.SkillRef) {
		if err := api.ReportSkillInvocation(base, token, runID, invoked); err != nil {
			fmt.Fprintf(os.Stderr, "  (skill-invocation telemetry: %v)\n", err)
		}
	}}
}

// reportInvokedSkills unions the skills the agent USED across a run's tasks and flips their usage rows
// to invoked=true. Best-effort telemetry: a nil reporter (no run id / credentials) or an empty set is a
// clean no-op, and a report failure logs without touching the run. Versions come from the run's staged
// skill set (o.skills); an unknown name backstops to 0 (the server matches by name on the flip).
func reportInvokedSkills(report runReporter, o crankOpts, results []crankResult) {
	if report.reportInvoked == nil {
		return
	}
	used := map[string]bool{}
	for _, r := range results {
		for _, n := range r.invokedSkills {
			used[n] = true
		}
	}
	if len(used) == 0 {
		return
	}
	verByName := map[string]int{}
	for _, s := range o.skills {
		verByName[s.Name] = s.Version
	}
	refs := make([]api.SkillRef, 0, len(used))
	for n := range used {
		refs = append(refs, api.SkillRef{Name: n, Version: verByName[n]})
	}
	report.reportInvoked(refs)
}

// ---- #77 slice 2: claim/fleet mode ----
//
// Instead of walking a static worklist, crank CLAIMS tasks from the run store one at a time
// (server-side FOR UPDATE SKIP LOCKED, slice 1), so N workers — here AND on other org machines
// pointed at the same run — chew one backlog concurrently without two ever taking the same task.
// The daemon seeds the run's tasks (queued) before launching; the claim itself flips a task to
// `running` server-side, so a worker only reports the terminal done/failed (+ branch). Resume is
// inherent: a re-launched crank claims only what's still queued. NOTE: --max is a file-mode brake
// and is ignored here; the claim-mode brakes are the token ceiling (soft, may overshoot by up to
// `workers` in-flight tasks) and halt-on-fail.

// claimFn returns the next claimed task, nil when the pool is drained, or an error. It's the
// network seam so runClaimPass is testable without a server.
type claimFn func() (*api.ClaimedTask, error)

// claimCreds reads the device token + API base crank uses to claim + report (the same env the
// daemon injects for run reporting). Token trimmed — a trailing newline in a secret silently fails auth.
func claimCreds() (base, token string) {
	token = strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	base = strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}
	return base, token
}

// claimWorkers is the default concurrency when --workers wasn't given: the daemon's
// PARTYLINE_CRANK_WORKERS env (an operator sets fleet width per machine), else 1. Capped at 16 to
// bound concurrent `claude` subprocesses on one box.
func claimWorkers() int {
	n := 1
	if v := strings.TrimSpace(os.Getenv("PARTYLINE_CRANK_WORKERS")); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	return n
}

func runCrankClaim(repo string, o crankOpts) {
	base, token := claimCreds()
	if token == "" {
		fatal(fmt.Errorf("claim mode needs a device token (PARTYLINE_DAEMON_TOKEN) — run via the daemon, or export it for a manual claimer"))
	}
	if o.workers < 1 {
		o.workers = claimWorkers()
	}
	logger := newRunLogger(o.run)
	defer logger.close()
	exec := realTaskExec(repo, o, logger)
	report := newRunReporter(o.run)
	// #213: lease the claim for longer than a task can run, so a slow-but-alive worker's task is
	// never reclaimed + double-run. timeout + 30min margin (server clamps to a sane max).
	leaseSeconds := int(o.timeout.Seconds()) + 1800
	claim := func() (*api.ClaimedTask, error) { return api.ClaimNextTask(base, token, o.run, leaseSeconds) }
	fmt.Fprintf(os.Stderr, "⇄ claim mode: run %s · %d worker(s)\n", o.run, o.workers)
	runClaimLoop(claim, o, exec, report)
}

// runClaimLoop runs the worker pool, and on a token-ceiling pause decides whether to resume
// (interactive: add/remove budget → relaunch — the pool naturally re-claims only what's still
// queued) or exit for the daemon to surface as needs_approval (#81). Seams are injected so a test
// can drive it without network or git.
func runClaimLoop(claim claimFn, o crankOpts, exec taskExec, report runReporter) {
	var used int64
	var all []crankResult
	for {
		ceilingHit, batch := runClaimPass(claim, o, exec, report, &used)
		all = append(all, batch...)
		if !ceilingHit {
			break
		}
		switch add := approveMoreTokens(int(used), o.maxTokens); {
		case add == approveRemoveLimit:
			o.maxTokens = 0
			fmt.Fprintf(os.Stderr, "   ↻ limit removed — continuing unbounded.\n")
		case add > 0:
			o.maxTokens += add
			fmt.Fprintf(os.Stderr, "   ↻ +%d approved — ceiling now %d, continuing.\n", add, o.maxTokens)
		case add == approvePauseBudget && o.run != "":
			// Unattended daemon run: signal PAUSE — the daemon maps budgetPauseExit → needs_approval
			// and notifies the operator (#81). On approval the daemon re-invokes crank --claim, which
			// resumes inherently (claims only still-queued tasks).
			fmt.Fprintf(os.Stderr, "\n⏸ paused: token budget reached (%d/%d) — needs approval\n", used, o.maxTokens)
			crankSummary(all, len(all))
			os.Exit(budgetPauseExit)
		default:
			fmt.Fprintf(os.Stderr, "\n■ stopping: token budget reached (%d/%d)\n", used, o.maxTokens)
			crankSummary(all, len(all))
			return
		}
	}
	crankSummary(all, len(all))
	reportInvokedSkills(report, o, all) // skill-invocation telemetry (best-effort)
	maybePauseForRateLimit(all, o)      // provider throttled us → pause with the reset time (precedence)
	maybePauseForQuarantine(all, o)     // Trust · T3: route verify failures to a human
}

// runClaimPass runs one worker-pool pass: `workers` goroutines each loop claim → exec (own
// worktree) → report until the pool drains or a brake trips. Returns whether the token ceiling
// was hit (so the caller decides pause/resume) and the results collected this pass. usedTokens is
// shared (by pointer) so it accumulates across ceiling-resume passes.
func runClaimPass(claim claimFn, o crankOpts, exec taskExec, report runReporter, usedTokens *int64) (ceilingHit bool, results []crankResult) {
	workers := o.workers
	if workers < 1 {
		workers = 1
	}
	var (
		mu      sync.Mutex
		stop    atomic.Bool
		ceiling atomic.Bool
		consec  int64
		wg      sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if stop.Load() {
					return
				}
				// Soft token ceiling: check the shared total before taking more work. With N
				// workers the total can overshoot by up to N in-flight tasks — acceptable for a
				// crude spend brake.
				if o.maxTokens > 0 && atomic.LoadInt64(usedTokens) >= int64(o.maxTokens) {
					ceiling.Store(true)
					stop.Store(true)
					return
				}
				ct, err := claim()
				if err != nil {
					fmt.Fprintf(os.Stderr, "  (claim error: %v — worker stopping)\n", err)
					return
				}
				if ct == nil {
					return // pool drained
				}
				fmt.Fprintf(os.Stderr, "\n▶ [task %d] %s\n", ct.Idx+1, ct.Task)
				started := time.Now()
				r := exec(ct.Idx, ct.Task)
				r.durationMs = int(time.Since(started).Milliseconds()) // #263
				atomic.AddInt64(usedTokens, int64(r.tokens))
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
				if r.ok {
					atomic.StoreInt64(&consec, 0)
					// Trust · T2a: quarantine a worker success whose acceptance checks failed
					// (report `blocked`, not `done`) — same as the file path; not a crash, so it
					// doesn't count toward the consecutive-failure halt.
					if (r.verify.ran && !r.verify.ok) || r.noPR {
						report.emitResult(ct.Idx, "blocked", r)
					} else {
						report.emitResult(ct.Idx, "done", r)
					}
				} else {
					n := atomic.AddInt64(&consec, 1)
					report.emitResult(ct.Idx, "failed", r)
					if o.haltOnFail > 0 && int(n) >= o.haltOnFail {
						fmt.Fprintf(os.Stderr, "\n■ stopping: %d consecutive failures\n", n)
						stop.Store(true)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	return ceiling.Load(), results
}

// branchAhead counts commits on the worktree's HEAD that are not on the remote default branch
// (origin/<default>) — the "is there anything to deliver?" test, independent of WHO made the
// commits (crank or the worker agent itself). 0 on any git failure, so a repo with no origin or
// no origin/HEAD falls back to the old commit-note-only gate — fail-safe toward not pushing.
func branchAhead(dir string) int {
	head, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	base := strings.TrimSpace(string(head))
	if err != nil || !strings.HasPrefix(base, "origin/") {
		return 0
	}
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", base+"..HEAD").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// commitWorktree stages + commits the worker's changes on ITS branch (never pushes, never
// touches another branch). Returns a human note. No changes → nothing to commit.
func commitWorktree(dir, msg string) string {
	if out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); len(strings.TrimSpace(string(out))) == 0 {
		return "no changes"
	}
	_ = exec.Command("git", "-C", dir, "add", "-A").Run()
	cmd := exec.Command("git", "-C", dir, "commit", "-m", msg)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=partyline-worker", "GIT_AUTHOR_EMAIL=worker@partyline.sh",
		"GIT_COMMITTER_NAME=partyline-worker", "GIT_COMMITTER_EMAIL=worker@partyline.sh")
	if err := cmd.Run(); err != nil {
		return "commit failed: " + err.Error()
	}
	return "committed"
}

func crankSummary(results []crankResult, total int) {
	ok := 0
	for _, r := range results {
		if r.ok {
			ok++
		}
	}
	fmt.Printf("\n%s crank done — %d/%d ok, %d attempted of %d in the backlog\n", cgOK+"✓"+cgOff, ok, len(results), len(results), total)
	fmt.Println("  each item is a branch for you to review — nothing was pushed or merged.")
	for _, r := range results {
		mark := cgBad + "✗" + cgOff
		if r.ok {
			mark = cgOK + "✓" + cgOff
		}
		fmt.Printf("  %s %-28s %s%s%s\n", mark, r.branch, cgDim, r.note, cgOff)
	}
	fmt.Printf("\n  review:  ptln wt        (list) · cd <repo>--<branch> && git log -1 && git diff HEAD~1\n")
	fmt.Printf("  discard: ptln wt rm <branch>\n")
}

// firstWords returns the first n words of s — for branch/commit naming.
func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}
