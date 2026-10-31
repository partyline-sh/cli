package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// The CI state of each Done card's PR. The watcher asks gh in the background and caches the answer
// in ~/.partyline; the Loops board only reads the cache, so it never waits on the network.

// prChecksEvery is how often the watcher asks gh about one PR.
const prChecksEvery = 2 * time.Minute

// prChecksTimeout bounds one `gh pr checks`; a hung network call must not hold the refresh.
const prChecksTimeout = 30 * time.Second

// prCheck is one PR's cached CI state: "pass", "fail", "pending", or "" when gh could not say.
type prCheck struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

func prChecksPath() string { return filepath.Join(loopsDir(), "pr-checks.json") }

// prChecksKey identifies a PR across runs: a bare number only means something in its repository.
func prChecksKey(r *loopRun) string { return r.Repo + "\x00" + r.PR }

func loadPRChecks() map[string]prCheck {
	out := map[string]prCheck{}
	if b, err := os.ReadFile(prChecksPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func savePRChecks(c map[string]prCheck) {
	_ = os.MkdirAll(loopsDir(), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := prChecksPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, prChecksPath())
	}
}

// cachedPRCheck is what the board shows for a run's PR; it never runs gh.
func cachedPRCheck(r *loopRun) string {
	if r.PR == "" {
		return ""
	}
	return loadPRChecks()[prChecksKey(r)].State
}

var prChecksBusy atomic.Bool

// refreshPRChecksInBackground is the watcher's tick step: it starts a refresh unless one is
// still running, so the tick and its heartbeat never wait on gh.
func refreshPRChecksInBackground() {
	if !prChecksBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer prChecksBusy.Store(false)
		refreshPRChecks(time.Now())
	}()
}

// refreshPRChecks asks gh about each Done card's PR whose cached state is older than prChecksEvery,
// and forgets PRs that are no longer on the board.
func refreshPRChecks(now time.Time) {
	cache := loadPRChecks()
	keep := map[string]prCheck{}
	changed := false
	for _, r := range loopRuns() {
		if r.PR == "" || r.State != loopPRReady || now.Sub(tabLastChange(r)) > tabDoneFor {
			continue
		}
		k := prChecksKey(r)
		c, ok := cache[k]
		if !ok || now.Sub(c.At) >= prChecksEvery {
			c, changed = prCheck{State: ghPRChecks(r.Repo, r.PR), At: now}, true
		}
		keep[k] = c
	}
	if changed || len(keep) != len(cache) {
		savePRChecks(keep)
	}
}

// ghPRChecks runs `gh pr checks` in the run's repository and sums its checks up: any failed or
// cancelled is "fail", else any pending is "pending", else "pass". gh missing, signed out, or no
// checks reported is "".
func ghPRChecks(dir, pr string) string {
	ctx, cancel := context.WithTimeout(context.Background(), prChecksTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks", pr, "--json", "bucket")
	cmd.Dir = dir
	out, _ := cmd.Output() // gh exits non-zero for failing and pending checks too; the JSON says which
	var checks []struct {
		Bucket string `json:"bucket"`
	}
	if json.Unmarshal(out, &checks) != nil || len(checks) == 0 {
		return ""
	}
	state := "pass"
	for _, c := range checks {
		switch c.Bucket {
		case "fail", "cancel":
			return "fail"
		case "pending":
			state = "pending"
		}
	}
	return state
}
