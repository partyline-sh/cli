package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	eng "partyline.sh/partyline/internal/engine"
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
	task, worktree, thread, model, engine := "", "", "", "", ""
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
		case "--engine":
			if i++; i < len(args) {
				engine = strings.TrimSpace(args[i])
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
		fatal(fmt.Errorf(`usage: ptln work "<task>" [--worktree <name>] [--thread <id>] [--allow-bash] [--model <m>] [--engine <e>] [--timeout 20m]`))
	}
	// Engine (Epic #73): --engine wins; else the cwd project's registered engine; else claude.
	if engine == "" {
		engine = engineForCwd()
	}
	spec, ok := engineSpecFor(engine)
	if !ok {
		fatal(fmt.Errorf("unknown engine %q — valid: claude, codex, gemini, antigravity", engine))
	}
	if _, err := exec.LookPath(spec.Bin); err != nil {
		fatal(fmt.Errorf("%s not found on PATH — the worker runs it headless", spec.Bin))
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

	out, err := runWorker(dir, task, engine, model, thread, allowBash, timeout, nil, "")
	fmt.Println(out.text)
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
// workerSkillManifest is the "Installed skills" line injected into every worker prompt for THIS
// process. It's a package var (not a workerPrompt parameter) so workerPrompt keeps its 2-arg shape and
// existing tests — and it IS genuinely process-global: crank sets it ONCE from the run's org skills
// (all tasks share the same set), and `ptln work` / describe leave it "" so nothing leaks into them.
var workerSkillManifest string

func workerPrompt(task string, thread bool) string {
	var b strings.Builder
	if workerSkillManifest != "" {
		b.WriteString(workerSkillManifest + "\n\n")
	}
	fmt.Fprintf(&b, "You are an autonomous worker in a sandboxed git worktree. Task:\n\n%s\n\n", task)
	b.WriteString("Rules:\n")
	b.WriteString("- Do the task end to end; make the edits directly in this worktree.\n")
	b.WriteString("- Do NOT run git push, git commit to any branch but this one, or anything destructive. Leave your changes uncommitted or committed on THIS branch only — a human reviews and merges.\n")
	b.WriteString("- If you're blocked and would need a tool you don't have (or a destructive action), STOP and clearly explain what you need — do not try to force around it.\n")
	b.WriteString("- You are HEADLESS: no browser, no way to SEE rendered output. For any UI / layout / CSS change you cannot verify it renders correctly — so prefer robust, well-understood patterns (and be wary of fragile ones like grid `auto-rows-fr`, which carries a min-content floor), do NOT claim the visual result works, and in your summary state plainly that the rendering is UNVERIFIED and list exactly what a human must open in a browser and check.\n")
	if thread {
		b.WriteString("- As you settle decisions, constraints, or contracts, record them with the partyline-context-threads `remember` tool (tag `entities`), and `recall` the shared context before assuming.\n")
	}
	b.WriteString("\nWhen finished, end with a short summary: what you changed (files) and anything a reviewer should check.")
	return b.String()
}

// workerResumePrompt frames a RESUMED run (Slice 2): the engine is being re-attached to its prior
// session (via --resume) in the SAME worktree, so its earlier context and edits are intact. The
// prompt is a continuation nudge — finish the remaining work, don't restart — with the same standing
// rules as workerPrompt so a fresh process re-states the guardrails.
func workerResumePrompt(task string, thread bool) string {
	var b strings.Builder
	if workerSkillManifest != "" {
		b.WriteString(workerSkillManifest + "\n\n")
	}
	b.WriteString("You are RESUMING an interrupted autonomous run (a rate limit or timeout stopped you). ")
	b.WriteString("Your earlier context is restored and your edits are still in this worktree. Continue from where you left off and finish the task — do NOT start over. Task, for reference:\n\n")
	fmt.Fprintf(&b, "%s\n\n", task)
	b.WriteString("Rules:\n")
	b.WriteString("- Finish the remaining work; keep your changes committed on THIS branch only or uncommitted. Do NOT git push or commit to another branch — a human reviews and merges.\n")
	b.WriteString("- If you're blocked and would need a tool you don't have (or a destructive action), STOP and clearly explain what you need.\n")
	b.WriteString("- You are HEADLESS: no browser. For any UI / layout / CSS change, do NOT claim the visual result works — state that the rendering is UNVERIFIED and list what a human must check.\n")
	if thread {
		b.WriteString("- As you settle decisions, constraints, or contracts, record them with the partyline-context-threads `remember` tool, and `recall` the shared context before assuming.\n")
	}
	b.WriteString("\nWhen finished, end with a short summary: what you changed (files) and anything a reviewer should check.")
	return b.String()
}

// workerOutcome is everything a worker run reports back to the crank/daemon pipeline: the final
// answer text, token usage, the engine's opaque resume handle (Claude's session id — "" when the
// engine can't resume headless), and the rate-limit reset time (zero = not throttled). Grouped into
// a struct so the pipeline can grow resume/throttle signals without a widening positional return.
type workerOutcome struct {
	text          string
	tokens        int            // O.5 token accounting; 0 = no usage seen
	resumeHandle  string         // Slice 2: engine's opaque per-run resume token; "" = restart-only
	rateReset     rateLimitReset // quota-window reset time; zero = not rate-limited
	invokedSkills []string       // injected skills the agent actually USED this run (claude stream only; nil otherwise)
}

// runWorker executes the headless, tool-scoped, timeout-bounded claude run. Thread-wiring gives
// the worker recall/remember (Model A — the user's own tokens). Returns the final answer text and the
// total tokens the run reported — 0 means "no usage seen" (unknown, never fatal).
//
// onLog (crank-01) is the LIVE STEP-OUTPUT sink: when non-nil, the worker runs in streaming mode
// (`--output-format stream-json --verbose`) and onLog is called with a human-readable line per step as
// the agent works — so the run detail page can tail the worker like GitHub Actions step logs. When nil
// (interactive `ptln work`, tests), the original buffered `--output-format json` path is used unchanged.
// resume (Slice 2) is the engine's opaque session id to CONTINUE — non-empty means "resume the
// prior run in this same worktree from its stored context" (claude -p --resume <id>) rather than
// start fresh. Empty is the normal path. Only Claude supports it today; other engines pass "".
//
// engineName (Epic #73) picks the build engine. Claude keeps the exact pre-adapter path below
// (streaming, MCP thread wiring, resume). Other engines run through the adapter's write posture —
// buffered, no thread wiring yet, resume degrades to restart — with every degradation logged, and
// engines that can't ENFORCE the posture (codex without bash, antigravity at all) refused outright.
func runWorker(dir, task, engineName, model, thread string, allowBash bool, timeout time.Duration, onLog func(string), resume string) (workerOutcome, error) {
	spec, okSpec := engineSpecFor(engineName)
	if !okSpec {
		return workerOutcome{}, fmt.Errorf("unknown engine %q", engineName)
	}
	if spec.Name != "claude" {
		return runWorkerAdapter(spec, dir, task, model, thread, allowBash, timeout, onLog, resume)
	}
	streaming := onLog != nil
	outputFormat := "json"
	if streaming {
		outputFormat = "stream-json" // NDJSON: one event per line, streamed as the agent works
	}
	// The worker records decisions/constraints to Common Ground as it works — but a headless `-p`
	// run cannot answer a permission prompt, so the context-threads MCP tools MUST be pre-allowed or
	// every remember/recall is silently DENIED (which is exactly what happened: runs kept reporting
	// "remember was blocked by a permission prompt"). Allow them only when a thread is wired (the
	// same gate that attaches the MCP server + PARTYLINE_THREAD_ID below).
	tools := workerTools(allowBash)
	if thread != "" {
		tools = append(tools,
			"mcp__partyline-context-threads__remember",
			"mcp__partyline-context-threads__recall",
			"mcp__partyline-context-threads__read_context",
		)
	}
	// On a resume, the engine already holds the prior context; the prompt becomes a short "continue"
	// nudge rather than the full task framing (which would read as "start over"). Empty resume → the
	// normal first-run prompt.
	prompt := workerPrompt(task, thread != "")
	if resume != "" {
		prompt = workerResumePrompt(task, thread != "")
	}
	cargs := []string{"-p", prompt, "--output-format", outputFormat, "--allowedTools", strings.Join(tools, ",")}
	if resume != "" {
		cargs = append(cargs, "--resume", resume) // Slice 2: continue the prior session in this worktree
	}
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
	fmt.Fprintf(os.Stderr, "⚙ worker running (%s budget, tools: %s)…\n", timeout, strings.Join(tools, " "))
	if streaming {
		return runWorkerStreaming(ctx, cmd, timeout, onLog)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return workerOutcome{text: stdout.String() + stderr.String()}, fmt.Errorf("hit the %s time budget — stopped", timeout)
	}
	// Parse the JSON result for the answer text + usage + resume handle. If it isn't that shape
	// (unexpected output, or a non-claude engine later), fall back to the raw output and report
	// unknown (0) usage — the ceiling degrades to "no usage seen → no token halt" rather than
	// crashing. The buffered path carries no rate-limit signal (that rides the stream); zero = none.
	if text, tokens, handle, ok := parseWorkerOutput(stdout.Bytes()); ok {
		return workerOutcome{text: text, tokens: tokens, resumeHandle: handle}, err
	}
	return workerOutcome{text: stdout.String() + stderr.String()}, err
}

// runWorkerAdapter is the non-claude build worker (Epic #73): one buffered engine run at the
// adapter's ToolsWrite posture. Honest degradations, all logged, never silent:
//   - resume: no non-claude engine has headless session resume — the task starts over in the
//     same worktree (the caller's stored handle is ignored; new outcomes carry no handle).
//   - thread wiring: the context-threads MCP hookup is claude-only today — remember/recall are
//     skipped, and the prompt drops the recall/remember instructions accordingly.
//   - streaming: no parseable event stream — onLog gets a heads-up, then the buffered result.
//
// Posture refusals surface the adapter's actionable error (codex needs --allow-bash to build,
// since it can't separate file edits from shell; antigravity has no enforceable headless mode).
func runWorkerAdapter(spec eng.Spec, dir, task, model, thread string, allowBash bool, timeout time.Duration, onLog func(string), resume string) (workerOutcome, error) {
	note := func(s string) {
		if onLog != nil {
			onLog(s)
		}
		fmt.Fprintf(os.Stderr, "  (%s)\n", s)
	}
	if resume != "" {
		note(fmt.Sprintf("engine %s has no headless resume — starting this task over in the same worktree", spec.Name))
	}
	if thread != "" {
		note(fmt.Sprintf("context-thread tools aren't wired for %s workers yet — remember/recall skipped for this task", spec.Name))
	}
	argv, stdinPrompt, err := spec.OneShotArgs(workerPrompt(task, false), model, eng.ToolsWrite(allowBash))
	if err != nil {
		return workerOutcome{}, err
	}
	if onLog != nil {
		note(fmt.Sprintf("engine %s does not stream; buffering output…", spec.Name))
	}
	fmt.Fprintf(os.Stderr, "⚙ worker running (%s budget, engine: %s)…\n", timeout, spec.Name)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runOneShot(ctx, dir, argv, stdinPrompt)
	if ctx.Err() == context.DeadlineExceeded {
		return workerOutcome{text: string(out)}, fmt.Errorf("hit the %s time budget — stopped", timeout)
	}
	// No usage/handle: non-claude envelopes carry neither, so the token ceiling degrades to
	// "no usage seen" (never a false halt) and the run is restart-only — both by design.
	return workerOutcome{text: oneShotText(spec, out)}, err
}

// parseWorkerOutput reads claude's `-p --output-format json` object: the final answer text, the
// total tokens used, and the session id (resume handle). ok=false when stdout isn't that JSON
// shape, so usage is treated as unknown and the handle is empty (restart-only). The envelope
// parse itself lives in internal/engine (the ONE copy) — this is the ok-shaped adapter the
// worker/describe/verify/review call sites use.
func parseWorkerOutput(stdout []byte) (text string, tokens int, handle string, ok bool) {
	spec, _ := eng.Lookup("claude")
	res, err := spec.ParseResult(stdout)
	if err != nil {
		return "", 0, "", false
	}
	return res.Text, res.Usage.Total(), res.SessionID, true
}

// workerNextSteps tells the human how to review — the "results are proposals" invariant made
// visible. In a worktree, review the branch diff; then merge or discard.
func workerNextSteps(dir, worktree string) string {
	if worktree == "" {
		return "→ review the changes in this dir (git diff), then commit/merge yourself."
	}
	return fmt.Sprintf("→ review the worker's branch:\n    cd %s && git status && git diff\n  merge it when you're happy, or drop it:  ptln wt rm %s", dir, worktree)
}
