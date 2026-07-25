// engine_oneshot.go — package main's glue around internal/engine for HEADLESS one-shot
// runs (the graded reviewer, the T2b verify reviewer, describe turns, the evidence
// verifier): pick the engine (server-sent > local registry > claude), build the argv at
// the strongest posture the engine can ENFORCE, exec it, and read the final text back.
// Interactive party turns (party_agent.go) and crank workers (work.go — E4) don't come
// through here.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	eng "partyline.sh/partyline/internal/engine"
)

// preferEngine resolves the effective engine from the LOCAL registry value and an optional
// server-sent override: a VALID server engine wins; empty/unknown keeps local ("" = claude,
// the default). note (when non-empty) is a one-line human notice — an ignored unknown value
// or an override — for the caller's log. Shape-validation happens HERE (the engines registry
// keys, via validEngine), so an injection-shaped server value can never reach an argv: it
// simply falls back, with a notice.
func preferEngine(local, server string) (engine, note string) {
	switch {
	case server == "":
		return local, ""
	case !validEngine(server):
		return local, fmt.Sprintf("ignoring unknown server engine %q — using local %q", server, engineLabel(local))
	case engineLabel(server) != engineLabel(local):
		return server, fmt.Sprintf("server engine %q overrides local %q", server, engineLabel(local))
	default:
		return server, "" // same effective engine — no notice
	}
}

// resolveRunEngine picks the engine for a daemon-executed one-shot job (preset "describe" /
// "review"): the RunEvent's server-sent engine when valid, else the local registry's
// per-project engine, else claude — the same pecking order as resolveLaunch. Returns the
// canonical engine name (never "") plus preferEngine's notice for the run log ("" = none).
func resolveRunEngine(reg daemonRegistry, label, serverEngine string) (engine, note string) {
	local := ""
	if p := projectByLabel(reg, label); p != nil {
		local = p.Engine
	}
	e, note := preferEngine(local, serverEngine)
	return engineLabel(e), note
}

// engineForCwd resolves the engine for a CLI command run from inside a project checkout
// (`ptln describe`): the local registry's per-project engine for the cwd-inferred project
// (the same longest-path match resolveDescribeThread relies on). claude when cwd is in no
// registered project, the project has no engine set, or a hand-edited registry holds an
// unknown value (owner-authored, so a quiet fallback — not an attack surface).
func engineForCwd() string {
	if label := projectLabelForCwd(); label != "" {
		if p := projectByLabel(loadDaemonRegistry(), label); p != nil && validEngine(p.Engine) {
			return p.Engine
		}
	}
	return "claude"
}

// engineSpecFor looks up the adapter Spec for a (possibly empty) engine name — "" is claude,
// mirroring engineLabel. ok=false for a name outside the registry.
func engineSpecFor(name string) (eng.Spec, bool) {
	return eng.Lookup(engineLabel(name))
}

// reviewerOneShot builds a one-shot argv for an INDEPENDENT reviewer at the strongest
// posture the engine can enforce: ToolsNone first; when the engine has no tool-less mode
// (codex, gemini) it downgrades ONCE to ToolsReadOnly — the only sanctioned downgrade,
// always logged via logf, never silent. An engine that can enforce neither posture
// (antigravity) returns the ToolsNone error so the caller fails closed.
// graderOneShot builds the REVIEW AGENT's argv: read-only tools PREFERRED, tool-less as the
// fallback. The opposite preference from reviewerOneShot (the inline verify gate) on purpose: the
// grader's job is to CHECK claims — "this already exists at file:line", the criteria's own verify
// hints — and a tool-less model can't check anything, it can only narrate. That's not hypothetical:
// tool-less graders CLAIMED verification anyway ("confirmed via grep across the repo") and were
// wrong — one fabricated a build-breaking-import finding that an actual typecheck disproves, and
// graded the run F on it. Read-only tools + a truthful checkout turn "verified" from prose into fact.
func graderOneShot(spec eng.Spec, prompt, model string, logf func(string)) (argv []string, stdinPrompt string, err error) {
	argv, stdinPrompt, err = spec.OneShotArgs(prompt, model, eng.ToolsReadOnly)
	if err == nil {
		return argv, stdinPrompt, nil
	}
	nargv, nstdin, nerr := spec.OneShotArgs(prompt, model, eng.ToolsNone)
	if nerr != nil {
		return nil, "", err
	}
	if logf != nil {
		logf(fmt.Sprintf("engine %s has no read-only mode; grader runs tool-less (verification limited to the diff text)", spec.Name))
	}
	return nargv, nstdin, nil
}

func reviewerOneShot(spec eng.Spec, prompt, model string, logf func(string)) (argv []string, stdinPrompt string, err error) {
	argv, stdinPrompt, err = spec.OneShotArgs(prompt, model, eng.ToolsNone)
	if err == nil {
		return argv, stdinPrompt, nil
	}
	rargv, rstdin, rerr := spec.OneShotArgs(prompt, model, eng.ToolsReadOnly)
	if rerr != nil {
		return nil, "", err // neither posture enforceable — surface the ToolsNone refusal
	}
	if logf != nil {
		logf(fmt.Sprintf("engine %s has no tool-less mode; reviewer runs read-only", spec.Name))
	}
	return rargv, rstdin, nil
}

// runOneShot executes a built one-shot argv in dir with the independence env (PARTYLINE=1 —
// no thread wiring, verifier ≠ producer) and returns its stdout. stdinPrompt (codex) is fed
// on stdin; for the other engines the prompt is already inside argv. ctx bounds the run —
// callers check ctx.Err() for their own timeout wording.
func runOneShot(ctx context.Context, dir string, argv []string, stdinPrompt string, extraEnv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	// extraEnv carries per-engine posture enforcement that lives in config rather than argv
	// (opencode's OPENCODE_CONFIG_CONTENT permission block — see Spec.OneShotEnv). Appended
	// after os.Environ so the posture wins over any user-level value.
	cmd.Env = append(append(os.Environ(), "PARTYLINE=1"), extraEnv...)
	if stdinPrompt != "" {
		cmd.Stdin = strings.NewReader(stdinPrompt)
	}
	return cmd.Output()
}

// oneShotText reads a one-shot run's stdout into the final reply text: claude's json
// envelope → its result field; every other engine → the raw output. A malformed claude
// envelope falls back to the raw output (the pre-adapter parseWorkerOutput ok=false path),
// so the caller's own reply parse decides pass/fail rather than this layer.
func oneShotText(spec eng.Spec, stdout []byte) string {
	if res, err := spec.ParseResult(stdout); err == nil {
		return res.Text
	}
	return string(stdout)
}
