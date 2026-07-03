package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/gitwt"
)

// work.go — E4.1, the worker ATOM: one bounded, sandboxed, witnessed autonomous task run.
// It is the unit the conductor loop (E4.8) will call, exposed now as `ptln work` so it's
// useful and testable on its own.
//
//	ptln work "add a dark-mode toggle to the navbar" [--worktree feat-dark] [--thread <id>]
//	          [--allow-bash] [--model <m>] [--timeout 20m]
//
// The four invariants (see docs/E4-CONDUCTOR-PLAN.md) are enforced HERE, at the atom:
//  1. Sandbox — runs in a git worktree by default (blast radius = one branch).
//  2. Proposal, not push — it leaves commits on the branch; a human merges. We never push.
//  3. Bounded — a hard wall-clock timeout; the headless run terminates or is killed.
//  4. Allowlist, not bypass — read/edit/write by default; Bash only with --allow-bash;
//     NEVER --dangerously-skip-permissions.
func workMain(args []string) {
	task, worktree, thread, model := "", "", "", ""
	allowBash := false
	timeout := 20 * time.Minute
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--worktree", "--wt":
			if i++; i < len(args) {
				worktree = strings.TrimSpace(args[i])
			}
		case "--thread":
			if i++; i < len(args) {
				thread = strings.TrimSpace(args[i])
			}
		case "--model":
			if i++; i < len(args) {
				model = strings.TrimSpace(args[i])
			}
		case "--allow-bash":
			allowBash = true
		case "--timeout":
			if i++; i < len(args) {
				if d, err := time.ParseDuration(args[i]); err == nil {
					timeout = d
				}
			}
		default:
			pos = append(pos, args[i])
		}
	}
	task = strings.TrimSpace(strings.Join(pos, " "))
	if task == "" {
		fatal(fmt.Errorf(`usage: ptln work "<task>" [--worktree <name>] [--thread <id>] [--allow-bash] [--model <m>] [--timeout 20m]`))
	}
	if _, err := exec.LookPath("claude"); err != nil {
		fatal(fmt.Errorf("claude not found on PATH — the worker runs it headless"))
	}

	dir, _ := os.Getwd()
	// Invariant 1 — sandbox. --worktree isolates to a branch; without it we run in cwd but
	// still refuse to touch a non-git dir's history (the human owns that risk explicitly).
	if worktree != "" {
		repo, err := gitwt.RepoRoot(dir)
		if err != nil {
			fatal(fmt.Errorf("--worktree needs a git repository: %w", err))
		}
		wtPath, branch, err := gitwt.Create(repo, worktree)
		if err != nil {
			fatal(err)
		}
		_ = gitwt.SeedInclude(repo, wtPath)
		fmt.Fprintf(os.Stderr, "⎇ worker sandbox: %s (branch %s)\n", wtPath, branch)
		dir = wtPath
	}
	// Inherit the repo's bound thread if none given (E3.5) — the worker shares the memory.
	if thread == "" {
		thread = loadRepoBind(dir)
	}

	out, err := runWorker(dir, task, model, thread, allowBash, timeout)
	fmt.Println(out)
	if err != nil {
		fatal(fmt.Errorf("worker: %w", err))
	}
	fmt.Fprintln(os.Stderr, "\n"+workerNextSteps(dir, worktree))
}

// workerTools is the default allowlist (invariant 4): read + edit the code, keep a todo list.
// Bash is added only when the caller opts in. Never a bypass flag.
func workerTools(allowBash bool) []string {
	t := []string{"Read", "Grep", "Glob", "Edit", "Write", "MultiEdit", "TodoWrite"}
	if allowBash {
		t = append(t, "Bash")
	}
	return t
}

// workerPrompt frames the task with the standing autonomous instructions: do the work, keep
// the shared memory current, and STOP + escalate rather than force past a blocked tool.
func workerPrompt(task string, thread bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are an autonomous worker in a sandboxed git worktree. Task:\n\n%s\n\n", task)
	b.WriteString("Rules:\n")
	b.WriteString("- Do the task end to end; make the edits directly in this worktree.\n")
	b.WriteString("- Do NOT run git push, git commit to any branch but this one, or anything destructive. Leave your changes uncommitted or committed on THIS branch only — a human reviews and merges.\n")
	b.WriteString("- If you're blocked and would need a tool you don't have (or a destructive action), STOP and clearly explain what you need — do not try to force around it.\n")
	if thread {
		b.WriteString("- As you settle decisions, constraints, or contracts, record them with the partyline-context-threads `remember` tool (tag `entities`), and `recall` the shared context before assuming.\n")
	}
	b.WriteString("\nWhen finished, end with a short summary: what you changed (files) and anything a reviewer should check.")
	return b.String()
}

// runWorker executes the headless, tool-scoped, timeout-bounded claude run. Thread-wiring gives
// the worker recall/remember (Model A — the user's own tokens). Returns the transcript text.
func runWorker(dir, task, model, thread string, allowBash bool, timeout time.Duration) (string, error) {
	cargs := []string{"-p", workerPrompt(task, thread != ""), "--allowedTools", strings.Join(workerTools(allowBash), ",")}
	if model != "" {
		cargs = append(cargs, "--model", model)
	}
	if thread != "" {
		if cfg := mcpServersJSON(true, nil, loadMCPCatalog()); cfg != "" {
			cargs = append(cargs, "--mcp-config", cfg)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", cargs...)
	cmd.Dir = dir
	// Per-process env (never a global — the contamination fix): the worker's thread + a
	// blank agent name so facts attribute to the user piloting it.
	env := append(os.Environ(), "PARTYLINE=1")
	if thread != "" {
		env = append(env, "PARTYLINE_THREAD_ID="+thread, "PARTYLINE_ENGINE=claude")
	}
	cmd.Env = env
	fmt.Fprintf(os.Stderr, "⚙ worker running (%s budget, tools: %s)…\n", timeout, strings.Join(workerTools(allowBash), " "))
	b, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(b), fmt.Errorf("hit the %s time budget — stopped", timeout)
	}
	return string(b), err
}

// workerNextSteps tells the human how to review — the "results are proposals" invariant made
// visible. In a worktree, review the branch diff; then merge or discard.
func workerNextSteps(dir, worktree string) string {
	if worktree == "" {
		return "→ review the changes in this dir (git diff), then commit/merge yourself."
	}
	return fmt.Sprintf("→ review the worker's branch:\n    cd %s && git status && git diff\n  merge it when you're happy, or drop it:  ptln wt rm %s", dir, worktree)
}
