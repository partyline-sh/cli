package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	out, _, err := runWorker(dir, task, model, thread, allowBash, timeout, nil)
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

// workerUsage is the per-run token accounting claude reports via `-p --output-format json`.
type workerUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// total is a crude "tokens this run touched" sum — the signal the O.5 ceiling accumulates. It is
// a safety net, not a billing ledger: cache reads are counted the same as fresh input on purpose
// (over-count, never under-count, so an unattended run stops sooner rather than later).
func (u workerUsage) total() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// workerResult is the subset of claude's `-p --output-format json` object we read: the final
// answer text and the run's token usage.
type workerResult struct {
	Result string      `json:"result"`
	Usage  workerUsage `json:"usage"`
}

// runWorker executes the headless, tool-scoped, timeout-bounded claude run. Thread-wiring gives
// the worker recall/remember (Model A — the user's own tokens). Returns the final answer text and the
// total tokens the run reported — 0 means "no usage seen" (unknown, never fatal).
//
// onLog (crank-01) is the LIVE STEP-OUTPUT sink: when non-nil, the worker runs in streaming mode
// (`--output-format stream-json --verbose`) and onLog is called with a human-readable line per step as
// the agent works — so the run detail page can tail the worker like GitHub Actions step logs. When nil
// (interactive `ptln work`, tests), the original buffered `--output-format json` path is used unchanged.
func runWorker(dir, task, model, thread string, allowBash bool, timeout time.Duration, onLog func(string)) (string, int, error) {
	streaming := onLog != nil
	outputFormat := "json"
	if streaming {
		outputFormat = "stream-json" // NDJSON: one event per line, streamed as the agent works
	}
	cargs := []string{"-p", workerPrompt(task, thread != ""), "--output-format", outputFormat, "--allowedTools", strings.Join(workerTools(allowBash), ",")}
	if streaming {
		cargs = append(cargs, "--verbose") // stream-json in -p mode requires --verbose to emit the event stream
	}
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
	if streaming {
		return runWorkerStreaming(ctx, cmd, timeout, onLog)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String() + stderr.String(), 0, fmt.Errorf("hit the %s time budget — stopped", timeout)
	}
	// Parse the JSON result for the answer text + usage. If it isn't that shape (unexpected output,
	// or a non-claude engine later), fall back to the raw output and report unknown (0) usage — the
	// ceiling degrades to "no usage seen → no token halt" rather than crashing.
	if text, tokens, ok := parseWorkerOutput(stdout.Bytes()); ok {
		return text, tokens, err
	}
	return stdout.String() + stderr.String(), 0, err
}

// parseWorkerOutput reads claude's `-p --output-format json` object: the final answer text and the
// total tokens used. ok=false when stdout isn't that JSON shape, so usage is treated as unknown.
func parseWorkerOutput(stdout []byte) (text string, tokens int, ok bool) {
	var r workerResult
	if json.Unmarshal(bytes.TrimSpace(stdout), &r) != nil {
		return "", 0, false
	}
	return r.Result, r.Usage.total(), true
}

// workerNextSteps tells the human how to review — the "results are proposals" invariant made
// visible. In a worktree, review the branch diff; then merge or discard.
func workerNextSteps(dir, worktree string) string {
	if worktree == "" {
		return "→ review the changes in this dir (git diff), then commit/merge yourself."
	}
	return fmt.Sprintf("→ review the worker's branch:\n    cd %s && git status && git diff\n  merge it when you're happy, or drop it:  ptln wt rm %s", dir, worktree)
}
