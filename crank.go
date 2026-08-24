package main

import (
	"bufio"
	"encoding/json"
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
	eng "partyline.sh/partyline/internal/engine"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/surface"
)

// crank.go — E4.8, the worklist loop: drive a backlog of tasks one at a time through the
// worker atom (E4.1), each in ITS OWN worktree, sharing ONE context thread. The brakes are
// the point — this prepares N reviewable branches, it does NOT ship anything:
//
//	ptln crank --file backlog.txt [--thread <id>] [--max N] [--max-tokens N]
//	           [--halt-on-fail K] [--timeout 20m] [--allow-bash] [--no-commit] [--resume]
//	           [--literal]
//
// Each non-blank, non-# line of the file is one task (--literal: every non-blank line, # and
// all). Sequential (not parallel) so each item sees what the previous ones recorded on the
// shared thread — the moat applied to autonomy.
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
	restart        bool   // "Restart" CTA: start the run OVER — fresh worktree+branch per task, ignore prior state
	claim          bool   // #77 slice 2: claim tasks from the run store (fleet mode) instead of a static file
	workers        int    // #77 slice 2: concurrent claim-loop workers (claim mode only); <1 → env/default 1
	mergePolicy    string // #77 slice 3: per-task branch handling after commit — manual (default) | pr | auto
	draft          bool   // --draft: open the PR as a DRAFT (review gate ON) — the human's Accept marks it ready
	// land: --land, the merge train. Verified work goes straight onto the base branch, one task at a
	// time, so later tasks fork from a base containing the earlier ones. OFF by default: this is the
	// only path in crank that writes to the base, and turning it on is a statement that you trust
	// your verify gate more than you value a human in front of every merge.
	land bool

	globals string // Phase B3: the project's globals document, written into each task's worktree as AGENTS.md + CLAUDE.md
	// anchored is what the thread records about the FILES these tasks name, resolved server-side and
	// written into each worktree beside the globals block (its own markers, so neither clobbers the
	// other). Globals are the same for every task; this is specific to the code being touched.
	anchored string
	// acceptance is the task's runnable criteria WITH the direction each must move, resolved by the
	// server from the work items this run came from. Empty = the pre-check is skipped entirely and
	// the loop behaves exactly as it did before.
	acceptance []api.RunAcceptanceCheck
	skills     []api.Skill // org skill library: injected into each task's worktree (.agents/.claude skills) + named in the worker prompt
	skillsDir  string      // the daemon's --skills-dir: holds skills.json + <name>.zip bundles (read at materialize time)
	model      string      // model selection: the build model passed to each task's worker (--model); "" = engine default
	branch     string      // CHAIN branch (--branch): every task in this run builds on THIS branch
	base       string      // BASE branch (--base): the project's configured fork point AND PR target
	baseFB     string      // BASE FALLBACK (--base-fallback): only sent for a STACKED CHAIN member,
	//             whose base is its predecessor's branch rather than an operator setting. That branch
	//             legitimately DISAPPEARS the moment the predecessor's PR merges, and its work is then
	//             in the project base — so falling back is correct, not a guess. Absent for every other
	//             run, which keeps a bad operator-configured --base a loud failure.
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
	// checks and lanes are the project's PIPELINE POLICY (G.6), read from the daemon-written
	// --pipeline DATA file. Policy only: which named checks run and whether they block, and which
	// reviewer engines judge the diff. There is no command and no path in either — the repo's
	// `.partyline/verify` and `.partyline/review` remain the only source of what actually executes,
	// and readPipelineFile re-validates every field rather than trusting the file.
	// Empty = the pre-G.4/G.5 behaviour: every repo check blocking and always-run, one reviewer.
	checks []checkPolicy
	lanes  []reviewLane
	// maxRepairs (--max-repairs, #569) bounds the in-run repair loop for THIS run: 0 quarantines on
	// the first rejection, higher lets the builder try again. Defaults to defaultMaxRepairRounds.
	maxRepairs int
	// literal (--literal) turns OFF #-comment stripping in the worklist: every non-blank line is a
	// task, verbatim. It exists because a task TITLE legitimately starts with "#" — a GitHub issue
	// ref ("#570: fix the loop") is the house style — and a daemon-written worklist is DATA, not a
	// hand-edited file with comments in it. Without this, a run whose every task was issue-titled
	// parsed as all-comments and did nothing while reporting success (chain c842d926). Opt-in and
	// explicit: the daemon always passes it; a human's `ptln crank --file backlog.txt` keeps comments.
	literal bool
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
	tokens           int              // O.5 ceiling signal — Total (incl. cache reads); over-counts, NOT displayed
	freshTokens      int              // DISPLAY spend: input+output (new I/O only; excludes cached context)
	cacheReadTokens  int              // cache_read only — muted "+N cached" detail
	costUSD          float64          // claude's total_cost_usd for this task (0 = not reported)
	prURL            string           // #212: the PR opened by merge_policy pr/auto (empty otherwise)
	summary          string           // #263: the worker's own "what I changed / what to review" summary (run legibility)
	durationMs       int              // #263: wall-clock the task took, in milliseconds (0 = not measured)
	verify           verifyResult     // Trust · T2a: acceptance-check outcome (ran/ok/reasons); zero value = no checks
	noPR             bool             // merge_policy pr/auto committed but opened NO PR (push/gh failed) → route to review, never silent-ship
	rateLimitResetAt time.Time        // when the quota window resets; zero = none GIVEN (not "not blocked")
	rateLimited      bool             // the provider refused this task — see workerOutcome.rateBlocked
	rateNote         string           // the provider's own wording for the block, when it gave us one
	resumeHandle     string           // Slice 2: engine's opaque resume token (Claude session id); "" = restart-only
	invokedSkills    []string         // injected skills the agent USED in this task (claude stream only); unioned + reported per run
	conflicts        []api.PRConflict // Slice A2: REAL merge conflicts vs other open PRs (git merge-tree)
	conflictsChecked bool             // the scan RAN (empty conflicts then means "checked, none" — clears stored state)
}

func crankMain(args []string) {
	o := crankOpts{haltOnFail: 2, timeout: 20 * time.Minute, commit: true, maxRepairs: defaultMaxRepairRounds}
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
		case "--max-repairs":
			// #569: the project's bound on the in-run repair loop. The daemon validates the range before
			// it reaches this argv; clamped here too so a hand-typed value can't ask for a hundred rounds.
			if i++; i < len(args) {
				o.maxRepairs = clampMaxRepairs(args[i], o.maxRepairs)
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
		case "--draft":
			o.draft = true
		case "--land":
			o.land = true
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
		case "--base-fallback":
			// See baseFB. Same shape validation as --base, applied by the daemon before this argv.
			if i+1 < len(args) {
				i++
				o.baseFB = strings.TrimSpace(args[i])
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
		case "--acceptance-file":
			// The daemon writes the run's runnable criteria as JSON and passes the path (same shape as
			// --globals-file / --context-file). DATA: each command is executed in the task's own
			// worktree and is never interpolated into another command or a path.
			if i++; i < len(args) {
				if b, err := os.ReadFile(strings.TrimSpace(args[i])); err == nil {
					_ = json.Unmarshal(b, &o.acceptance)
				}
			}
		case "--context-file":
			// Anchor-triggered recall: the daemon writes the thread facts anchored to the files these
			// tasks name, and passes the path (same shape as --globals-file, and read the same way —
			// best-effort, since missing context slows a worker rather than breaking it).
			if i++; i < len(args) {
				if b, err := os.ReadFile(strings.TrimSpace(args[i])); err == nil {
					o.anchored = string(b)
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
		case "--grants-file":
			// #575: the daemon writes the run's build-role tool grants (names/prefixes, DATA) to a
			// file and passes its path here (parallel to --globals-file). Load best-effort — a
			// missing/unreadable file just means no extra tools, never a failed run. Every entry is
			// re-validated at USE (resolveLaunchGrants in the worker) before anything widens.
			if i++; i < len(args) {
				if b, err := os.ReadFile(strings.TrimSpace(args[i])); err == nil {
					var g api.ToolGrants
					if json.Unmarshal(b, &g) == nil && (len(g.MCP) > 0 || len(g.Shell) > 0) {
						workerToolGrants = &g
					}
				} else {
					fmt.Fprintf(os.Stderr, "(grants file unreadable — running without tool grants: %v)\n", err)
				}
			}
		case "--literal":
			// Worklist lines are DATA: no #-comment stripping. See crankOpts.literal.
			o.literal = true
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
		case "--pipeline":
			// G.6: the project's PIPELINE POLICY as a daemon-written JSON DATA file. Policy only —
			// readPipelineFile re-validates every field and drops anything that does not fit the
			// shape, so a malformed or hostile file degrades to the default pipeline rather than
			// changing what executes.
			if i++; i < len(args) {
				o.checks, o.lanes = readPipelineFile(args[i])
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
		fatal(fmt.Errorf(`usage: ptln crank --file <backlog.txt> [--thread <id>] [--max N] [--max-tokens N] [--halt-on-fail K] [--timeout 20m] [--allow-bash] [--model <m>] [--engine <e>] [--resume] [--literal] [--max-repairs N]
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
		tasks, err = parseTasks(o.file, o.literal)
		if err != nil {
			fatal(err)
		}
		if len(tasks) == 0 {
			// Non-zero on purpose: a run with nothing to do must not report success.
			fmt.Fprintln(os.Stderr, "ptln:", emptyWorklistError(o.file, o.literal))
			os.Exit(emptyWorklistExit)
		}
		// #76 task-authoring aid: nudge (never block) toward an executable acceptance criterion per task.
		warnTasksMissingAcceptanceCue(tasks)
	}
	// The effective engine must exist in the registry AND on this machine before any task runs.
	engineSpec, engineOK := engineSpecFor(o.engine)
	if !engineOK {
		// Derived from the registry so this message can never drift behind newly added engines
		// (it once said four when six were valid).
		fatal(fmt.Errorf("unknown engine %q — valid: %s", o.engine, strings.Join(eng.Names(), ", ")))
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
	// FAIL-SAFE, NOT FAIL-OPEN. This used to fall back to "running the full list" on any read
	// failure — which on a resume means silently REBUILDING already-done tasks from scratch: real
	// model spend (an entire five-hour window, in the incident that motivated this) re-doing work
	// that was finished, committed, and verified. If we can't learn what's done, the only safe
	// move is to stop before spending anything: self-report the abort with the ACTUAL error (so
	// the run page names the cause instead of a bare exit status) and exit resumeAbortExit.
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}
	abort := func(why string) {
		msg := "resume aborted: " + why + " — nothing was re-run and no tokens were spent. Continue to retry."
		fmt.Fprintln(os.Stderr, "✗ "+msg)
		if token != "" {
			_ = api.SetRunStatus(base, token, runID, "failed", msg)
		}
		os.Exit(resumeAbortExit)
	}
	if token == "" {
		abort("no device token in the environment, so the store of already-done tasks is unreadable")
	}
	var rows []api.RunTaskStatus
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if rows, err = api.ListRunTasks(base, token, runID); err == nil {
			break
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}
	if err != nil {
		abort(fmt.Sprintf("couldn't read which tasks were already done (%v)", err))
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

// parseTasks reads a backlog file: one task per line, blank lines always skipped. #-comments are
// skipped too UNLESS literal is set, in which case every non-blank line is a task verbatim — see
// crankOpts.literal for why that opt-out has to exist.
func parseTasks(path string, literal bool) ([]string, error) {
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
		if line == "" || (!literal && strings.HasPrefix(line, "#")) {
			continue
		}
		tasks = append(tasks, line)
	}
	return tasks, sc.Err()
}

// emptyWorklistExit: the worklist parsed to ZERO tasks, so this run has nothing to do. It is a
// FAILURE, not a clean stop — crank used to print a note and return 0, which made the daemon record
// `done` on a run that produced no tasks, no log, and no PR (chain c842d926: three of four members).
// Distinct from the pause codes so waitRun can name the actual cause on the run page.
const emptyWorklistExit = 7

// emptyWorklistError is the refusal message for a zero-task worklist. Without --literal it names the
// #-as-comment trap by name, because that is overwhelmingly the reason a non-empty file yields no
// tasks (every line was an issue-ref title like "#570: ...").
func emptyWorklistError(path string, literal bool) error {
	if literal {
		return fmt.Errorf("no tasks in %s — the worklist is empty (blank lines are ignored)", path)
	}
	return fmt.Errorf("no tasks in %s — blank lines and lines starting with # are ignored as comments; "+
		"if your task titles start with # (an issue ref like \"#570: ...\"), pass --literal to take every line as a task", path)
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

// defaultMaxRepairRounds bounds the in-run repair loop (builder fixes → gate re-reviews) when the
// run doesn't say. Two rounds resolves the common cases — a fixable miss, then a fix-of-the-fix —
// while a task the gate still rejects after two honest attempts is a builder↔reviewer disagreement
// (ambiguous task, or the reviewer is wrong), which no amount of retrying converges: that one goes
// to a human. Overridable per run with --max-repairs 0..maxRepairRoundsCeiling (#569).
const defaultMaxRepairRounds = 2

// maxRepairRoundsCeiling caps what --max-repairs may ask for. Past a handful of rounds the loop is
// no longer converging, it's just spending — the ceiling makes that non-negotiable.
const maxRepairRoundsCeiling = 5

// clampMaxRepairs is crank's OWN gate on a --max-repairs argument (#569) — the last line of defense,
// independent of the daemon's validMaxRepairs, because crank is also invoked by hand. Only an integer
// in [0, maxRepairRoundsCeiling] is accepted; anything else (unparseable, negative, over the ceiling)
// returns cur unchanged, so a bad value keeps the default instead of becoming one. Pure + total so
// the table can be pinned in a test. Note 0 is a REAL value here, not "absent".
func clampMaxRepairs(arg string, cur int) int {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n < 0 || n > maxRepairRoundsCeiling {
		return cur
	}
	return n
}

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

// originalTask recovers the real task from a LEGACY retry-wrapped store entry. Old binaries wrote
// the full rejectedRetryPrompt (preamble + findings + original) into the task store, so everything
// later derived from the stored task — branch slug, PR title, PR body — read "A previous attempt at
// this task was REJECTED…". New stores keep the task pristine (the wrapper travels in the prompt),
// but old runs' stores are polluted forever; the wrapper always carried this marker, so use it.
func originalTask(task string) string {
	if _, after, ok := strings.Cut(task, "--- THE ORIGINAL TASK ---"); ok {
		return strings.TrimSpace(after)
	}
	return task
}

// taskBranchName is the branch/worktree identity for task i of a run. The slug alone is NOT an
// identity: it's the task's first words, and (a) unrelated tasks can share them, (b) every legacy
// retry store begins with the same rejection preamble — which is how four unrelated runs piled
// commits onto one shared "crank-01-A-previous-attempt-at" branch and a single franken-PR (#499).
// The run-id fragment makes the branch unique PER RUN while staying stable WITHIN a run — exactly
// what resume-in-place (reuse the worktree), restart (wipe this run's branch, nobody else's), and
// chain stacking (fork the stored branch) require. No run id (manual crank) → legacy shape.
func taskBranchName(runID string, i int, task string) string {
	slug := gitwt.FlatSlug(firstWords(originalTask(task), 4))
	if runID == "" {
		return fmt.Sprintf("crank-%02d-%s", i+1, slug)
	}
	frag := runID
	if len(frag) > 8 {
		frag = frag[:8]
	}
	return fmt.Sprintf("crank-%s-%02d-%s", frag, i+1, slug)
}

// rateLimitExit is crank's exit code for an unattended run the model PROVIDER throttled (rate limit)
// before it could finish — "paused, resumable when the quota window resets," not a failure. crank
// self-reports the run to needs_approval with the reset time BEFORE exiting (it has the reset time
// from the worker; the daemon doesn't), so the daemon just leaves the status alone on this code.
const rateLimitExit = 5

// resumeAbortExit: a --resume that could NOT read the run's task store and refused to run blind.
// Distinct so the daemon can leave the (already self-reported) failed status + reason alone. See
// resumeStore — the old behavior was to fall back to the full worklist, i.e. silently rebuild
// finished work at full model cost.
const resumeAbortExit = 6

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
//
// `task` is the ORIGINAL task text — it names the branch, titles the commit + PR, and is what the
// verify/review gate reviews against. `prompt` is what the WORKER actually receives: normally the
// same as task, but on a findings-aware retry it's the retry-wrapped prompt ("a previous attempt
// was REJECTED …"). Keeping them separate is what stops that wrapper prose from leaking into the
// branch name and PR title (it once produced titles like "crank: A previous attempt was REJECTED").
type taskExec func(i int, task, prompt string) crankResult

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

// isEntitlementBlock reports whether a provider block reason is an ENTITLEMENT/billing refusal (which
// a quota reset never clears) rather than a time-windowed rate limit (which it does). Substring match
// on the wording providers actually emit — "usage credits required", org overage disabled, model not
// enabled. Deliberately broad: a false positive just shows the honest "add credits / enable overage"
// copy on a real rate limit, which is a mild inaccuracy; a false negative resurrects the bug of
// telling a user to wait for a reset that can't help.
func isEntitlementBlock(note string) bool {
	n := strings.ToLower(note)
	for _, m := range []string{"credit", "overage", "org_level_disabled", "not enabled", "isn't enabled", "entitlement", "billing", "insufficient"} {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

// firstNonEmpty returns the first non-blank string, or "".
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
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
	// An ENTITLEMENT block ("usage credits required", org overage disabled, model not enabled) is NOT
	// a rate limit, and mislabeling it as one sent the owner waiting for an 11:30 "reset" that could
	// never clear it. The distinction: a rate limit resets with time; an entitlement block resets only
	// when a human changes billing. It's detected here because the SAME run can carry BOTH — an
	// allowed 5-hour event gives a reset time, then an entitlement rejection blocks — and the old
	// switch checked the reset time FIRST, so the entitlement reason never surfaced. Entitlement wins:
	// its reset time is irrelevant, so we drop it (pass zero) and the run pauses as a plain
	// needs_approval whose reason names the actual fix, rather than a countdown to a non-event.
	entitlement := blocked && isEntitlementBlock(note)
	detail := ""
	switch {
	case entitlement:
		detail = "⏸ blocked — " + firstNonEmpty(note, "this model needs usage credits, or overage is disabled for your org") +
			"\nA quota reset won't clear this. Enable overage / add credits for this org (or switch model), then Continue."
		reset = time.Time{} // not a time-based pause — don't offer a bogus auto-resume countdown
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
	// G.2: the entitlement/rate-limit distinction above is already made in the DETAIL text. Send it
	// as data too, so the board can withhold the auto-resume countdown for a block that no amount of
	// waiting will clear.
	reason := surface.PauseRateLimit
	if entitlement {
		reason = surface.PauseEntitlement
	}
	if base != "" && token != "" {
		_ = api.SetRunPausedReason(base, token, o.run, reason, detail, reset)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", detail)
	os.Exit(rateLimitExit)
}

// realTaskExec is the production per-task worker: a fresh worktree, the worker atom, then a
// commit on its own branch (never a push). Extracted from the loop so runCrankWith is testable.
func realTaskExec(repo string, o crankOpts, logger *runLogger) taskExec {
	return func(i int, task, prompt string) crankResult {
		// Unchained: one worktree/branch per task, keyed to THIS run (see taskBranchName — the slug
		// alone collided across runs). CHAINED: every task in every member of the chain uses the SAME
		// branch, so step N opens the files step N-1 just edited (gitwt.create reuses an existing
		// worktree whose branch matches) — no forking from origin/<default> and re-colliding.
		name := taskBranchName(o.run, i, task)
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
			// Wipe the abandoned attempt's PR first (GitHub only): close it + delete its remote branch so
			// it doesn't linger open — the fresh run below opens a new one. Best-effort, before the local
			// branch is deleted (existingPRURL reads it via gh, which uses the remote anyway).
			if o.gitProvider == "" || o.gitProvider == "github" {
				closePRForBranch(realRunner(repo, mergeGitHubToken(o.run)), gitwt.Slug(name))
			}
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
		// Everything from here to the worker's first streamed line used to be silent, and on a cold
		// clone it is the longest silent stretch in a run: resolving the base, creating a worktree,
		// writing globals, materializing skills. A card whose only line is a task title from eight
		// minutes ago reads exactly like a hung one — the confusion that let a genuinely dead run sit
		// unnoticed for days. These are cheap notes on an existing stream, not new plumbing.
		logger.note(i, "step", "⚙ preparing worktree "+name)
		baseRef := ""
		if o.base != "" {
			ref, berr := gitwt.RemoteBase(repo, o.base)
			// A STACKED CHAIN member forks from its predecessor's branch, and that branch is deleted
			// the moment the predecessor's PR merges — the normal end state under merge policy `pr`.
			// The name still lives in run_tasks, so the server keeps handing it over and the member
			// fails identically on every retry, forever, without doing any work. Once the predecessor
			// is merged its commits ARE in the project base, so the fallback is the same tree by
			// another name — not a silent wrong base.
			//
			// Only a stacked member carries a fallback. Without one this stays a hard failure, because
			// a base an OPERATOR configured that is missing from origin is a mistake worth stopping on.
			if berr != nil && o.baseFB != "" && o.baseFB != o.base {
				if fb, ferr := gitwt.RemoteBase(repo, o.baseFB); ferr == nil {
					logger.note(i, "step", "⚙ base "+o.base+" is gone from origin (predecessor merged) — forking from "+o.baseFB)
					ref, berr = fb, nil
				}
			}
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
		// Phase B3: inject the project globals into THIS worktree as AGENTS.md + CLAUDE.md (the worker's cwd), so it
		// reads the project's rules/stack/guardrails natively. Written here (not the repo root) because
		// the worktree only sees tracked / SeedInclude'd files. No-op when the run carried no globals.
		writeWorktreeGlobals(wtPath, o.globals)
		writeWorktreeContext(wtPath, o.anchored)

		// RED BEFORE GREEN. Run the task's acceptance checks against the untouched worktree first: an
		// acceptance check that ALREADY passes cannot prove the work happened, and a task built on one
		// reaches a reviewer looking finished. Stopping here costs one command; letting it through
		// costs a model run, its repair rounds and a reviewer call.
		//
		// NOT ON A RESUME. The check above rests on the worktree being UNTOUCHED, and on a resume it
		// deliberately is not: `crank --resume` continues in the same worktree, which still holds the
		// previous attempt's uncommitted work. A task interrupted mid-flight (rate limit, a provider
		// 529, a timeout) has usually written its tests already, so its acceptance check passes — and
		// the preflight then refuses to let it finish, reading "the work is already here" as "this can
		// never prove itself".
		//
		// That made an interrupted run UNRESUMABLE for exactly the tasks furthest along, which is the
		// opposite of what resume is for. The judgement belongs on the FIRST attempt, against a clean
		// tree; on a resume it is not merely unhelpful, it is wrong.
		if len(o.acceptance) > 0 && resume == "" {
			pf := preflightAcceptance(wtPath, o.acceptance, o.timeout)
			for _, w := range pf.warns {
				logger.note(i, "step", "⚠ "+w)
			}
			if pf.blocked != "" {
				logger.note(i, "step", "✗ acceptance check already passes — not started")
				return crankResult{task: task, branch: name, ok: false, note: pf.blocked}
			}
			if pf.ran > 0 {
				logger.note(i, "step", fmt.Sprintf("✓ %s red before the work, as acceptance requires", plural(pf.ran, "check", "checks")))
			}
		}
		// Org skill library: inject the run's enabled skills so ANY engine can use them — claude reads
		// .claude/skills, everything else .agents/skills. Best-effort per skill (bad names skipped);
		// the injected dirs are git-excluded in the worktree so the worker never commits them.
		if skills := loadGitwtSkills(o.skills, o.skillsDir); len(skills) > 0 {
			logger.note(i, "step", fmt.Sprintf("⚙ staging %s", plural(len(skills), "skill", "skills")))
			_ = gitwt.MaterializeSkills(wtPath, skills)
		} else {
			_ = gitwt.MaterializeSkills(wtPath, nil)
		}
		// Dispatch-time citation check (grade integrity): verify the task's cited paths against the
		// EXACT tree the worker sees. Stale citations go in front of the worker — locate the current
		// equivalent, never rebuild a removed file because a two-week-old ticket named it.
		if stale := staleCitations(wtPath, originalTask(task)); len(stale) > 0 {
			logger.note(i, "step", "⚠ stale citations in task: "+strings.Join(stale, ", "))
			prompt = staleCitationNote(stale) + prompt
		}
		// crank-01: live step output. A non-nil sink (run id + device token present) streams the
		// worker's stdout into run_logs as it works; nil → the buffered path, unchanged.
		if resume != "" {
			logger.note(i, "step", "↻ resuming "+firstWords(task, 12))
		} else {
			logger.note(i, "step", "▶ "+firstWords(task, 12))
		}
		out, werr := runWorker(wtPath, prompt, o.engine, o.model, o.thread, o.allowBash, o.timeout, logger.sink(i), resume)
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
			out, werr = runWorker(wtPath, prompt, o.engine, o.model, o.thread, o.allowBash, o.timeout, logger.sink(i), "")
		}
		// #263: keep the worker's own summary (workerPrompt asks it to end with "what I changed +
		// what a reviewer should check") so the run history is legible — this used to be discarded.
		committed := false // #641: set where the commit is detected; drives the teardown decision below
		r := crankResult{task: task, branch: branch, ok: werr == nil, tokens: out.tokens, freshTokens: out.freshTokens, cacheReadTokens: out.cacheReadTokens, costUSD: out.costUSD, summary: strings.TrimSpace(out.text), rateLimitResetAt: out.rateReset, rateLimited: out.rateBlocked, rateNote: out.rateNote, resumeHandle: out.resumeHandle, invokedSkills: out.invokedSkills}
		if werr != nil {
			r.note = werr.Error()
		} else if !r.rateLimitResetAt.IsZero() {
			// Slice 2: the provider throttled us mid-task. Do NOT commit/verify/merge partial work as
			// if it were finished — leave the edits in the worktree so a resume continues in-place. The
			// task is reported `blocked` (not done) and the run pauses with the reset time (below).
			r.note = "⏸ rate limit reached — partial work left for resume"
		} else if o.commit {
			r.note = commitWorktree(wtPath, "crank: "+firstWords(task, 10))
			// Deliverability is a property of the BRANCH, not of who committed. The old gate
			// (`r.note == "committed"`) only passed when CRANK's own commit landed — but a worker
			// often commits its work itself, so commitWorktree saw a clean tree, said "no changes",
			// and a fully-built branch was silently stranded: never verified, never pushed, no PR,
			// while the task still reported done. Upgrade the note when the branch is ahead of the
			// base so agent-committed work flows into the same verify+merge path.
			if r.note == "no changes" && branchAhead(wtPath) > 0 {
				r.note = "committed (by agent)"
			}
			// #641: the teardown decision needs this as a FACT, captured here. r.note accumulates
			// (" · verify failed", " · PR opened", …) as the task proceeds, so a HasPrefix check at
			// the end of the function would be reading a different string than this one.
			committed = strings.HasPrefix(r.note, "committed")
			// #77 slice 3: only a branch with commits to deliver goes to push/PR/merge. push/pr/merge
			// operate on the shared repo (the branch ref lives in the main .git, not the worktree).
			if strings.HasPrefix(r.note, "committed") {
				// SAFETY FIRST — push before the gate, not after it. The verify/repair cycle can
				// take many minutes and can end in a rejection, a rate limit, or a crash; every one
				// of those paths used to leave the branch on this machine alone. Pushing first makes
				// the work durable and reviewable no matter what happens next, and costs nothing:
				// it's a branch, not a merge. (See pushWork for the three ways work got stranded.)
				tok := ""
				if o.gitProvider == "" || o.gitProvider == "github" {
					tok = mergeGitHubToken(o.run)
				}
				runner := realRunner(repo, tok)
				// C2: bring the branch onto the CURRENT base before anything else looks at it. The
				// worktree forked when the task STARTED, so a long task is stale by construction —
				// and a task that ran while a teammate merged is stale by exactly the change that
				// will conflict with it. Doing this before the push means the branch is published in
				// its final shape (no force-push later); doing it before verify means the gate checks
				// the code as it will actually land.
				//
				// A conflicting rebase does NOT fail the task: the agent's work is left exactly as
				// written, still pushed, still reviewable — resolving someone else's change here,
				// ungated and unattributed, is the repair ladder's job, not this one's.
				if fnote, fresh := freshenBranch(repo, wtPath, o.base); fnote != "" {
					r.note += " · " + fnote
					logger.note(i, "step", map[bool]string{true: "⟳ ", false: "⚠ "}[fresh]+fnote)
				}
				if pnote := pushWork(runner, branch); pnote != "" {
					r.note += " · " + pnote
					logger.note(i, "step", "⤴ "+pnote)
				}
				// Trust · T2: VERIFY before merge — three layers. T2a: the project's executable
				// acceptance checks (.partyline/verify). T2b: an independent adversarial reviewer
				// of the diff (.partyline/review). T2d: a vision reviewer that renders the changed
				// UI and looks at it (.partyline/visual — see visual.go). Pass (or none enabled) →
				// the branch is eligible to merge per policy. Any fails → QUARANTINE: skip the
				// merge, leave the branch for a human, carry the reasons (runCrankWith flips the
				// task to `blocked`, not `done`).
				// GREEN AFTER RED. The pre-flight proved these were failing before the worker
				// touched anything; this proves they pass now. Without it the pair is half a
				// control: a worker can finish, report success, and nothing ever asks whether the
				// task's own definition of done actually came true.
				//
				// It runs BEFORE the repo's own gates because it is the cheapest and the most
				// specific: if the thing the task was for still does not work, the reviewer's
				// opinion of the diff is not the interesting news.
				if len(o.acceptance) > 0 {
					ranAfter, unmet, warnsAfter := greenAfterAcceptance(wtPath, o.acceptance, verifyTimeout(o))
					for _, w := range warnsAfter {
						logger.note(i, "step", "⚠ "+w)
					}
					if len(unmet) > 0 {
						logger.note(i, "step", "✗ acceptance — the task's own check still fails")
						joined := strings.Join(unmet, "\n")
						r.verify = verifyResult{ran: true, ok: false, reasons: joined}
						r.ok = false
						r.note = joined
						return r
					}
					if ranAfter > 0 {
						logger.note(i, "step", fmt.Sprintf("✓ acceptance — %s green after the work", plural(ranAfter, "check", "checks")))
					}
				}
				r.verify = verifyTask(repo, wtPath, o.base, task, o.engine, verifyTimeout(o), pipelineCfg{
					visual:  visualCfg{on: o.visual, routes: o.visualRoutes},
					checks:  o.checks,
					lanes:   o.lanes,
					changed: changedFiles(wtPath),
					step:    func(line string) { logger.note(i, "step", line) },
				})
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
				// o.maxRepairs the task quarantines WITH its attempt history, and that is the
				// moment a human is legitimately needed.
				repairs := 0
				for round := 1; round <= o.maxRepairs && r.verify.ran && !r.verify.ok; round++ {
					logger.note(i, "step", fmt.Sprintf("🛠 verify rejected — auto-repair %d/%d", round, o.maxRepairs))
					fmt.Fprintf(os.Stderr, "  ✗ verify failed — auto-repair round %d/%d\n", round, o.maxRepairs)
					ahead := branchAhead(wtPath)
					// Re-inject the project globals: the pre-commit strip removed them, and the repair
					// worker deserves the same context the first attempt had (injection is idempotent).
					writeWorktreeGlobals(wtPath, o.globals)
					writeWorktreeContext(wtPath, o.anchored)
					rout, rerr := runWorker(wtPath, rejectedRetryPrompt(r.verify.reasons, task), o.engine, o.model, o.thread, o.allowBash, o.timeout, logger.sink(i), r.resumeHandle)
					r.tokens += rout.tokens
					r.freshTokens += rout.freshTokens
					r.cacheReadTokens += rout.cacheReadTokens
					r.costUSD += rout.costUSD
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
					r.verify = verifyTask(repo, wtPath, o.base, task, o.engine, verifyTimeout(o), pipelineCfg{
						visual:  visualCfg{on: o.visual, routes: o.visualRoutes},
						checks:  o.checks,
						lanes:   o.lanes,
						changed: changedFiles(wtPath),
						step:    func(line string) { logger.note(i, "step", line) },
					})
				}
				if repairs > 0 && r.verify.ran && r.verify.ok {
					logger.note(i, "step", fmt.Sprintf("✓ repaired — verify gate passes (round %d)", repairs))
					fmt.Fprintf(os.Stderr, "  ✓ repaired — verify gate now passes\n")
				} else if repairs > 0 {
					r.verify.reasons = fmt.Sprintf("(auto-repair: the builder retried %d time(s); the gate still rejects)\n%s", repairs, r.verify.reasons)
				}
				if r.verify.ran && !r.verify.ok {
					// Quarantined — NOT merged, but still pushed: re-push so the reviewer's human
					// sees the repaired state, not the first attempt. The branch name is already on
					// the task, so "review the quarantined branch" is now an action they can take.
					if pnote := pushWork(runner, branch); pnote != "" {
						r.note += " · " + pnote
					}
					r.note += " · verify failed (quarantined, not merged — branch is pushed for review)"
				} else if landed, lnote := tryLand(runner, o, repo, branch, wtPath, r.verify); landed {
					// THE MERGE TRAIN. Verified work goes straight onto the base, one branch at a
					// time (land.go), so the NEXT task in this run forks from a base that already
					// contains it. That is what stops five branches aging into five conflicts while
					// they wait for a human — and it is why this is gated on the verify gate having
					// actually passed rather than on the run merely finishing.
					//
					// Off unless --land. A branch that cannot land falls through to the merge policy
					// below, so the work is still pushed and still reviewable.
					r.note += " · " + lnote
				} else {
					if lnote != "" {
						r.note += " · " + lnote // tried to land, couldn't — say why, then open the PR
					}
					// originalTask: a legacy retry store carries the wrapped prompt as its task — strip it so
					// the PR is titled/described by the REAL task, not "A previous attempt was REJECTED…".
					// The last silent stretch: pushing and opening a PR is seconds on a good network
					// and a long stall on a bad one, and until now the card said nothing either way.
					if o.mergePolicy == "pr" || o.mergePolicy == "auto" {
						logger.note(i, "step", "⤴ pushing "+branch+" and opening a pull request")
					}
					note, prURL := applyMergePolicy(runner, branch, crankPRTitle(originalTask(task)), crankPRBody(originalTask(task), r.summary), o.mergePolicy, o.gitProvider, o.base, o.draft)
					if prURL != "" {
						logger.note(i, "step", "✓ pull request open — "+prURL)
					}
					if note != "" {
						r.note += " · " + note
					}
					r.prURL = prURL // #212: surfaced on the run task in the web
					// Slice A2: with the PR open, test-merge this branch against every OTHER open PR to
					// the same base and report the REAL conflicts (git merge-tree). The control plane
					// gates Accept + banners the drawer on what we report here. GitHub only — the scan
					// speaks `gh`.
					if prURL != "" && (o.gitProvider == "" || o.gitProvider == "github") {
						base := o.base
						if base == "" {
							base = gitwt.DefaultBaseName(repo)
						}
						r.conflicts, r.conflictsChecked = scanPRConflicts(runner, branch, base)
					}
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
		// #641: decide what survives this task. Skipped for a rate-limited task — its partial work
		// is deliberately left uncommitted in the worktree for the resume to finish, so this is the
		// one ending where the worktree is not an orphan but the actual state of the job.
		if wtPath != "" && !r.rateLimited {
			note := tearDownTask(repo, wtPath, branch, taskEnd{
				committed:   committed,
				quarantined: r.verify.ran && !r.verify.ok,
				prOpened:    r.prURL != "",
				mergePolicy: o.mergePolicy,
			})
			r.note += " · " + note
			logger.note(i, "step", note)
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
		// findings in front of it. Prompt-only — the ORIGINAL `task` still names the branch, titles
		// the commit + PR, and is what the gate reviews against; only the worker's PROMPT carries the
		// findings wrapper. Passing both keeps that wrapper out of the branch name and PR title.
		prompt := task
		if f := o.resumeFindings[i]; f != "" {
			prompt = rejectedRetryPrompt(f, task)
		}
		started := time.Now()
		r := exec(i, task, prompt)
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
		PRURL: cr.prURL, Summary: cr.summary, Tokens: cr.tokens, FreshTokens: cr.freshTokens,
		CacheReadTokens: cr.cacheReadTokens, CostUSD: cr.costUSD, DurationMs: cr.durationMs,
		Verified: verified, ResumeHandle: cr.resumeHandle,
		Conflicts: cr.conflicts, ConflictsChecked: cr.conflictsChecked,
		// G.3: send the typed report, not just the pass/fail summary. The control plane derives
		// `done` from it — this worker no longer gets to be the last word on whether its own work
		// was verified. Nil on a gate that never ran, which the server records as UNVERIFIED
		// rather than as a pass.
		Gate: cr.verify.report,
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
				r := exec(ct.Idx, ct.Task, ct.Task)                    // claim path carries no findings wrapper — prompt == task
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
	// The injected project-globals block (AGENTS.md/CLAUDE.md) is context FOR the worker, not part
	// of the deliverable — strip it so `add -A` can't sweep it into the commit/PR (the reviewer
	// rightly graded that pollution down on every run that carried it).
	stripWorktreeGlobals(dir)
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
	// Gated through sgr/dim: a crank run is routinely piped into a log, and raw SGR in a log file
	// is corruption, not colour.
	fmt.Printf("\n%s crank done — %d/%d ok, %d attempted of %d in the backlog\n", sgr(cgOK, "✓"), ok, len(results), len(results), total)
	fmt.Println("  each item is a branch for you to review — nothing was pushed or merged.")
	for _, r := range results {
		mark := sgr(cgBad, "✗")
		if r.ok {
			mark = sgr(cgOK, "✓")
		}
		fmt.Printf("  %s %-28s %s\n", mark, r.branch, dim(r.note))
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

// crankPRTitle turns a task into a human-readable PR title: its first non-blank line, whitespace
// collapsed, capped so it reads as a title and not a paragraph. Always derived from the ORIGINAL
// task (never a retry-wrapped prompt), which is why the leak that produced "crank: A previous
// attempt was REJECTED …" can't recur. No "crank:" prefix — the PR body carries the provenance.
func crankPRTitle(task string) string {
	line := ""
	for _, l := range strings.Split(task, "\n") {
		if line = strings.TrimSpace(l); line != "" {
			break
		}
	}
	line = strings.Join(strings.Fields(line), " ") // collapse runs of whitespace
	const max = 72
	if r := []rune(line); len(r) > max {
		line = strings.TrimSpace(string(r[:max])) + "…"
	}
	if line == "" {
		return "crank: automated change"
	}
	return line
}

// crankPRBody is the PR description a crank run opens: the task it set out to do, plus the build
// agent's own account of what it changed and what a reviewer should check (the worker prompt asks
// for exactly this, #263). A real, reviewable body — not "Opened automatically by ptln crank."
func crankPRBody(task, summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "_(the build agent did not report a summary)_"
	}
	return "## Task\n\n" + strings.TrimSpace(task) +
		"\n\n## What changed\n\n" + summary +
		"\n\n---\n🤖 Opened automatically by [ptln](https://partyline.sh) crank."
}
