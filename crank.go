package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/gitwt"
)

// crank.go — E4.8, the worklist loop: drive a backlog of tasks one at a time through the
// worker atom (E4.1), each in ITS OWN worktree, sharing ONE context thread. The brakes are
// the point (see docs/E4-CONDUCTOR-PLAN.md) — this prepares N reviewable branches, it does
// NOT ship anything:
//
//	ptln crank --file backlog.txt [--thread <id>] [--max N] [--halt-on-fail K]
//	           [--timeout 20m] [--allow-bash] [--no-commit]
//
// Each non-blank, non-# line of the file is one task. Sequential (not parallel) so each item
// sees what the previous ones recorded on the shared thread — the moat applied to autonomy.
// Stops on: list exhausted · --max reached · K consecutive failures · (per-item) time budget.

type crankOpts struct {
	file       string
	thread     string
	max        int // 0 = all
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
		case "--max":
			if i++; i < len(args) {
				fmt.Sscanf(args[i], "%d", &o.max)
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
		fatal(fmt.Errorf(`usage: ptln crank --file <backlog.txt> [--thread <id>] [--max N] [--halt-on-fail K] [--timeout 20m] [--allow-bash]`))
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

// crankShouldHalt is the stop-condition decision (unit-tested): stop when we've hit the item
// cap or K consecutive failures. done = items completed so far; consecFails = current streak.
func crankShouldHalt(done, consecFails int, o crankOpts) (halt bool, why string) {
	if o.max > 0 && done >= o.max {
		return true, fmt.Sprintf("reached --max %d", o.max)
	}
	if o.haltOnFail > 0 && consecFails >= o.haltOnFail {
		return true, fmt.Sprintf("%d consecutive failures", consecFails)
	}
	return false, ""
}

func runCrank(repo string, tasks []string, o crankOpts) {
	var results []crankResult
	consecFails := 0
	for i, task := range tasks {
		if halt, why := crankShouldHalt(i, consecFails, o); halt {
			fmt.Fprintf(os.Stderr, "\n■ stopping: %s (%d/%d tasks attempted)\n", why, i, len(tasks))
			break
		}
		name := fmt.Sprintf("crank-%02d-%s", i+1, gitwt.Slug(firstWords(task, 4)))
		fmt.Fprintf(os.Stderr, "\n▶ [%d/%d] %s\n", i+1, len(tasks), task)
		wtPath, branch, err := gitwt.Create(repo, name)
		if err != nil {
			results = append(results, crankResult{task, name, false, "worktree: " + err.Error()})
			consecFails++
			continue
		}
		_ = gitwt.SeedInclude(repo, wtPath)
		_, werr := runWorker(wtPath, task, "", o.thread, o.allowBash, o.timeout)
		r := crankResult{task: task, branch: branch, ok: werr == nil}
		if werr != nil {
			r.note = werr.Error()
			consecFails++
		} else {
			consecFails = 0
			if o.commit {
				r.note = commitWorktree(wtPath, "crank: "+firstWords(task, 10))
			}
		}
		results = append(results, r)
	}
	crankSummary(results, len(tasks))
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
