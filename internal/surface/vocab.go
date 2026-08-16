package surface

// The declared vocabularies. Each one is the ONLY place its members are written down; TypeScript
// unions, CHECK constraints, docs tables, and UI copy keys are generated from here.
//
// Adding a member is therefore a one-line change plus a generated migration — not the three-places
// edit that produced constraint #195.

// RunStatus is the run lifecycle. Values match the runs_status_check constraint as of migration
// 20260720000000_run_pause.sql.
var RunStatus = Vocab{
	Name:   "run_status",
	Column: "runs.status",
	Doc:    "Where a run is in its lifecycle. The board's columns are derived from this, not stored.",
	Terms: []Term{
		{Key: "queued", Doc: "Enqueued and waiting. A queued run has not been dispatched to any machine; the Start action moves it on."},
		{Key: "accepted", Doc: "Dispatched to a daemon, which has not yet reported the process as running."},
		{Key: "declined", Doc: "The machine's operator refused the run at the console confirm."},
		{Key: "running", Doc: "A worker process is live on the machine and streaming logs."},
		{Key: "needs_approval", Doc: "Paused awaiting a human decision. See pause_reason for what the decision is — the status alone does not say, which is why some pauses used to surface with nothing to decide."},
		{Key: "paused", Doc: "Held mid-task by an operator. The worker process is SIGSTOPped and still resident, so resuming continues exactly where it stopped and rebuilds nothing."},
		{Key: "done", Doc: "Every task reached a terminal state and the run finished."},
		{Key: "failed", Doc: "The run ended without completing its worklist."},
		{Key: "killed", Doc: "Cancelled by a human before it finished."},
	},
}

// TaskStatus is the per-task lifecycle inside a run (run_tasks.status, migration 0037).
var TaskStatus = Vocab{
	Name:   "task_status",
	Column: "run_tasks.status",
	Doc:    "The lifecycle of one task within a run. Reported by the worker, except `done`, which the control plane derives from a gate report.",
	Terms: []Term{
		{Key: "queued", Doc: "Seeded on the run and not yet attempted."},
		{Key: "running", Doc: "A worker is building this task in its own git worktree."},
		{Key: "blocked", Doc: "Stopped short of completion with work worth keeping — a failed verify gate, or a provider rate limit mid-task. The branch survives for a human or a resume."},
		{Key: "done", Doc: "Completed and, where a gate is configured, verified. Never self-reported by the worker."},
		{Key: "failed", Doc: "The attempt errored and left nothing worth resuming."},
	},
}

// PauseReason is NEW (Epic G.2). It splits the four unrelated situations that all currently
// present as needs_approval, so each can offer the actions that actually apply to it.
var PauseReason = Vocab{
	Name:   "pause_reason",
	Column: "runs.pause_reason",
	Doc:    "Why a run is paused. needs_approval means 'a human may be needed'; this says what for. Each reason has its own action set, and one of them has no actions at all.",
	Terms: []Term{
		{Key: "budget", Doc: "The run hit its token ceiling at a task boundary. Actions: approve more budget, remove the limit, or stop."},
		{Key: "rate_limit", Doc: "The engine provider throttled us and the run holds until the quota resets. NO ACTION IS REQUIRED — a scheduled job resumes it at resume_at. The UI shows a countdown and no buttons."},
		{Key: "entitlement", Doc: "The provider refused on billing rather than on rate: usage credits required, org overage disabled, or the model not enabled. Distinct from rate_limit because WAITING CANNOT FIX IT — a quota reset clears a rate limit; only a human changing billing clears this. Showing a countdown here sends the operator to wait for a moment that never comes. Actions: fix billing or switch model, then continue."},
		{Key: "quarantine", Doc: "A verify gate rejected at least one task. Actions: review the branch, merge anyway, send back for repair, or discard."},
		{Key: "stall", Doc: "The run stopped producing output or its machine went quiet. Actions: continue, restart, or cancel."},
	},
}

// Preset selects what kind of job a run is. This is the vocabulary of constraint #195: it must
// agree with runs_preset_check, and generating both from here is the fix.
var Preset = Vocab{
	Name:   "preset",
	Column: "runs.preset",
	Doc:    "What kind of job a run is. The daemon maps a preset to a fixed argv; no part of it is ever supplied by the control plane as a command.",
	Terms: []Term{
		{Key: "spec", Doc: "The default build posture: a worker with edit tools and no shell."},
		{Key: "build", Doc: "A build with shell access, for tasks whose verification needs to run commands."},
		{Key: "chat", Doc: "An interactive party session rather than an autonomous run."},
		{Key: "describe", Doc: "A planning job that produces work items instead of a branch. Hidden from the build board."},
		{Key: "review", Doc: "A hidden run that reviews another run's branches and records an advisory grade."},
		{Key: "rebase", Doc: "A hidden run that rebases a conflicted branch onto its base and resolves what it can."},
	},
}

// MergePolicy is the per-run choice of what happens to a verified branch.
var MergePolicy = Vocab{
	Name:   "merge_policy",
	Column: "runs.merge_policy",
	Doc:    "What a completed, verified task does with its branch. The default proposes; it never pushes to a protected branch on its own.",
	Terms: []Term{
		{Key: "manual", Doc: "Leave a reviewable branch and open nothing. The default, and the posture of a local `ptln crank`."},
		{Key: "pr", Doc: "Push the branch and open a pull request for a human to merge."},
		{Key: "auto", Doc: "Open a PR and enable auto-merge, but only when the base branch has required status checks configured. Without a CI gate the PR is left open."},
	},
}

// Engine is the closed set of AI CLIs a daemon knows how to spawn. Mirrors web/src/lib/engines.ts
// ENGINES and the CLI's validEngine; those become generated from here.
var Engine = Vocab{
	Name: "engine",
	Doc:  "The AI CLI a job runs on. Models are free-form and engine-defined; the engine itself is a closed set, validated everywhere it is accepted.",
	Terms: []Term{
		{Key: "claude", Doc: "Claude Code. The first-class integration: the only engine with vision in headless mode and verified MCP context-thread wiring."},
		{Key: "codex", Doc: "OpenAI Codex CLI. Headless-capable; MCP wiring is experimental and gated."},
		{Key: "gemini", Doc: "Gemini CLI. Headless-capable; no MCP wiring, so context threads are unavailable."},
		{Key: "opencode", Doc: "opencode (>= 0.25). Headless with a per-invocation deny-by-default permission config, and the gateway to open-weight models."},
		{Key: "goose", Doc: "Block's goose. Headless-capable."},
		{Key: "antigravity", Doc: "Interactive and planning use only. Its sole headless mode is a full permissions bypass we never pass, so the daemon refuses it for build and review."},
	},
}

// GateVerdict is NEW (Epic G.0): the outcome of the whole verify gate for one task.
var GateVerdict = Vocab{
	Name: "gate_verdict",
	Doc:  "The verify gate's judgment on one task's branch. `skipped` is deliberately distinct from `pass`: a repo that configured no checks has not proved anything.",
	Terms: []Term{
		{Key: "pass", Doc: "Every enabled lane passed and raised nothing."},
		{Key: "pass_with_findings", Doc: "No lane blocked, but reviewers raised non-blocking findings. The branch merges and the findings ride on the pull request."},
		{Key: "fail", Doc: "At least one blocking lane rejected. The task is quarantined: pushed for review, never merged."},
		{Key: "blocked", Doc: "The gate could not reach a judgment for infrastructure reasons — a provider outage, a timeout. Fail-closed: treated as needing a human, never as a pass."},
		{Key: "skipped", Doc: "No lane was enabled for this repo. Honest absence of evidence, not evidence of correctness."},
	},
}

// GateCode is NEW (Epic G.0/G.2): the machine-readable reason behind a lane's outcome. This is the
// classed vocabulary — each code declares whether a retry could plausibly fix it.
var GateCode = Vocab{
	Name:    "gate_code",
	Classed: true,
	Doc:     "Why a gate lane produced the result it did. Every code carries a retry disposition, which is what lets the system retry a throttled provider without asking a human, and never retry a rejected diff.",
	Terms: []Term{
		{Key: "ok", Class: ClassNone, Doc: "The lane passed."},
		{Key: "skipped", Class: ClassNone, Doc: "The lane was not enabled for this repo."},
		{Key: "check.failed", Class: ClassHard, Doc: "An acceptance check exited non-zero on the task's branch and passes on the base, so the diff introduced the regression."},
		{Key: "check.timeout", Class: ClassTransient, Doc: "An acceptance check exceeded its timeout. Transient on first occurrence; a check that times out twice is broken rather than slow, and is reclassified hard."},
		{Key: "check.baseline_red", Class: ClassNone, Doc: "The check fails on the base branch too. Pre-existing debt this diff did not introduce — recorded as an advisory, never a quarantine."},
		{Key: "reviewer.rejected", Class: ClassHard, Doc: "The independent reviewer judged that the diff does not satisfy the task."},
		{Key: "reviewer.unparseable", Class: ClassHard, Doc: "The reviewer's reply carried no verdict we could read. Fail-closed: an unreadable answer is not a pass."},
		{Key: "reviewer.timeout", Class: ClassTransient, Doc: "The reviewer engine exceeded its timeout without answering."},
		{Key: "reviewer.no_diff", Class: ClassHard, Doc: "There was no committed change to review. A task that produced nothing has not been verified by anything."},
		{Key: "visual.rejected", Class: ClassHard, Doc: "The vision reviewer looked at the rendered change and judged it wrong."},
		{Key: "visual.no_renderer", Class: ClassNone, Doc: "Visual verification is enabled but no renderer resolved for this repo. Advisory: the gate warns and executes nothing, because the alternative is running something the control plane supplied."},
		{Key: "readonly.mutated", Class: ClassHard, Doc: "A gate lane modified the worktree it was judging — a changed file, a moved HEAD, or a new stash entry. An integrity failure of the harness, not a defect in the code under review."},
		{Key: "provider.rate_limited", Class: ClassTransient, Doc: "The engine provider throttled the request. The run pauses until the quota resets and then continues itself."},
		{Key: "provider.timeout", Class: ClassTransient, Doc: "The engine did not respond within the allotted time."},
		{Key: "provider.unavailable", Class: ClassTransient, Doc: "The engine provider returned a server-side error or could not be reached."},
		{Key: "engine.unknown", Class: ClassHard, Doc: "The configured engine is not one this daemon version knows how to spawn."},
		{Key: "engine.launch_failed", Class: ClassTransient, Doc: "The engine binary could not be started."},
	},
}

// Go constants for the values referenced from code. Declaring them here — beside the vocabulary
// rather than as string literals at call sites — means a typo is a compile error instead of a
// value the database rejects at runtime. TestConstantsAreDeclaredTerms asserts every constant
// below is an actual member, so the two cannot drift apart.
const (
	VerdictPass             = "pass"
	VerdictPassWithFindings = "pass_with_findings"
	VerdictFail             = "fail"
	VerdictBlocked          = "blocked"
	VerdictSkipped          = "skipped"

	PauseBudget      = "budget"
	PauseRateLimit   = "rate_limit"
	PauseEntitlement = "entitlement"
	PauseQuarantine  = "quarantine"
	PauseStall       = "stall"

	CodeOK                  = "ok"
	CodeSkipped             = "skipped"
	CodeCheckFailed         = "check.failed"
	CodeCheckTimeout        = "check.timeout"
	CodeCheckBaselineRed    = "check.baseline_red"
	CodeReviewerRejected    = "reviewer.rejected"
	CodeReviewerUnparseable = "reviewer.unparseable"
	CodeReviewerTimeout     = "reviewer.timeout"
	CodeReviewerNoDiff      = "reviewer.no_diff"
	CodeVisualRejected      = "visual.rejected"
	CodeVisualNoRenderer    = "visual.no_renderer"
	CodeReadOnlyMutated     = "readonly.mutated"
	CodeProviderRateLimited = "provider.rate_limited"
	CodeProviderTimeout     = "provider.timeout"
	CodeProviderUnavailable = "provider.unavailable"
	CodeEngineUnknown       = "engine.unknown"
	CodeEngineLaunchFailed  = "engine.launch_failed"
)
