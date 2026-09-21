package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// run_finished.go — a small local record of runs that just ENDED, so something can tell you.
//
// The tray already posts native notifications, and posts them as partyline rather than as Script
// Editor (cmd/ptln-tray/notify_darwin.go). What it watches is peer consults. A run finishing on your
// own laptop is the case email and Slack are both wrong for — you are sitting in front of the
// machine that did the work, and the answer arrives in a mailbox.
//
// `ptln state` is NETWORK-FREE by contract: the tray polls it every few seconds, so it may only read
// local files. Hence a local record rather than a server query — the daemon knows the moment a run
// ends, and writing one line then is cheaper than asking the control plane forever after.
//
// Bounded and self-expiring. This is a notification queue, not history: the run store already holds
// what happened, and a tray that has been running for a week must not accumulate a week of rows.

const (
	finishedKeep = 20            // rows retained, newest first
	finishedTTL  = 2 * time.Hour // older than this is not news
)

type finishedRun struct {
	RunID   string `json:"run_id"`
	Project string `json:"project,omitempty"`
	Status  string `json:"status"` // done | failed | needs_approval | killed
	Tasks   int    `json:"tasks,omitempty"`
	At      string `json:"at"` // RFC3339
}

var finishedMu sync.Mutex

func finishedRunsPath() string { return filepath.Join(daemonDir(), "runs-finished.json") }

func loadFinishedRuns() []finishedRun {
	var out []finishedRun
	if b, err := os.ReadFile(finishedRunsPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// recordFinishedRun appends one ending. Best effort throughout: a run that finished correctly must
// never be reported as failed because we could not write a notification row.
func recordFinishedRun(r finishedRun) {
	if r.RunID == "" {
		return
	}
	if r.At == "" {
		r.At = time.Now().UTC().Format(time.RFC3339)
	}
	finishedMu.Lock()
	defer finishedMu.Unlock()

	rows := append([]finishedRun{r}, prune(loadFinishedRuns())...)
	// Dedupe by run id, newest wins — a re-report (resume, restart) replaces rather than repeats.
	seen := map[string]bool{}
	var out []finishedRun
	for _, x := range rows {
		if seen[x.RunID] {
			continue
		}
		seen[x.RunID] = true
		out = append(out, x)
		if len(out) >= finishedKeep {
			break
		}
	}
	if err := os.MkdirAll(filepath.Dir(finishedRunsPath()), 0o700); err != nil {
		return
	}
	if b, err := json.MarshalIndent(out, "", "  "); err == nil {
		_ = os.WriteFile(finishedRunsPath(), b, 0o600)
	}
}

// recentFinishedRuns is what `ptln state` publishes: newest first, nothing stale.
func recentFinishedRuns() []finishedRun {
	finishedMu.Lock()
	defer finishedMu.Unlock()
	return prune(loadFinishedRuns())
}

// prune drops anything past the TTL and sorts newest first. An unparseable timestamp is KEPT rather
// than discarded — dropping a row because we could not read its clock would silently lose the very
// notification this exists to deliver.
func prune(rows []finishedRun) []finishedRun {
	cutoff := time.Now().Add(-finishedTTL)
	var out []finishedRun
	for _, r := range rows {
		t, err := time.Parse(time.RFC3339, r.At)
		if err != nil || t.After(cutoff) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	if len(out) > finishedKeep {
		out = out[:finishedKeep]
	}
	return out
}

// projectPathForLabel resolves a project label to its local absolute path through THIS machine's
// registry — the same mapping resolveRun uses, and the same reason: a label from the control plane
// is a reference, and only the machine turns it into a path.
func projectPathForLabel(label string) string {
	if label == "" {
		return ""
	}
	if p := projectByLabel(loadDaemonRegistry(), label); p != nil {
		return p.Path
	}
	return ""
}
