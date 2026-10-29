package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The person's decisions on a run, shared by the approval popup (loop_gate_tui.go) and the hidden
// terminal commands (loop_gates.go). Each one checks the run is still waiting for it, so two
// surfaces open at once cannot decide the same thing twice.

// withRunLock runs fn on the run as it is on disk now, holding the run's lock. A popup may have
// been open for minutes: the run it loaded can since have been decided elsewhere, stopped, or had
// its brief revised. Deciding on that stale copy would approve something the person did not read.
func withRunLock(id string, fn func(r *loopRun) error) error {
	// The id comes from the session's arguments: it must name an existing run, never a path.
	if !loopIDPattern.MatchString(id) {
		return fmt.Errorf("no loop %q", id)
	}
	if _, err := os.Stat(loopRunPath(id)); err != nil {
		return fmt.Errorf("no loop %q", id)
	}
	f, err := os.OpenFile(loopFile(id, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	r, err := loadLoopRun(id)
	if err != nil {
		return err
	}
	return fn(r)
}

// gateProvenance records how a decision was made, in its event: the terminal it came from and the
// processes above it. The gate cannot stop every forgery by a process running as the person; this
// makes one findable afterwards.
// decisionProvenance is what decisions record; a variable so the test binary does not ask the
// person's live tmux server for its clients.
var decisionProvenance = gateProvenance

func gateProvenance() map[string]any {
	pid := os.Getpid()
	tty, _ := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	sid, _ := unix.Getsid(0)
	prov := map[string]any{"tty": strings.TrimSpace(string(tty)), "sid": sid, "processes": processChain(pid), "confirmed_by": gateConfirmMethod}
	gateConfirmMethod = "none" // describes one decision only
	// The attached clients, and what each runs under: a client an agent attached for itself shows
	// here.
	if out, err := tmuxCmd("list-clients", "-F", "#{client_pid} #{client_tty}").Output(); err == nil {
		var clients []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				cpid, _ := strconv.Atoi(f[0])
				clients = append(clients, map[string]any{"tty": f[1], "processes": processChain(cpid)})
			}
		}
		prov["clients"] = clients
	}
	return prov
}

// processChain is pid and its ancestors, each as "pid command".
func processChain(pid int) []string {
	var chain []string
	for _, p := range append([]int{pid}, processAncestors(pid)...) {
		out, err := exec.Command("ps", "-o", "pid=,command=", "-p", strconv.Itoa(p)).Output()
		if err == nil {
			chain = append(chain, strings.TrimSpace(string(out)))
		}
	}
	return chain
}

// loopIDPattern is the form newLoopID makes.
var loopIDPattern = regexp.MustCompile(`^[0-9a-f]{6}$`)

// stillAsRead refuses when the run has moved on from what the person was shown.
func stillAsRead(r *loopRun, state string, revision int) error {
	if r.State != state {
		return fmt.Errorf("loop %s is now %s; nothing was decided", r.ID, r.State)
	}
	if revision != 0 && r.Revision != revision {
		return fmt.Errorf("the brief changed since you read it (now revision %d); nothing was decided", r.Revision)
	}
	return nil
}

// loopApproveRun approves brief revision `revision` and the checks: records the base commit and
// starts the baseline.
func loopApproveRun(id string, revision int) error {
	return withRunLock(id, func(r *loopRun) error {
		if err := stillAsRead(r, loopAwaiting, revision); err != nil {
			return err
		}
		if err := r.checkConfig(); err != nil {
			return err
		}
		head, err := gitMem(r.Repo, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		r.Base = head
		data := map[string]any{"base": head, "revision": revision, "by": decisionProvenance()}
		if batchPending(r) {
			b, err := batchApproved(r)
			if err != nil {
				return err
			}
			data["batch"] = b
		} else if r.Batch != "" {
			// The person approved a revision of a batch step: it is the step's approved brief now.
			if err := os.WriteFile(loopFile(r.ID, approvedBriefFile), []byte(latestBrief(r)), 0o600); err != nil {
				return err
			}
			r.InScope = 0
		}
		// A revision approved mid-run keeps the run's base and baseline: re-running round 0 on the
		// half-built tree would count what the work already broke as failing before it.
		if _, err := os.Stat(loopFile(r.ID, "baseline.json")); err == nil {
			r.Base = data["base"].(string)
			if prevBase := baseAtApproval(r); prevBase != "" {
				r.Base = prevBase
			}
			if err := r.set(loopBuilding, fmt.Sprintf("brief %d approved; building", r.Revision), "approved", data); err != nil {
				return err
			}
			return loopPost(r.ID, fmt.Sprintf("The user approved brief %d. Carry on building to it, then run `ptln loop ready %s --report <file>`.", r.Revision, r.ID))
		}
		if err := r.set(loopBaseline, "running the checks before any work", "approved", data); err != nil {
			return err
		}
		return spawnRound(r.ID, 0)
	})
}

// loopRejectRun rejects brief revision `revision` with the person's note and tells the session.
func loopRejectRun(id string, revision int, note string) error {
	return withRunLock(id, func(r *loopRun) error {
		if err := stillAsRead(r, loopAwaiting, revision); err != nil {
			return err
		}
		note = strings.TrimSpace(note)
		// Revising, not awaiting: the rejected brief must not stay approvable, and the run is no
		// longer waiting for the person, so its indicators clear.
		if err := r.set(loopRevising, "brief rejected: "+note, "rejected", map[string]any{"note": note, "revision": revision, "by": decisionProvenance()}); err != nil {
			return err
		}
		how := fmt.Sprintf("`ptln loop brief %s --file <file>`", r.ID)
		if batchPending(r) {
			how = fmt.Sprintf("`ptln loop batch revise %s --brief <file> ...` (every step, in order)", r.ID)
		}
		return loopPost(r.ID, fmt.Sprintf("The user rejected brief %d. Their note: %q\nRevise it with %s.", r.Revision, note, how))
	})
}

// loopDecideDispute records the person's ruling on finding `finding`, the one they were shown:
// "fix" or "dismiss".
func loopDecideDispute(id, finding, decision string) error {
	if decision != "fix" && decision != "dismiss" {
		return fmt.Errorf("decision %q is not fix or dismiss", decision)
	}
	return withRunLock(id, func(r *loopRun) error {
		if err := stillAsRead(r, loopAwaitYou, 0); err != nil {
			return err
		}
		var t loopTracked
		readLoopJSON(r.ID, "dispute.json", &t)
		if t.ID == "" || t.ID != finding {
			return fmt.Errorf("the disputed finding is now %q, not %q; nothing was decided", t.ID, finding)
		}
		t.Decision = decision
		var decided []loopTracked
		readLoopJSON(r.ID, "decided.json", &decided)
		writeLoopJSON(r.ID, "decided.json", append(decided, t))
		if r.Batch != "" && t.Decision != "" && strings.HasPrefix(t.ID, "S") {
			// A batch's deferred findings, put to the person one at a time after its last step.
			return batchDeferredDecided(r, t)
		}
		r.Recheck = true
		if builderDriven(r) {
			task := fmt.Sprintf("The user decided disputed finding %s: %s.", t.ID, decision)
			if decision == "fix" {
				task += " Fix it:\n\n" + formatTracked([]loopTracked{t})
			} else {
				task += " It will not be raised again. Finish the work and report."
			}
			msg := builderHandOff(r, t.ID+" decided", task)
			if err := r.set(loopBuilding, t.ID+" decided: "+decision, "decided", map[string]any{"finding": t.ID, "decision": decision, "by": decisionProvenance()}); err != nil {
				return err
			}
			if msg != "" {
				return loopPost(r.ID, msg)
			}
			return nil
		}
		if err := r.set(loopBuilding, t.ID+" decided: "+decision, "decided", map[string]any{"finding": t.ID, "decision": decision, "by": decisionProvenance()}); err != nil {
			return err
		}
		msg := fmt.Sprintf("The user decided %s: %s.", t.ID, decision)
		if decision == "fix" {
			msg += " Fix it."
		} else {
			msg += " It will not be raised again."
		}
		return loopPost(r.ID, msg+fmt.Sprintf(" Then run `ptln loop ready %s --report <file>`.", r.ID))
	})
}

// loopWaiting reports whether r is waiting for the person.
func loopWaiting(r *loopRun) bool { return r.State == loopAwaiting || r.State == loopAwaitYou }

// loopWaitingRuns lists every run waiting for the person, oldest first.
func loopWaitingRuns() []*loopRun {
	var out []*loopRun
	runs := loopRuns()
	for i := len(runs) - 1; i >= 0; i-- {
		if loopWaiting(runs[i]) {
			out = append(out, runs[i])
		}
	}
	return out
}

// spawnRound starts a detached round; a variable so tests can record it instead of running the
// test binary as `ptln loop _round`.
var spawnRound = spawnLoopRound

// markWaiting is what a state change calls to update the tmux indicators. A variable so the test
// binary can replace it: tmuxCmd talks to the person's live server.
var markWaiting = loopMarkWaiting

// loopRefreshIndicators rebuilds the indicators from the runs on disk, for a new or resumed tmux
// server whose options start empty.
func loopRefreshIndicators() {
	runs := loopWaitingRuns()
	if len(runs) == 0 {
		_ = tmuxCmd("set-option", "-g", "-u", "@ptln_waiting").Run()
		return
	}
	// One reconcile covers every run and window (loopMarkWaiting ignores its argument); calling it
	// per waiting run repeated the whole scan and every tmux call once per run, every tick.
	markWaiting(runs[0])
}

// loopMarkWaiting keeps the tmux indicators in step with the runs: @ptln_needs on every window
// holding a waiting run's session (the tab shows the needs-you glyph) and the global
// @ptln_waiting count (the status bar shows "N to approve"). Every window is reconciled, not only
// r's: a pane moved by a merge or a break-out leaves its old window behind. Runs hold separate
// locks, so the reconciliation takes one shared lock and reads the runs after it, or two
// transitions could publish their snapshots out of order. Failure is ignored: the indicator is a
// courtesy, the run's state is the record.
func loopMarkWaiting(*loopRun) {
	if f, err := os.OpenFile(filepath.Join(loopsDir(), "indicators.lock"), os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		defer f.Close()
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) == nil {
			defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
	}
	waiting := loopWaitingRuns()
	waitingWindows := map[string]bool{}
	for _, w := range waiting {
		for _, k := range w.Session {
			if pane, ok := strings.CutPrefix(k, "pane:"); ok {
				if out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_id}").Output(); err == nil {
					waitingWindows[strings.TrimSpace(string(out))] = true
				}
			}
		}
	}
	// The person's session only: the contractor session's windows carry no loop indicators.
	if out, err := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}").Output(); err == nil {
		for _, win := range strings.Fields(string(out)) {
			if waitingWindows[win] {
				_ = tmuxCmd("set-option", "-w", "-t", win, "@ptln_needs", "1").Run()
			} else {
				_ = tmuxCmd("set-option", "-w", "-u", "-t", win, "@ptln_needs").Run()
			}
		}
	}
	if n := len(waiting); n > 0 {
		_ = tmuxCmd("set-option", "-g", "@ptln_waiting", strconv.Itoa(n)).Run()
	} else {
		_ = tmuxCmd("set-option", "-g", "-u", "@ptln_waiting").Run()
	}
	_ = tmuxCmd("refresh-client", "-S").Run()
}

// baseAtApproval is the base commit recorded by the run's first approval.
func baseAtApproval(r *loopRun) string {
	evs, _ := loopEvents(r.ID)
	for _, e := range evs {
		if e.Type == "approved" {
			if b, ok := e.Data["base"].(string); ok {
				return b
			}
		}
	}
	return ""
}
