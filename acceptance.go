package main

import (
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// acceptance.go — prove the deliverable was ABSENT before the work, so proving it arrived afterwards
// means something.
//
// THE FAILURE THIS CLOSES. A task's executable check used to be counted, never run before the work.
// "Baseline the numbered migrations" carried one, passed it, and still reached a reviewer with its
// core deliverable missing — because the check passed at BOTH ends. Nothing mechanical could tell
// done from not-done, so a human had to read the diff, two auto-repair rounds later.
//
// A check that already passes is not acceptance. It is a guard: it proves nothing broke, which is
// worth having and is a different claim.
//
//	acceptance — must FAIL now and PASS after. It proves something was built.
//	guard      — must PASS now and after. It proves nothing else broke.
//
// Running this costs one command before the work. It saves a model run, its repair rounds, and a
// reviewer call on exactly the tasks that were never going to be verifiable — and the tax is paid in
// the worktree, where checks have to run anyway.

type preflight struct {
	// blocked is set when the task cannot prove itself. The message is what a human reads INSTEAD of
	// a result, so it says which check, what happened, and what to do about it.
	blocked string
	warns   []string
	ran     int
}

// preflightAcceptance runs the task's checks against the worktree BEFORE the worker touches it.
//
// It is deliberately not fatal on uncertainty. A check that cannot run tells us nothing, and blocking
// a real task because our own check timed out is a worse outcome than missing one bad task — so the
// asymmetry is: fail-closed on "it passed", fail-open on "could not tell".
func preflightAcceptance(wtPath string, checks []api.RunAcceptanceCheck, timeout time.Duration) preflight {
	var p preflight
	var alreadyGreen []string
	for _, c := range checks {
		cmd := strings.TrimSpace(c.Command)
		if cmd == "" {
			continue
		}
		out, timedOut, err := runCheck(wtPath, cmd, timeout)
		if timedOut {
			p.warns = append(p.warns, fmt.Sprintf("acceptance check timed out before the work (>%s), so it proves nothing either way: %s", timeout, cmd))
			continue
		}
		p.ran++
		switch {
		case c.Direction == "guard" && err != nil:
			// A guard that is already red is pre-existing breakage. Not this task's doing, and not a
			// reason to refuse it — but the worker must know, or it will read its own green-to-red as
			// a regression it caused.
			p.warns = append(p.warns, fmt.Sprintf("guard %q is ALREADY failing on this base — pre-existing, not this task's regression:\n%s", cmd, tailString(out, 400)))
		case c.Direction != "guard" && err == nil:
			// The case this file exists for.
			alreadyGreen = append(alreadyGreen, fmt.Sprintf("  %s\n    (criterion: %s)", cmd, strings.TrimSpace(c.Text)))
		}
	}
	if len(alreadyGreen) > 0 {
		p.blocked = "This task cannot prove itself. Its acceptance check ALREADY PASSES on the base branch, " +
			"before any work has been done:\n\n" + strings.Join(alreadyGreen, "\n") + "\n\n" +
			"So it cannot tell whether the work happened — it would report success either way. Either the " +
			"work is already done (check the branch and close this), or the check is a GUARD rather than " +
			"acceptance and the task needs one that fails today and passes when the deliverable lands."
	}
	return p
}

// greenAfterAcceptance is the other half of the pair.
//
// preflightAcceptance proves the check was RED before the work. This proves it is GREEN after. One
// without the other proves nothing: red-only says the work had not already been done, green-only
// says a check passes and never asks whether it passed all along. Together they say something was
// built.
//
// It is deliberately narrow. Only acceptance-direction criteria are judged — a GUARD that goes red
// is a regression and belongs to the reviewer and the repo's own checks, which already run and
// already say so with better context. Re-failing it here would report the same problem twice under
// a name that does not describe it.
//
// FAIL-CLOSED on a check that cannot be run at all, and OPEN on one that times out. A command that
// errors out is a claim we cannot verify; a command that ran out of clock proves nothing either way
// and must not manufacture a failure the human then has to disprove.
func greenAfterAcceptance(wtPath string, checks []api.RunAcceptanceCheck, timeout time.Duration) (ran int, reasons []string, warns []string) {
	for _, c := range checks {
		cmd := strings.TrimSpace(c.Command)
		if cmd == "" || c.Direction == "guard" {
			continue
		}
		out, timedOut, err := runCheck(wtPath, cmd, timeout)
		if timedOut {
			warns = append(warns, fmt.Sprintf("acceptance check timed out after the work (>%s), so it proves nothing either way: %s", timeout, cmd))
			continue
		}
		ran++
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("acceptance check STILL FAILS after the work — the task's own definition of done is unmet:\n  %s\n  (criterion: %s)\n%s",
				cmd, strings.TrimSpace(c.Text), tailString(out, 600)))
		}
	}
	return ran, reasons, warns
}
