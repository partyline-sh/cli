package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
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
	file        string
	thread      string
	run         string // O.3: run id (UUID) — when set, crank self-reports per-task lifecycle
	max         int    // 0 = all
	maxTokens   int    // O.5: crude token ceiling for the whole worklist; 0 = unbounded (off)
	haltOnFail  int
	timeout     time.Duration
	allowBash   bool
	commit      bool
	resume      bool         // #81 slice 3a: when set (and run != ""), skip tasks already `done` in the run store
	resumeSkip  map[int]bool // original indices to skip this run (built from the run store); nil = skip nothing
	claim       bool         // #77 slice 2: claim tasks from the run store (fleet mode) instead of a static file
	workers     int          // #77 slice 2: concurrent claim-loop workers (claim mode only); <1 → env/default 1
	mergePolicy string       // #77 slice 3: per-task branch handling after commit — manual (default) | pr | auto
}

type crankResult struct {
	task   string
	branch string
	ok     bool
	note   string
	tokens int    // O.5: tokens this task's worker reported (0 = unknown / no usage seen)
	prURL  string // #212: the PR opened by merge_policy pr/auto (empty otherwise)
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
		}
	}
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
		fatal(fmt.Errorf(`usage: ptln crank --file <backlog.txt> [--thread <id>] [--max N] [--max-tokens N] [--halt-on-fail K] [--timeout 20m] [--allow-bash] [--resume]
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
	if _, err := exec.LookPath("claude"); err != nil {
		fatal(fmt.Errorf("claude not found on PATH — the worker runs it headless"))
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
	// by it). Best-effort — a read failure runs the FULL list; resume is never fatal.
	if o.resume && o.run != "" {
		o.resumeSkip = resumeDoneSet(o.run)
	}

	runCrank(repo, tasks, o)
}

// resumeDoneSet reads the run's per-task rows and returns the set of original indices already
// `done` (to skip on a --resume). Reuses the daemon's env credentials (token + base) exactly like
// newRunReporter. Best-effort: any missing credential or read error logs and returns nil (skip
// nothing → the full list runs), so --resume can never abort a run.
func resumeDoneSet(runID string) map[int]bool {
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	if token == "" {
		fmt.Fprintf(os.Stderr, "  (--resume: no device token in env — running the full list)\n")
		return nil
	}
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}
	rows, err := api.ListRunTasks(base, token, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (--resume: couldn't read run tasks: %v — running the full list)\n", err)
		return nil
	}
	skip := map[int]bool{}
	for _, r := range rows {
		if r.Status == "done" {
			skip[r.Idx] = true
		}
	}
	fmt.Fprintf(os.Stderr, "↻ resuming: skipping %d already-done task(s)\n", len(skip))
	return skip
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
	runCrankWith(tasks, o, realTaskExec(repo, o), newRunReporter(o.run))
}

// realTaskExec is the production per-task worker: a fresh worktree, the worker atom, then a
// commit on its own branch (never a push). Extracted from the loop so runCrankWith is testable.
func realTaskExec(repo string, o crankOpts) taskExec {
	return func(i int, task string) crankResult {
		name := fmt.Sprintf("crank-%02d-%s", i+1, gitwt.Slug(firstWords(task, 4)))
		wtPath, branch, err := gitwt.Create(repo, name)
		if err != nil {
			return crankResult{task: task, branch: name, ok: false, note: "worktree: " + err.Error()}
		}
		_ = gitwt.SeedInclude(repo, wtPath)
		_, tokens, werr := runWorker(wtPath, task, "", o.thread, o.allowBash, o.timeout)
		r := crankResult{task: task, branch: branch, ok: werr == nil, tokens: tokens}
		if werr != nil {
			r.note = werr.Error()
		} else if o.commit {
			title := "crank: " + firstWords(task, 10)
			r.note = commitWorktree(wtPath, title)
			// #77 slice 3: only a real commit produces a branch to push/PR/merge. push/pr/merge
			// operate on the shared repo (the branch ref lives in the main .git, not the worktree).
			if r.note == "committed" {
				note, prURL := applyMergePolicy(realRunner(repo), branch, title, o.mergePolicy)
				if note != "" {
					r.note += " · " + note
				}
				r.prURL = prURL // #212: surfaced on the run task in the web
			}
		}
		// O.5: a token ceiling that can't see this task's usage is a blind spot — make it visible.
		if o.maxTokens > 0 && tokens == 0 {
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
		report.emit(i, task, "queued", "", "", "")
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
		report.emit(i, task, "running", "", "", "")
		r := exec(i, task)
		results = append(results, r)
		usedTokens += r.tokens
		if r.ok {
			consecFails = 0
			report.emit(i, r.task, "done", r.branch, r.note, r.prURL)
		} else {
			consecFails++
			report.emit(i, r.task, "failed", r.branch, r.note, r.prURL)
		}
	}
	crankSummary(results, len(tasks))
}

// runReporter posts per-task lifecycle events to the run store (O.3). post is nil when there's
// no run id / no credentials, making every emit a no-op — self-reporting is pure telemetry and
// must never affect the run. The daemon passes the run id (--run) + device token + base to the
// crank child; a POST failure is logged and swallowed inside post.
type runReporter struct {
	post func(idx int, task, status, branch, detail, prURL string)
}

func (r runReporter) emit(idx int, task, status, branch, detail, prURL string) {
	if r.post == nil {
		return
	}
	r.post(idx, task, status, branch, detail, prURL)
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
	return runReporter{post: func(idx int, task, status, branch, detail, prURL string) {
		if err := api.UpsertRunTask(base, token, runID, idx, task, status, branch, detail, prURL); err != nil {
			fmt.Fprintf(os.Stderr, "  (run-task telemetry idx %d %s: %v)\n", idx, status, err)
		}
	}}
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
	exec := realTaskExec(repo, o)
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
				r := exec(ct.Idx, ct.Task)
				atomic.AddInt64(usedTokens, int64(r.tokens))
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
				if r.ok {
					atomic.StoreInt64(&consec, 0)
					report.emit(ct.Idx, r.task, "done", r.branch, r.note, r.prURL)
				} else {
					n := atomic.AddInt64(&consec, 1)
					report.emit(ct.Idx, r.task, "failed", r.branch, r.note, r.prURL)
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
