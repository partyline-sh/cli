package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
//	           [--halt-on-fail K] [--timeout 20m] [--allow-bash] [--no-commit]
//
// Each non-blank, non-# line of the file is one task. Sequential (not parallel) so each item
// sees what the previous ones recorded on the shared thread — the moat applied to autonomy.
// Stops on: list exhausted · --max reached · --max-tokens exceeded (O.5, Claude-first; other
// engines report no usage → no token halt) · K consecutive failures · (per-item) time budget.

type crankOpts struct {
	file       string
	thread     string
	run        string // O.3: run id (UUID) — when set, crank self-reports per-task lifecycle
	max        int    // 0 = all
	maxTokens  int    // O.5: crude token ceiling for the whole worklist; 0 = unbounded (off)
	haltOnFail int
	timeout    time.Duration
	allowBash  bool
	commit     bool
}

type crankResult struct {
	task   string
	branch string
	ok     bool
	note   string
	tokens int // O.5: tokens this task's worker reported (0 = unknown / no usage seen)
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
		}
	}
	if o.file == "" {
		fatal(fmt.Errorf(`usage: ptln crank --file <backlog.txt> [--thread <id>] [--max N] [--max-tokens N] [--halt-on-fail K] [--timeout 20m] [--allow-bash]`))
	}
	tasks, err := parseTasks(o.file)
	if err != nil {
		fatal(err)
	}
	if len(tasks) == 0 {
		fmt.Println("no tasks in the file (lines; blank and # ignored)")
		return
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
	// The daemon appends --run; PARTYLINE_RUN_ID is the env fallback for the same value.
	if o.run == "" {
		o.run = strings.TrimSpace(os.Getenv("PARTYLINE_RUN_ID"))
	}
	// O.5 token ceiling: the flag wins; PARTYLINE_MAX_TOKENS is the env fallback so the daemon/run
	// path can set it later. 0 = unbounded (default) — today's behavior, unchanged when unset.
	if o.maxTokens == 0 {
		if v := strings.TrimSpace(os.Getenv("PARTYLINE_MAX_TOKENS")); v != "" {
			fmt.Sscanf(v, "%d", &o.maxTokens)
		}
	}

	runCrank(repo, tasks, o)
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

// approveMoreTokens is the pause-and-approve gate (#80): when the worklist hits the token ceiling
// at a task boundary, an INTERACTIVE run pauses and lets the human extend the budget instead of
// hard-stopping. Returns >0 = add that many tokens · -1 = remove the limit entirely · 0 = stop.
// A non-interactive run (no TTY — e.g. an unattended/daemon run) can't prompt, so it returns 0
// (the hard stop, unchanged); surfacing the pause in the web + a notification is the unattended
// equivalent (daemon path, not this function).
func approveMoreTokens(used, limit int) int {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return 0
	}
	fmt.Fprintf(os.Stderr, "\n⏸  token budget reached (%d/%d).\n", used, limit)
	fmt.Fprintf(os.Stderr, "   approve more?  [enter = stop · <number> = add that many tokens · a = remove the limit] › ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch s := strings.TrimSpace(line); {
	case s == "":
		return 0
	case s == "a" || s == "all" || s == "remove" || s == "unlimited":
		return -1
	default:
		n := 0
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			return n
		}
		fmt.Fprintf(os.Stderr, "   (didn't understand %q — stopping)\n", s)
		return 0
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
			r.note = commitWorktree(wtPath, "crank: "+firstWords(task, 10))
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
		report.emit(i, task, "queued", "", "")
	}
	var results []crankResult
	consecFails := 0
	usedTokens := 0
loop:
	for i, task := range tasks {
		// Token ceiling: pause-and-approve (#80) BEFORE the hard-halt check. Interactive → let the
		// human add budget or lift the limit and continue in-process (state intact, no re-run);
		// non-interactive → approveMoreTokens returns 0 and we hard-stop as before.
		if o.maxTokens > 0 && usedTokens >= o.maxTokens {
			switch add := approveMoreTokens(usedTokens, o.maxTokens); {
			case add < 0:
				o.maxTokens = 0
				fmt.Fprintf(os.Stderr, "   ↻ limit removed — continuing unbounded.\n")
			case add > 0:
				o.maxTokens += add
				fmt.Fprintf(os.Stderr, "   ↻ +%d approved — ceiling now %d, continuing.\n", add, o.maxTokens)
			default:
				fmt.Fprintf(os.Stderr, "\n■ stopping: token budget reached (%d/%d) (%d/%d tasks attempted)\n", usedTokens, o.maxTokens, i, len(tasks))
				break loop
			}
		}
		if halt, why := crankShouldHalt(i, consecFails, usedTokens, o); halt {
			fmt.Fprintf(os.Stderr, "\n■ stopping: %s (%d/%d tasks attempted)\n", why, i, len(tasks))
			break loop
		}
		fmt.Fprintf(os.Stderr, "\n▶ [%d/%d] %s\n", i+1, len(tasks), task)
		report.emit(i, task, "running", "", "")
		r := exec(i, task)
		results = append(results, r)
		usedTokens += r.tokens
		if r.ok {
			consecFails = 0
			report.emit(i, r.task, "done", r.branch, r.note)
		} else {
			consecFails++
			report.emit(i, r.task, "failed", r.branch, r.note)
		}
	}
	crankSummary(results, len(tasks))
}

// runReporter posts per-task lifecycle events to the run store (O.3). post is nil when there's
// no run id / no credentials, making every emit a no-op — self-reporting is pure telemetry and
// must never affect the run. The daemon passes the run id (--run) + device token + base to the
// crank child; a POST failure is logged and swallowed inside post.
type runReporter struct {
	post func(idx int, task, status, branch, detail string)
}

func (r runReporter) emit(idx int, task, status, branch, detail string) {
	if r.post == nil {
		return
	}
	r.post(idx, task, status, branch, detail)
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
	return runReporter{post: func(idx int, task, status, branch, detail string) {
		if err := api.UpsertRunTask(base, token, runID, idx, task, status, branch, detail); err != nil {
			fmt.Fprintf(os.Stderr, "  (run-task telemetry idx %d %s: %v)\n", idx, status, err)
		}
	}}
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
