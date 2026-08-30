package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

// The daemon side of preset `report` — an agent that looks and tells you.
//
// WHAT MAKES THIS DIFFERENT FROM EVERY OTHER RUN. There is no worktree, no commit, no branch and
// nothing to merge. A trigger fires, this reads the repo AS IT IS, writes an evaluation, and stops.
//
// The guarantee is the TOOL POSTURE, not the prompt. eng.ToolsReadOnly hard-disallows
// Bash/Edit/Write/MultiEdit/WebFetch/Task and drops project MCP entirely (see
// internal/engine/oneshot.go), so "it cannot open a pull request" is enforced by the argv rather
// than requested in English. A prompt that asks an agent not to commit is a hope; a posture that
// denies it the tool is a control. That distinction is the whole point of this preset — asking
// nicely is what produced eight branches of markdown from eight blocked deploys.

const reportTimeout = 5 * time.Minute

// reportPrompt frames the evaluation and the exact VERDICT line parsed back, mirroring the
// adversarial reviewer's convention in verify.go so there is one shape to learn.
//
// The verdict wording matters: `attention` must mean "a human needs to look", NOT "something is
// broken". A deploy guard that fired correctly is working-as-intended and still needs nobody, while
// a silently-reverting merge needs somebody even though nothing failed. Getting an agent to sort on
// "does this need a human" rather than "did something fail" is what keeps the channel worth reading.
func reportPrompt(task string) string {
	var b strings.Builder
	b.WriteString("You are an investigator. Something happened and you have been woken to explain it.\n\n")
	b.WriteString("You can READ this repository and nothing else. You cannot run commands, edit files, open a\n")
	b.WriteString("pull request, or change anything — and you are not expected to. Your entire output is the\n")
	b.WriteString("explanation below. Do not describe changes as if you had made them.\n\n")
	b.WriteString("IF YOU FIND A FIX, DESCRIBE IT — do not attempt it. Say what you would change and where, in\n")
	b.WriteString("enough detail that a human can start the work. Proposing is useful; pretending is not.\n\n")
	b.WriteString("WHAT HAPPENED:\n\n")
	// The inbound text is DATA — a webhook body a stranger can write. Fenced and labelled so it
	// cannot read as instructions to you, the same treatment triggers give it server-side.
	b.WriteString("```\n" + task + "\n```\n\n")
	b.WriteString("Write a short evaluation in markdown: what happened, why, and what (if anything) a human\n")
	b.WriteString("should do. Lead with the answer, not the investigation. Cite the files you read.\n\n")
	b.WriteString("Then end your reply with EXACTLY one line:\n")
	b.WriteString("VERDICT: ok — <one line: what happened and why nobody needs to act>\n")
	b.WriteString("or\n")
	b.WriteString("VERDICT: attention — <one line: what a human needs to look at>\n\n")
	b.WriteString("Choose `ok` when the system behaved correctly, EVEN IF something failed — a guard that\n")
	b.WriteString("refused a bad deploy did its job. Choose `attention` when a human must decide or act.\n")
	return b.String()
}

// parseReportVerdict reads the trailing VERDICT line.
//
// FAIL-CLOSED, deliberately: an unreadable verdict becomes `attention`. A report nobody could judge
// is a report nobody has judged, and defaulting that to all-clear is exactly how the one finding
// that mattered gets buried. The server applies the same rule independently.
func parseReportVerdict(reply string) (verdict, reason string) {
	lines := strings.Split(reply, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(strings.ToUpper(ln), "VERDICT:") {
			continue
		}
		v := strings.TrimSpace(ln[len("VERDICT:"):])
		low := strings.ToLower(v)
		// The separator is an em dash in the instruction but models emit "-", ":" or nothing.
		cut := func(s string) string {
			for _, sep := range []string{"—", "--", " - ", ":"} {
				if _, after, ok := strings.Cut(s, sep); ok {
					return strings.TrimSpace(after)
				}
			}
			return ""
		}
		switch {
		case strings.HasPrefix(low, "ok"):
			return "ok", cut(v)
		case strings.HasPrefix(low, "attention"):
			return "attention", cut(v)
		}
	}
	return "attention", "the agent did not end with a readable VERDICT line"
}

// runReportJob executes one report run: read the repo, write an evaluation, post it, stop.
func runReportJob(d daemonDevice, ev api.RunEvent) error {
	fail := func(stage string, e error) error {
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", stage+": "+e.Error())
		return fmt.Errorf("%s: %w", stage, e)
	}

	reg := loadDaemonRegistry()
	// The SAME label→path chokepoint every other run goes through: nothing server-supplied ever
	// becomes a path. Only the validated directory is taken; the argv is unused here.
	_, dir, err := resolveRun(reg, runRefFromEvent(ev))
	if err != nil {
		return fail("resolve", err)
	}
	if len(ev.Tasks) == 0 || strings.TrimSpace(ev.Tasks[0]) == "" {
		return fail("report", fmt.Errorf("nothing to investigate"))
	}

	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "running", "")
	logger := newRunLoggerWith(d.Base, d.Token, ev.RunID)

	engineName, note := resolveRunEngine(reg, ev.ProjectLabel, ev.Engine)
	if note != "" {
		if sink := logger.sink(0); sink != nil {
			sink(note)
		}
	}
	spec, ok := eng.Lookup(engineName)
	if !ok {
		logger.close()
		return fail("report", fmt.Errorf("unknown engine %q", engineName))
	}
	model := ""
	if modelRe.MatchString(ev.Model) {
		model = ev.Model
	}

	// ToolsReadOnly is the control. If an engine cannot ENFORCE read-only non-interactively it
	// returns an error here rather than a weaker invocation — and this refuses to run rather than
	// quietly investigating with write access. That is the correct trade: a report that did not run
	// is visible; a report agent that could edit the repo is not.
	argv, stdinPrompt, err := spec.OneShotArgs(reportPrompt(ev.Tasks[0]), model, eng.ToolsReadOnly)
	if err != nil {
		logger.close()
		return fail("report", fmt.Errorf("%s cannot enforce a read-only posture: %w", engineName, err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	out, err := runOneShot(ctx, dir, argv, stdinPrompt, spec.OneShotEnv(eng.ToolsReadOnly)...)
	logger.close()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fail("report", fmt.Errorf("the investigation timed out after %s", reportTimeout))
		}
		return fail("report", fmt.Errorf("the engine turn failed"))
	}

	evaluation := oneShotText(spec, out)
	verdict, reason := parseReportVerdict(evaluation)

	if err := api.PostRunReport(d.Base, d.Token, ev.RunID, verdict, reason, evaluation); err != nil {
		return fail("report", err)
	}
	// The finding IS the deliverable, so the run is done either way — `attention` is not a failure
	// of the run, it is the run succeeding at telling you something.
	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "done", reason)
	return nil
}
