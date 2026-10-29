package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A loop run's state lives under ~/.partyline/loops/<id>/, outside the repository, so the session
// being reviewed cannot edit it by writing into its own checkout. events.jsonl is the record; the
// state is computed from it, so a crash leaves nothing to reconcile.

const (
	loopMaxRounds    = 6 // `ready` calls per run, counting check failures and review rounds alike
	loopMaxRevisions = 3 // brief revisions
)

// Run states.
const (
	loopAwaiting  = "awaiting_approval" // brief and checks wait for the person
	loopBatchRev  = "batch_review"      // reviewers read a batch's briefs as a set, before the person
	loopScopeRev  = "scope_review"      // reviewers check a batch step's revised brief against the approved one
	loopRevising  = "revising"          // the person rejected the brief; the session revises it
	loopBaseline  = "baseline"          // checks running before any work, to know what already fails
	loopBuilding  = "building"          // the session, or a batch step's builder, is working
	loopChecking  = "checking"          // a round is running checks and reviews
	loopAwaitYou  = "awaiting_you"      // a disputed P1 waits for the person
	loopPaused    = "paused"            // a review waits for a reviewer that could not run; see loop_pause.go
	loopPRReady   = "pr_ready"          // no new failures, no open P1: the session opens the PR
	loopFailed    = "failed"
	loopStopped   = "stopped"
	loopRoundTask = "round"
)

type loopRun struct {
	Remote        *remoteGateItem `json:"-"`
	Config        *loopConfig     `json:"config,omitempty"`
	ID            string          `json:"id"`
	Repo          string          `json:"repo"` // repository root
	SessionEngine string          `json:"session_engine,omitempty"`
	// The engine process that started the run, by pid and start time (ps lstart): a pane id is
	// reused after a tmux restart, so a wake or delivery by pane checks this process is still there.
	SessionPID     int      `json:"session_pid,omitempty"`
	SessionStarted string   `json:"session_started,omitempty"`
	Session        []string `json:"session"` // identities of the main session (tmux pane, session key)
	Checks         []string `json:"checks"`
	Review         string   `json:"review"` // reviewer spec, "claude,codex"
	Base           string   `json:"base"`   // commit at approval
	Created        time.Time
	State          string `json:"state"`
	Round          int    `json:"round"`
	Revision       int    `json:"revision"`
	Note           string `json:"note,omitempty"` // why it is in this state, for status and the session
	LastTree       string `json:"last_tree,omitempty"`
	Batch          string `json:"batch,omitempty"` // the batch this run is a step of
	Step           int    `json:"step,omitempty"`  // 1-based step in the batch
	// Free counts revisions that were not sent to the person (a batch's approval swapping in step
	// 1's brief, a revision the scope review accepted); they do not use up loopMaxRevisions.
	Free    int `json:"free_revisions,omitempty"`
	InScope int `json:"in_scope_revisions,omitempty"` // revisions the scope review accepted in this step
	// Recheck lets the next round review an unchanged tree: a finding was ruled on or deferred since
	// the last round, so the same code can now pass without an edit.
	Recheck bool   `json:"recheck,omitempty"`
	PR      string `json:"pr,omitempty"` // the PR the session opened, a URL or a number (`ptln loop pr`)
	// A builder-driven batch step's builders (loop_builder.go): the current contractor, how many
	// have run, how many in a row ended without a report, the current one's engine, and whether it
	// was asked only for a missing report.
	Builder       string `json:"builder,omitempty"`
	Builds        int    `json:"builds,omitempty"`
	BuildFails    int    `json:"build_fails,omitempty"`
	BuilderEngine string `json:"builder_engine,omitempty"`
	ReportOnly    bool   `json:"report_only,omitempty"`
	// BuildRevision is the brief revision the current builder was given; BuilderStopped, that it was
	// stopped because the brief is being revised.
	BuildRevision  int  `json:"build_revision,omitempty"`
	BuilderStopped bool `json:"builder_stopped,omitempty"`
	// A paused run (loop_pause.go): the state it resumes to, and when the limit that paused it
	// lifts; zero when only `ptln loop resume` restarts it.
	PausedFrom  string    `json:"paused_from,omitempty"`
	PausedUntil time.Time `json:"paused_until,omitempty"`
	// reviewed: this process's round got past the stale-tree check and reviewed a tree. Not saved.
	reviewed bool
}

type loopEvent struct {
	At    time.Time      `json:"at"`
	Type  string         `json:"type"`
	State string         `json:"state,omitempty"`
	Note  string         `json:"note,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

func loopsDir() string             { return filepath.Join(stateDir(), "loops") }
func loopDir(id string) string     { return filepath.Join(loopsDir(), id) }
func loopRunPath(id string) string { return filepath.Join(loopDir(id), "run.json") }

func newLoopID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func loadLoopRun(id string) (*loopRun, error) {
	if !loopIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid loop id %q", id)
	}
	b, err := os.ReadFile(loopRunPath(id))
	if err != nil {
		return nil, fmt.Errorf("no loop %q", id)
	}
	var r loopRun
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("loop %s: unreadable run.json: %v", id, err)
	}
	return &r, nil
}

// save writes run.json atomically and appends an event recording the transition.
func (r *loopRun) save(ev loopEvent) error {
	if err := os.MkdirAll(loopDir(r.ID), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := loopRunPath(r.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, loopRunPath(r.ID)); err != nil {
		return err
	}
	ev.At, ev.State = time.Now().UTC(), r.State
	f, err := os.OpenFile(filepath.Join(loopDir(r.ID), "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line, _ := json.Marshal(ev)
	_, err = f.Write(append(line, '\n'))
	return err
}

func (r *loopRun) set(state, note, evType string, data map[string]any) error {
	r.State, r.Note = state, note
	if err := r.save(loopEvent{Type: evType, Note: note, Data: data}); err != nil {
		return err
	}
	if r.Batch != "" {
		if b, err := loadBatch(r.Batch); err == nil {
			writeBatchSummary(b)
		}
	}
	markWaiting(r)
	return nil
}

// finished: nothing more will happen in the run. pr_ready is final: its message tells the session to
// open the PR, and a run left open would block the next loop in the repository and keep adding its
// state line to every prompt.
func (r *loopRun) finished() bool {
	return r.State == loopFailed || r.State == loopStopped || r.State == loopPRReady
}

// live: the run, or the batch it is a step of, still has work to do. A batch step at pr_ready with
// steps after it keeps the worktree and the session's prompt line until the next step starts, so
// nothing else can start in between and the session is told to run `batch next`.
func (r *loopRun) live() bool {
	if !r.finished() {
		return true
	}
	// The last step stays live until `batch next` records where the batch ended.
	return r.State == loopPRReady && (batchContinues(r) || batchUnclosed(r))
}

// loopRuns lists every run on the machine, newest first.
func loopRuns() []*loopRun {
	entries, _ := os.ReadDir(loopsDir())
	var out []*loopRun
	for _, e := range entries {
		if r, err := loadLoopRun(e.Name()); err == nil && e.IsDir() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// activeLoopFor is the unfinished run in repo, if any. One per repository.
func activeLoopFor(repo string) *loopRun {
	for _, r := range loopRuns() {
		if r.Repo == repo && r.live() {
			return r
		}
	}
	return nil
}

// loopForSession is the unfinished run whose main session has any of keys.
func loopForSession(keys []string) *loopRun {
	for _, r := range loopRuns() {
		if !r.live() {
			continue
		}
		for _, k := range keys {
			for _, s := range r.Session {
				if k != "" && k == s {
					return r
				}
			}
		}
	}
	return nil
}

// loopEvents reads a run's event log. An unparsable line is an error, never skipped: the log is
// the record, and guessing past a damaged one is how a run resumes into the wrong state.
func loopEvents(id string) ([]loopEvent, error) {
	f, err := os.Open(filepath.Join(loopDir(id), "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []loopEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var ev loopEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("events.jsonl line %d: %v", n, err)
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}
