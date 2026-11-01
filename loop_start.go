package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Starting a run and revising its brief. The person's decisions go through the approval popup.

type loopFlags struct {
	config    *loopConfig
	brief     string
	briefs    []string          // every --brief, in order, for a batch
	disputes  map[string]string // --dispute <finding>=<reason>, for a batch review
	checks    []string
	review    string
	reviewSet bool // --review was given
	report    string
	reason    string
}

func parseLoopFlags(args []string) (loopFlags, []string) {
	f := loopFlags{review: "claude,codex"}
	var rest []string
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 >= len(args) {
				fatal(fmt.Errorf("%s needs a value", args[i]))
			}
			i++
			return args[i]
		}
		switch args[i] {
		case "--brief", "--file":
			f.brief = readLoopFile(next())
			f.briefs = append(f.briefs, f.brief)
		case "--dispute":
			id, reason, ok := strings.Cut(next(), "=")
			if !ok || strings.TrimSpace(reason) == "" {
				fatal(fmt.Errorf("--dispute takes <finding>=<reason>"))
			}
			if f.disputes == nil {
				f.disputes = map[string]string{}
			}
			f.disputes[strings.TrimSpace(id)] = strings.TrimSpace(reason)
		case "--checks":
			f.checks = append(f.checks, next())
		case "--config":
			var err error
			f.config, err = readLoopConfig(next())
			if err != nil {
				fatal(err)
			}
		case "--review":
			f.review, f.reviewSet = next(), true
		case "--report":
			f.report = readLoopFile(next())
		case "--reason":
			f.reason = next()
		default:
			rest = append(rest, args[i])
		}
	}
	return f, rest
}

func loopStart(args []string) {
	pid, engine := engineAncestor()
	if err := loopSingleCheck(engine); err != nil {
		fatal(err)
	}
	f, _ := parseLoopFlags(args)
	if f.brief == "" || len(f.checks) == 0 {
		fatal(fmt.Errorf("usage: ptln loop start --brief <file> --checks \"<cmd>\" [--checks ...] [--review claude,codex] [--config <file>]\n\nbrief template:\n\n%s", loopBriefTemplate))
	}
	root, keys := loopPrecheck(f, []string{f.brief})
	what := askForSettings()
	if f.config == nil && !f.reviewSet && what == "" {
		c, err := startConfig(f)
		if err != nil {
			fatal(err)
		}
		f.config = c
	}
	r := &loopRun{ID: newLoopID(), Repo: root, Session: keys, SessionEngine: engine, Checks: f.checks, Review: f.review, Config: f.config,
		Created: time.Now().UTC(), Revision: 1, SessionPID: pid, SessionStarted: processStarted(pid)}
	if err := os.MkdirAll(loopDir(r.ID), 0o700); err != nil {
		fatal(err)
	}
	_ = os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(f.brief), 0o600)
	if what != "" {
		if err := waitForSettings(r, f.config == nil && !f.reviewSet, what); err != nil {
			fatal(err)
		}
		fmt.Println(settingsWaitAdvice(r, what))
		return
	}
	if err := r.set(loopAwaiting, "brief 1 waiting for approval", "started", map[string]any{"checks": f.checks, "review": f.review}); err != nil {
		fatal(err)
	}
	if loopsDecide() {
		fmt.Printf("loop %s started. The loop approves its own brief and starts in a moment; nobody needs to approve it.\n%s\n", r.ID, loopAdvice(r))
	} else {
		fmt.Printf("loop %s started. Tell the user it is waiting for them: ctrl-\\ then a opens the approval popup with the brief and the checks. Do not approve it yourself.\n%s\n", r.ID, loopAdvice(r))
	}
}

// loopPrecheck validates what a run or batch is started with and returns the repository root and the
// session's identity, or exits.
func loopPrecheck(f loopFlags, briefs []string) (string, []string) {
	for _, b := range briefs {
		if err := validateBrief(b); err != nil {
			fatal(err)
		}
		if err := briefIsText(b); err != nil {
			fatal(err)
		}
	}
	// What the person approves is what runs: a check with a newline or escape in it could show one
	// thing in the approval popup and run another.
	for _, c := range f.checks {
		if termSafe(c) != c {
			fatal(fmt.Errorf("check %q contains control characters; give each command as its own --checks", c))
		}
	}
	if _, err := parseLoopEngines(f.review); err != nil {
		fatal(err)
	}
	cwd, _ := os.Getwd()
	root, err := gitMem(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		fatal(fmt.Errorf("not in a git repository"))
	}
	if r := activeLoopFor(root); r != nil {
		fatal(fmt.Errorf("loop %s is already running in this repository (%s, in %s); one per repository, including its worktrees", r.ID, r.State, r.Repo))
	}
	keys := chatSelfKeys("")
	if len(keys) == 0 {
		fatal(fmt.Errorf("start a loop from a claude, codex or gemini session running in partyline: this process has no session identity"))
	}
	if err := requireLoopHooks(); err != nil {
		fatal(err)
	}
	return root, keys
}

// requireLoopHooks checks reachability without assuming config-file hooks appear in argv.
func requireLoopHooks() error {
	pid, engine := engineAncestor()
	if pid == 0 {
		return fmt.Errorf("no supported engine session found; loops need claude, codex or gemini in partyline")
	}
	out, _ := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return loopHooksCheck(engine, string(out), chatAncestorPane())
}

func loopHooksCheck(engine, command, pane string) error {
	switch engine {
	case "claude":
		if !strings.Contains(command, "memory-hook") {
			return fmt.Errorf("this Claude session has no partyline hooks; reopen it with `ptln --resume` or from the session list")
		}
	case "codex", "gemini":
		if pane == "" {
			return fmt.Errorf("this %s session has no partyline pane found from its ancestry", engine)
		}
	default:
		return fmt.Errorf("loops need claude, codex or gemini in partyline")
	}
	return nil
}

func loopSingleCheck(engine string) error {
	if engine == "gemini" {
		return fmt.Errorf("Gemini can start batches, but not single runs: partyline cannot recognise its idle input box; batch results wait for its next prompt")
	}
	return nil
}

// loopReviseBrief stores a new revision of the brief and puts the run back to awaiting approval.
func loopReviseBrief(id, brief string) error {
	// Under the run's lock, like the person's decisions: an approval and a revision cannot
	// interleave, so the person never approves a brief the session replaced while they read it.
	return withRunLock(id, func(r *loopRun) error {
		if r.finished() {
			return fmt.Errorf("loop %s is %s", r.ID, r.State)
		}
		// A round in progress holds its own copy of the run and saves it when it ends, which would
		// overwrite a revision made meanwhile and let work continue without the new brief approved.
		if r.State == loopBaseline || r.State == loopChecking {
			return fmt.Errorf("a round is running (%s); revise the brief after it reports", r.State)
		}
		if r.Revision-r.Free >= loopMaxRevisions {
			_ = r.set(loopFailed, fmt.Sprintf("%d brief revisions without approval", r.Revision), "failed", nil)
			return fmt.Errorf("loop %s: %s", r.ID, r.Note)
		}
		r.Revision++
		if err := os.WriteFile(loopFile(r.ID, fmt.Sprintf("brief-%d.md", r.Revision)), []byte(brief), 0o600); err != nil {
			return err
		}
		return r.set(loopAwaiting, fmt.Sprintf("brief %d waiting for approval", r.Revision), "brief_revised", nil)
	})
}

// briefIsText refuses a brief carrying control characters other than newline and tab: the brief is
// shown to the person in the approval popup, and an escape sequence could redraw what they read.
func briefIsText(brief string) error {
	for n, line := range strings.Split(strings.ReplaceAll(brief, "\r\n", "\n"), "\n") {
		if l := strings.ReplaceAll(line, "\t", " "); termSafe(l) != l {
			return fmt.Errorf("the brief has control characters on line %d; it must be plain text", n+1)
		}
	}
	return nil
}

func loopFile(id, name string) string { return filepath.Join(loopDir(id), name) }

// loopBrief revises the brief: after a rejection, or when the person changed the scope mid-build.
func loopBrief(args []string) {
	f, rest := parseLoopFlags(args)
	r := mustLoop(rest)
	if f.brief == "" {
		fatal(fmt.Errorf("usage: ptln loop brief <id> --file <file>"))
	}
	if err := validateBrief(f.brief); err != nil {
		fatal(err)
	}
	if err := briefIsText(f.brief); err != nil {
		fatal(err)
	}
	if batchPending(r) {
		fatal(fmt.Errorf("loop %s is a batch waiting for its review or approval; revise it with `ptln loop batch revise %s --brief <file> ...`", r.ID, r.ID))
	}
	if r.Batch != "" && r.State == loopBuilding {
		why, err := batchReviseStep(r, f.brief)
		if err != nil {
			fatal(err)
		}
		if why == "" {
			fmt.Printf("the revision went to the scope review. Do not edit files until it reports: %s\n", loopAdvice(r))
			return
		}
		fmt.Printf("%s\nIt waits for the user's approval: ctrl-\\ then a.\n", why)
		return
	}
	if err := loopReviseBrief(r.ID, f.brief); err != nil {
		fatal(err)
	}
	if loopsDecide() {
		fmt.Printf("brief submitted. The loop approves it and carries on in a moment.\n")
	} else {
		fmt.Printf("brief submitted. Tell the user it is waiting for them: ctrl-\\ then a opens the approval popup.\n")
	}
}

func mustLoop(args []string) *loopRun {
	if len(args) == 0 {
		fatal(fmt.Errorf("which loop? `ptln loop status` lists them"))
	}
	r, err := loadLoopRun(args[0])
	if err != nil {
		fatal(err)
	}
	return r
}

// spawnLoopRound starts `ptln loop _round <id> <n>` detached from this process, so the round
// survives the command that started it and the session's turn ending.
func spawnLoopRound(id string, n int) error {
	log, err := os.OpenFile(loopFile(id, fmt.Sprintf("round-%d.log", n)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(selfExe(), "loop", "_round", id, strconv.Itoa(n))
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func loopStatus(args []string) {
	runs := loopRuns()
	if len(args) > 0 {
		r := mustLoop(args)
		runs = []*loopRun{r}
	}
	if len(runs) == 0 {
		fmt.Println("no loops")
		return
	}
	for _, r := range runs {
		fmt.Printf("%s  %-18s round %d/%d  %s  %s\n", r.ID, r.State, r.Round, loopMaxRounds, repoLabel(r.Repo), r.Note)
		if len(args) > 0 {
			for _, c := range loadContractors() {
				if c.Loop == r.ID {
					fmt.Printf("  contractor %s  %-8s %-8s %s  %s  %s\n", c.ID, c.Role, c.State, termSafe(c.Engine), termSafe(c.Title), termSafe(c.Result))
				}
			}
		}
	}
}

// batchReviseStep revises a running batch step's brief. An unattended step may change how it reaches
// its goal: the reviewers check the revision stays inside what the person approved, in a detached
// worker (_scopereview) because a review outlives the session's command, and only a revision that
// does not waits for the person. It returns why the revision went straight to the person, or "" when
// it was sent to the scope review.
func batchReviseStep(r *loopRun, brief string) (string, error) {
	capped := false
	err := withRunLock(r.ID, func(r *loopRun) error {
		if r.State != loopBuilding {
			return fmt.Errorf("loop %s is %s; a step's brief is revised while building", r.ID, r.State)
		}
		// A builder at work would go on building the old brief: it stops, and a fresh one starts on
		// the revision once the step is building again (buildersResume).
		if builderDriven(r) && r.Builder != "" && !r.BuilderStopped {
			stopContractor(r.Builder)
			r.BuilderStopped = true
			if err := r.save(loopEvent{Type: "builder_stopped", Note: "the brief is being revised"}); err != nil {
				return err
			}
		}
		if r.InScope >= maxFreeRevisions {
			capped = true
			return nil
		}
		if err := os.WriteFile(loopFile(r.ID, "scope-pending.md"), []byte(brief), 0o600); err != nil {
			return err
		}
		return r.set(loopScopeRev, "the reviewers are checking the revised brief's scope", "scope_review", nil)
	})
	if err != nil {
		return "", err
	}
	if capped {
		return fmt.Sprintf("- this step has had %d revisions without you; the next one is yours to approve", maxFreeRevisions), loopReviseBrief(r.ID, brief)
	}
	if err := spawnScopeReview(r.ID); err != nil {
		// No reviewer will come: back to building with the old brief, rather than stuck waiting.
		_ = withRunLock(r.ID, func(r *loopRun) error {
			if r.State != loopScopeRev {
				return nil
			}
			return r.set(loopBuilding, "the scope review could not start: "+err.Error(), "scope_review_failed", nil)
		})
		return "", err
	}
	return "", nil
}

var spawnScopeReview = func(id string) error {
	log, err := os.OpenFile(loopFile(id, "scope-review.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(selfExe(), "loop", "_scopereview", id)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// scopeReviewRun is the detached `_scopereview <id>`: it reviews the pending revision against the
// approved brief and applies the outcome under the run's lock, if the run still waits for it.
func scopeReviewRun(id string) error {
	r, err := loadLoopRun(id)
	if err != nil {
		return err
	}
	if r.State != loopScopeRev {
		return fmt.Errorf("loop %s is %s; no scope review is due", r.ID, r.State)
	}
	pending, err := os.ReadFile(loopFile(id, "scope-pending.md"))
	if err != nil || strings.TrimSpace(string(pending)) == "" {
		// Nothing to review: back to building, with the old brief, for the session to resubmit.
		return withRunLock(id, func(r *loopRun) error {
			if r.State != loopScopeRev {
				return nil
			}
			if err := r.set(loopBuilding, "the revised brief was lost; resubmit it", "scope_review_failed", nil); err != nil {
				return err
			}
			return loopPost(r.ID, "The revised brief could not be read, so nothing changed. Resubmit it with `ptln loop brief "+r.ID+" --file <file>`.")
		})
	}
	why := "- the approved brief for this step is missing"
	if approved, err := os.ReadFile(loopFile(id, approvedBriefFile)); err == nil {
		why = batchScopeCheck(r, string(approved), string(pending))
	}
	return withRunLock(id, func(r *loopRun) error {
		if r.State != loopScopeRev {
			return nil // stopped meanwhile
		}
		r.Revision++
		if err := os.WriteFile(loopFile(r.ID, fmt.Sprintf("brief-%d.md", r.Revision)), pending, 0o600); err != nil {
			return err
		}
		if why == "" {
			r.Free++
			r.InScope++
			if err := r.set(loopBuilding, fmt.Sprintf("brief %d, accepted by the scope review", r.Revision), "brief_revised_in_scope", map[string]any{"revision": r.Revision}); err != nil {
				return err
			}
			return loopPost(r.ID, "The reviewers found the revised brief inside the approved scope; it is the brief now. Carry on building.")
		}
		if err := r.set(loopAwaiting, fmt.Sprintf("brief %d waiting for approval: beyond the approved scope", r.Revision), "brief_revised_out_of_scope", map[string]any{"scope": why}); err != nil {
			return err
		}
		return loopPost(r.ID, "The reviewers found the revised brief goes beyond what the user approved:\n"+why+"\nIt waits for the user's approval; the batch waits with it.")
	})
}
