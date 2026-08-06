package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
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

// workerToolGrants (#575) are the run's BUILD-role tool grants — set ONCE by crank from the
// daemon-written --grants-file (all tasks in a run share the project's grants, so a package var
// is correct, exactly like workerSkillManifest). `ptln work` leaves it nil: a human-driven local
// run grants nothing implicitly. Every entry is re-validated at use (resolveLaunchGrants).
var workerToolGrants *api.ToolGrants

// workerBashPosture states, UP FRONT, whether the worker can run shell commands — so it never
// discovers the answer by hitting a wall of permission denials. Without Bash it burned ~12M tokens
// on one run retrying npm/tsc/vitest/git 25+ times (and spawning a sub-agent to retry more); this
// line is the fix. With Bash it's told to actually verify (run the tests) before finishing.
func workerBashPosture(allowBash bool) string {
	// #575: granted command prefixes are pre-approved even without general Bash — the posture
	// line must say so, or the worker (told "every attempt is denied") never uses them.
	granted := ""
	if workerToolGrants != nil && len(workerToolGrants.Shell) > 0 {
		granted = strings.Join(workerToolGrants.Shell, ", ")
	}
	if !allowBash {
		s := "- You do NOT have general shell/Bash access in this run. Do NOT attempt to run tests, builds, linters, `npm`, `tsc`, `vitest`, `go`, `git`, or any shell command, and do NOT spawn sub-agents to run them — those tools are unavailable and every attempt is denied, so trying only wastes the run. Edit files directly; a separate review verifies your work. If the task genuinely cannot be done without running a command, STOP and say so.\n"
		if granted != "" {
			s += "- EXCEPTION: these specific command prefixes ARE granted and pre-approved — use them freely when the task needs them: " + granted + ".\n"
		}
		return s
	}
	return "- You HAVE shell/Bash access. Before you finish, VERIFY your change: run the project's typecheck and tests (use the scripts in package.json / Makefile / README — commonly `npm test`, `npx tsc --noEmit`, `go test ./...`). Report exactly what you ran and the result; if you could not verify, say so plainly.\n"
}

// workerContextCap bounds the injected recall slice so a large thread can't blow up the worker
// prompt. This is the crude Phase-2 selector (whole thread, capped); Phase 3 (pgvector) makes the
// SELECTION relevant per-task rather than "the recent tail". A var so a test can shrink it.
var workerContextCap = 8 << 10

// workerContext fetches the run's thread context and frames it as "background the builder already
// knows" — the piece that turns a captured thread from write-only into something a crank run READS.
// Best-effort, exactly like threadPrimer: returns "" on any error (not logged in, no thread, network)
// so a build never blocks on it. The task drives SELECTION: selectContextBlocks ranks facts by how
// well their `entities` anchors match what the task is about, so a big thread contributes the RELEVANT
// slice within workerContextCap rather than just its recent tail.
func workerContext(threadID, task string) string {
	if threadID == "" || api.LoadToken() == "" {
		return ""
	}
	_, blocks, err := api.New().GetThread(threadID)
	if err != nil || len(blocks) == 0 {
		return ""
	}
	facts := formatContextBlocks(selectContextBlocks(blocks, task, workerContextCap))
	if strings.TrimSpace(facts) == "" || strings.HasPrefix(facts, "No shared context") {
		return ""
	}
	return facts
}

// selectContextBlocks picks the most relevant LIVE blocks for a task, within a byte budget — the
// crude Phase-3 "Select" step built on the `entities` anchors the scribe already emits (file:/dir:/
// pkg:/symbol:/concept:), NO embeddings needed. Ordering: (1) every overview (orientation a builder
// always wants), then (2) non-overview facts ranked by entity/keyword match to the task, then (3) if
// budget remains, the most RECENT unmatched facts (graceful fallback to "recent tail" when nothing
// matches — the pre-select behavior). Pure + deterministic for a given input, so it's unit-tested.
// Phase-3-proper (pgvector) later swaps the scorer for semantic similarity; this seam stays.
func selectContextBlocks(blocks []api.ContextBlock, task string, capBytes int) []api.ContextBlock {
	taskLower := strings.ToLower(task)
	live := make([]api.ContextBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Status == "superseded" || b.Status == "proposed" || b.Status == "pruned" {
			continue
		}
		live = append(live, b)
	}
	// Partition: overviews always in; the rest get scored.
	type scored struct {
		blk   api.ContextBlock
		score int
	}
	var overviews []api.ContextBlock
	var rest []scored
	for _, b := range live {
		if b.Kind == "overview" {
			overviews = append(overviews, b)
			continue
		}
		rest = append(rest, scored{b, blockRelevance(b, taskLower)})
	}
	// Stable sort: matched (score>0) first by score desc, then everything by recency (id desc) —
	// so unmatched blocks tail in newest-first, preserving the old "recent tail" fallback.
	sort.SliceStable(rest, func(i, j int) bool {
		if (rest[i].score > 0) != (rest[j].score > 0) {
			return rest[i].score > 0 // matched ahead of unmatched
		}
		if rest[i].score != rest[j].score {
			return rest[i].score > rest[j].score
		}
		return rest[i].blk.ID > rest[j].blk.ID // recency tie-break
	})
	// Fill the budget: overviews always count first, then ranked rest.
	out := make([]api.ContextBlock, 0, len(live))
	used := 0
	add := func(b api.ContextBlock) bool {
		cost := len(b.Body) + 48 // ~ the "• #id [kind] body — author {entities}" envelope
		for _, e := range b.Entities {
			cost += len(e) + 2
		}
		if used > 0 && used+cost > capBytes {
			return false
		}
		out = append(out, b)
		used += cost
		return true
	}
	for _, b := range overviews {
		add(b) // always attempt; the first block is admitted even if it alone exceeds cap
	}
	for _, s := range rest {
		if !add(s.blk) {
			break // budget hit — the rest is lower-ranked, so stop
		}
	}
	return out
}

// blockRelevance scores one block against the (lowercased) task by its entity anchors, with a light
// body-keyword fallback. A typed anchor (file:/dir:/pkg:/symbol:/concept:) is stripped to its value,
// then that value — and each meaningful path segment of it — is checked as a substring of the task;
// a hit is worth more than a bare-word body match. 0 = no signal (block is unmatched).
func blockRelevance(b api.ContextBlock, taskLower string) int {
	score := 0
	for _, e := range b.Entities {
		val := e
		if i := strings.IndexByte(e, ':'); i >= 0 {
			val = e[i+1:] // strip file:/dir:/pkg:/symbol:/concept:
		}
		val = strings.ToLower(strings.TrimSpace(val))
		if val == "" {
			continue
		}
		if strings.Contains(taskLower, val) {
			score += 3 // whole anchor named in the task — strong signal
			continue
		}
		for _, seg := range strings.FieldsFunc(val, func(r rune) bool { return r == '/' || r == '-' || r == '_' || r == '.' }) {
			if len(seg) >= 4 && strings.Contains(taskLower, seg) {
				score += 1 // a path/name segment appears — weak signal
			}
		}
	}
	return score
}

func workerPrompt(task string, ctx string, thread, allowBash bool) string {
	var b strings.Builder
	if workerSkillManifest != "" {
		b.WriteString(workerSkillManifest + "\n\n")
	}
	fmt.Fprintf(&b, "You are an autonomous worker in a sandboxed git worktree. Task:\n\n%s\n\n", task)
	if ctx != "" {
		// Injected shared context: background the builder already knows (NOT a to-do). This is the
		// read side of Context Threads — the run starts warm with the team's decisions/constraints/
		// contracts instead of having to remember to `recall`.
		b.WriteString("## Shared team context (background you already know)\n\n")
		b.WriteString("The team has recorded these durable facts on this project's context thread. Treat them as background you can rely on — do NOT act on them or change anything because of them unless the task asks. If any is stale or contradicts what you find in the code, trust the code and note the discrepancy.\n\n")
		b.WriteString(ctx)
		b.WriteString("\n\n")
	}
	b.WriteString("Rules:\n")
	b.WriteString("- Do the task end to end; make the edits directly in this worktree.\n")
	b.WriteString("- Do NOT run git push, git commit to any branch but this one, or anything destructive. Leave your changes uncommitted or committed on THIS branch only — a human reviews and merges.\n")
	b.WriteString(workerBashPosture(allowBash))
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
func workerResumePrompt(task string, thread, allowBash bool) string {
	var b strings.Builder
	if workerSkillManifest != "" {
		b.WriteString(workerSkillManifest + "\n\n")
	}
	b.WriteString("You are RESUMING an interrupted autonomous run (a rate limit or timeout stopped you). ")
	b.WriteString("Your earlier context is restored and your edits are still in this worktree. Continue from where you left off and finish the task — do NOT start over. Task, for reference:\n\n")
	fmt.Fprintf(&b, "%s\n\n", task)
	b.WriteString("Rules:\n")
	b.WriteString("- Finish the remaining work; keep your changes committed on THIS branch only or uncommitted. Do NOT git push or commit to another branch — a human reviews and merges.\n")
	b.WriteString(workerBashPosture(allowBash))
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
	text            string
	tokens          int            // O.5 ceiling signal — Total (incl. cache reads). Over-counts on purpose; NOT for display.
	freshTokens     int            // DISPLAY token spend: input+output (new I/O only; excludes cached context); 0 = no usage seen
	cacheReadTokens int            // cache_read only — a muted "+N cached" detail, never the headline
	costUSD         float64        // claude's total_cost_usd for this run (0 = not reported)
	resumeHandle    string         // Slice 2: engine's opaque per-run resume token; "" = restart-only
	rateReset       rateLimitReset // quota-window reset time; zero = none given (NOT "not blocked")
	rateBlocked     bool           // the provider refused. Separate from rateReset because an ENTITLEMENT
	// block ("credits required for this model") carries no reset time — treating a zero reset as
	// "not blocked" is exactly what used to make those runs die as a bare `exit status 1`.
	rateNote      string   // the provider's own wording for the block, when it gave us one
	invokedSkills []string // injected skills the agent actually USED this run (claude stream only; nil otherwise)
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
	// #575: build-role tool grants WIDEN the allowlist — targeted Bash(prefix:*) rules and
	// locally-resolved MCP servers, never a blanket Bash. Same resolver + audit posture as
	// planning launches; invalid/unknown entries are skipped, never widened.
	grantMCPConfig := ""
	if workerToolGrants != nil {
		extra, gcfg, notes := resolveLaunchGrants(workerToolGrants, loadMCPCatalog())
		for _, n := range notes {
			fmt.Fprintf(os.Stderr, "  (build grants: %s)\n", n)
		}
		if len(extra) > 0 {
			fmt.Fprintf(os.Stderr, "  build grants applied → %s\n", strings.Join(extra, " "))
			tools = append(tools, extra...)
		}
		grantMCPConfig = gcfg
	}
	// On a resume, the engine already holds the prior context; the prompt becomes a short "continue"
	// nudge rather than the full task framing (which would read as "start over"). Empty resume → the
	// normal first-run prompt.
	prompt := workerPrompt(task, workerContext(thread, task), thread != "", allowBash)
	if resume != "" {
		// Resume: the engine already holds the prior session's context (incl. any recall it did on the
		// first run), so re-injecting would bloat a deliberately-short continuation nudge. Skip it.
		prompt = workerResumePrompt(task, thread != "", allowBash)
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
	if grantMCPConfig != "" {
		// A second --mcp-config is fine — claude merges repeated flags (proven by the party
		// spawn, which already carries two).
		cargs = append(cargs, "--mcp-config", grantMCPConfig)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", cargs...)
	cmd.Dir = dir
	// #814: own process group, group teardown on cancel, bounded post-exit pipe wait. Without this a
	// worker's MCP children inherit the output pipe and Wait blocks forever — the timeout below
	// cannot fire, which is how one worker stayed resident for seven days.
	groupSpawn(cmd)
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
	if spec, ok := eng.Lookup("claude"); ok {
		if res, perr := spec.ParseResult(stdout.Bytes()); perr == nil {
			return workerOutcome{
				text: res.Text, tokens: res.Usage.Total(), freshTokens: res.Usage.Fresh(),
				cacheReadTokens: res.Usage.CacheReadInputTokens, costUSD: res.CostUSD, resumeHandle: res.SessionID,
			}, err
		}
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
		note(fmt.Sprintf("context-thread WRITE tools (remember/recall) aren't wired for %s workers yet — but the shared context is injected into the prompt read-only", spec.Name))
	}
	if workerToolGrants != nil {
		note(fmt.Sprintf("tool grants not yet applied for %s workers — allowlist posture unchanged", spec.Name))
	}
	// Non-claude engines can't call the recall MCP tool, but injecting the context slice into the
	// prompt is engine-agnostic — so they still start warm with the team's facts (just can't write back).
	argv, stdinPrompt, err := spec.OneShotArgs(workerPrompt(task, workerContext(thread, task), false, allowBash), model, eng.ToolsWrite(allowBash))
	if err != nil {
		return workerOutcome{}, err
	}
	if onLog != nil {
		note(fmt.Sprintf("engine %s does not stream; buffering output…", spec.Name))
	}
	fmt.Fprintf(os.Stderr, "⚙ worker running (%s budget, engine: %s)…\n", timeout, spec.Name)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runOneShot(ctx, dir, argv, stdinPrompt, spec.OneShotEnv(eng.ToolsWrite(allowBash))...)
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
