// Trust · T2a — executable verify gate. After a task's worker commits its branch, run the
// project's own acceptance checks (build / test / smoke) IN THE WORKTREE before the branch is
// eligible to merge. This is the objective layer of the verify gate; T2b adds an independent
// reviewer agent for the judgment layer.
//
// Checks are the TEAM'S OWN DATA (reference-not-command): they live in the BASE repo at
// `.partyline/verify` (one shell command per line), and we read them from the base repo — NOT the
// agent's worktree — so a task can't weaken its own gate by editing the file it's judged against.
// No checks file → the gate is a no-op, reported honestly as SKIPPED (not a pass).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const verifyFile = ".partyline/verify"

// readChecks returns the acceptance-check commands from the BASE repo (the version the team
// committed, not the agent's edited worktree). One command per non-empty, non-comment (#) line;
// nil when there's no verify file.
func readChecks(baseRepo string) []string {
	b, err := os.ReadFile(filepath.Join(baseRepo, verifyFile))
	if err != nil {
		return nil
	}
	var cmds []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" && !strings.HasPrefix(ln, "#") {
			cmds = append(cmds, ln)
		}
	}
	return cmds
}

// verifyResult is the outcome of the acceptance checks against one task's worktree.
type verifyResult struct {
	ran     bool   // false → no checks defined (gate skipped, not a pass)
	ok      bool   // all checks passed
	reasons string // on failure: which check failed + a bounded output tail (for the human)
}

// runChecks executes each check via `sh -c` in the worktree, in order, stopping at the FIRST
// failure (a later check is meaningless once the build fails). Each check is bounded by timeout.
// The failure's output tail is captured (bounded) so a chatty check can't bloat the ledger/detail.
func runChecks(wtPath string, checks []string, timeout time.Duration) verifyResult {
	if len(checks) == 0 {
		return verifyResult{ran: false}
	}
	for _, cmd := range checks {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		c := exec.CommandContext(ctx, "sh", "-c", cmd)
		c.Dir = wtPath
		out, err := c.CombinedOutput()
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("check timed out (>%s): %s", timeout, cmd)}
		}
		if err != nil {
			return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("check failed: %s\n%s", cmd, tailString(string(out), 1200))}
		}
	}
	return verifyResult{ran: true, ok: true}
}

// tailString trims to the last n bytes (with a leading ellipsis) so a long check log stays bounded.
func tailString(s string, n int) string {
	if s = strings.TrimSpace(s); len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
