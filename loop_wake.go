package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Loop wakes use only the run's pane identity, independently of chat membership and chat wakes.
type loopWakeState struct {
	Read  string
	Batch string
	Count int
}

func loopMarkRead(id string) {
	_ = loopWriteWakeFile(id, "inbox-read", []byte(fmt.Sprint(time.Now().UnixNano())))
}

var loopPanesNow = func() map[string]string {
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{pane_id}\t#{pane_tty}").Output()
	panes := map[string]string{}
	if err != nil {
		return panes
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 2 {
			panes[f[0]] = paneEngine(f[1])
		}
	}
	return panes
}

var loopWakePane = wakePane

// A busy or watched pane has not used a wake attempt. wakePane rechecks both
// conditions immediately before typing, to cover changes after this preflight.
var loopWakeEligible = func(pane string) bool {
	if paneWatched(pane) {
		return false
	}
	ok, _ := paneIdleEmpty(pane)
	return ok
}

func loopWakeIdle() {
	var panes map[string]string
	for _, run := range loopRuns() {
		// Only a run a codex session started: an empty engine is a run from before any engine but
		// claude could start one, so a codex pane under it is a reused pane.
		if !run.live() || run.SessionEngine != "codex" || !loopPending(run.ID) {
			continue
		}
		if panes == nil {
			panes = loopPanesNow()
		}
		for _, key := range run.Session {
			if !strings.HasPrefix(key, "pane:") {
				continue
			}
			pane := strings.TrimPrefix(key, "pane:")
			if panes[pane] != "codex" || !loopSessionOnPane(run, pane) || !loopWakeEligible(pane) {
				continue
			}
			// Reserve the attempt under the lock; tmux must never hold the run lock.
			var line string
			err := withRunLock(run.ID, func(r *loopRun) error {
				if !r.live() || r.SessionEngine != "codex" {
					return nil
				}
				read, _ := os.ReadFile(loopFile(r.ID, "inbox-read"))
				var state loopWakeState
				b, _ := os.ReadFile(loopFile(r.ID, "wake.json"))
				_ = json.Unmarshal(b, &state)
				if state.Read != string(read) {
					state = loopWakeState{Read: string(read)}
				}
				entries, _ := os.ReadDir(loopInbox(r.ID))
				var names []string
				for _, e := range entries {
					if strings.HasSuffix(e.Name(), ".txt") {
						names = append(names, e.Name())
					}
				}
				batch := strings.Join(names, "\n")
				if batch == "" || batch == state.Batch || state.Count >= 3 {
					return nil
				}
				line = fmt.Sprintf("[partyline loop %s] You have a loop message. Run `ptln loop inbox %s` and follow it.", r.ID, r.ID)
				state.Batch, state.Count = batch, state.Count+1
				b, _ = json.Marshal(state)
				return loopWriteWakeFile(r.ID, "wake.json", b)
			})
			if err == nil && line != "" {
				_ = loopWakePane(pane, line)
			}
			break
		}
	}
}

// Readers see either complete generation; a watcher restart must not reset a partial counter.
func loopWriteWakeFile(id, name string, data []byte) error {
	f, err := os.CreateTemp(loopDir(id), ".wake-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), loopFile(id, name))
}

// loopSessionOnPane: the engine process that started r still runs, on pane's terminal. A pane id
// tmux reused for another session after a restart fails this.
var loopSessionOnPane = func(r *loopRun, pane string) bool {
	out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_tty}").Output()
	if err != nil {
		return false
	}
	return runSessionOn(r, strings.TrimSpace(string(out)))
}

// runSessionOn: r's recorded engine process is alive, is the same process (its start time), and
// has tty as its terminal.
func runSessionOn(r *loopRun, tty string) bool {
	if r.SessionPID <= 0 || r.SessionStarted == "" || tty == "" {
		return false
	}
	if processStarted(r.SessionPID) != r.SessionStarted {
		return false
	}
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(r.SessionPID)).Output()
	return err == nil && "/dev/"+strings.TrimSpace(string(out)) == tty
}

// processStarted is pid's start time as ps prints it, or "" when there is no such process.
func processStarted(pid int) string {
	if pid <= 0 {
		return ""
	}
	// Pinned rendering: the recording process and the comparing one may have different TZ or locale.
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
