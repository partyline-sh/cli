package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// hooks.go — the customer's own script, at the moments partyline knows something happened.
//
// WHY THIS EXISTS. The board's last column is called Accepted rather than Shipped because partyline
// cannot know a customer's deploy pipeline: every team's is different, and two projects on ONE board
// can differ. That is honest, and on its own it leaves the customer with nothing after Accept — the
// work is signed off and whatever happens next is invisible to us AND unassisted by us.
//
// A hook is how both stay true. Partyline says WHAT happened, in a fixed vocabulary it already owns.
// The customer's script decides what that MEANS for them — promote, smoke, deploy, notify, nothing.
// We never learn what it did, which is exactly the property that lets the same board serve a game
// and a web app.
//
// THE TRUST LINE IS THE SAME ONE AS EVERYWHERE ELSE. A hook is a REPO-TRUSTED script: it lives in
// the repo at `.partyline/hooks/<event>`, is reviewed like any other code in that repo, and the
// control plane can never supply it. That is the identical rule that keeps a web-triggered update
// from becoming fleet RCE — the server may name a thing, it may never ship the thing.
//
// Hooks are OFF until a file exists. Absence is the default and costs nothing.

const hooksDir = ".partyline/hooks"

// hookTimeout bounds one hook. Deploy scripts are legitimately slow, so this is generous — but it is
// finite, because a hook that hangs must not hold a run open forever.
const hookTimeout = 10 * time.Minute

// HookEvent is the vocabulary. It deliberately mirrors the outbound webhook kinds: the same moments,
// named the same way, so a team that already subscribes to `work_item.accepted` does not have to
// learn a second set of words to run a script on it instead.
type HookEvent string

const (
	HookRunQueued          HookEvent = "run.queued"
	HookWorktreeCreated    HookEvent = "worktree.created"
	HookPreVerify          HookEvent = "pre.verify"
	HookPostVerify         HookEvent = "post.verify"
	HookPreMerge           HookEvent = "pre.merge"
	HookPostMerge          HookEvent = "post.merge"
	HookRunAccepted        HookEvent = "run.accepted"
	HookIntegrationDropped HookEvent = "integration.dropped"
)

// hookEvents is every event, for the doc surface and for validating a hook file's NAME — a script
// called `.partyline/hooks/post-merge` (a plausible typo for `post.merge`) would otherwise sit there
// looking installed and never fire.
func hookEvents() []HookEvent {
	return []HookEvent{
		HookRunQueued, HookWorktreeCreated, HookPreVerify, HookPostVerify,
		HookPreMerge, HookPostMerge, HookRunAccepted, HookIntegrationDropped,
	}
}

// hookResult is what the run log records. A hook never decides anything — it reports.
type hookResult struct {
	Event   HookEvent
	Ran     bool
	Failed  bool
	TimedOu bool
	Out     string
}

// hookPath resolves an event to its script in the BASE repo, or "" when none is installed.
//
// Read from the base repo and not the agent's worktree, for the same reason the verify checks are:
// a task must not be able to install or edit the hook that runs on its own work.
func hookPath(baseRepo string, ev HookEvent) string {
	p := filepath.Join(baseRepo, hooksDir, string(ev))
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return ""
	}
	return p
}

// strayHooks lists files in the hooks directory whose name is not an event.
//
// A misnamed hook is the failure mode this whole feature is most likely to produce: someone writes
// `.partyline/hooks/on-merge`, it never runs, and nothing anywhere says why. Silence would read as
// "hooks are broken". So we name them.
func strayHooks(baseRepo string) []string {
	entries, err := os.ReadDir(filepath.Join(baseRepo, hooksDir))
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, e := range hookEvents() {
		known[string(e)] = true
	}
	var stray []string
	for _, e := range entries {
		if !e.IsDir() && !known[e.Name()] {
			stray = append(stray, e.Name())
		}
	}
	sort.Strings(stray)
	return stray
}

// runHook executes the script for an event, if one is installed, with the event's facts in the
// environment. Returns a zero result when no hook exists — the common case, and not a failure.
//
// A FAILING HOOK DOES NOT FAIL THE RUN. The hook is the customer's opinion about what happens next;
// the run already did what partyline promised. Swallowing the failure would be worse — a deploy
// script that exited 1 must be visible — so it is reported loudly and the run continues.
func runHook(baseRepo string, ev HookEvent, env map[string]string) hookResult {
	path := hookPath(baseRepo, ev)
	if path == "" {
		return hookResult{Event: ev}
	}
	// The script is invoked through the shell so a hook can be a one-liner without a shebang or an
	// executable bit — the same shape as `.partyline/verify`, which people already know.
	kv := make([]string, 0, len(env))
	for k, v := range env {
		kv = append(kv, "PARTYLINE_"+k+"="+v)
	}
	sort.Strings(kv) // deterministic, so a log diff is readable
	out, timedOut, err := runHookCmd(baseRepo, path, kv, hookTimeout)
	return hookResult{Event: ev, Ran: true, Failed: err != nil || timedOut, TimedOu: timedOut, Out: out}
}

// note renders a hook's outcome for the run log. Empty when nothing was installed.
func (r hookResult) note() string {
	switch {
	case !r.Ran:
		return ""
	case r.TimedOu:
		return fmt.Sprintf("⚠ hook %s timed out after %s — the run is unaffected", r.Event, hookTimeout)
	case r.Failed:
		return fmt.Sprintf("⚠ hook %s exited non-zero — the run is unaffected:\n%s", r.Event, strings.TrimSpace(tailString(r.Out, 400)))
	default:
		return fmt.Sprintf("✓ hook %s ran", r.Event)
	}
}

// runHookCmd is runCheck's sibling, split out because a hook needs an ENVIRONMENT and a check does
// not. Same shell invocation, same timeout discipline, same combined output.
func runHookCmd(dir, path string, env []string, timeout time.Duration) (out string, timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", path)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	b, err := c.CombinedOutput()
	return string(b), ctx.Err() == context.DeadlineExceeded, err
}
