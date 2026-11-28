package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// State is where a run is. A run is not "a session that went idle": it ends only through an
// explicit result, a timeout, a stop, or as unknown.
type State string

const (
	Queued          State = "queued"
	Running         State = "running"
	WaitingApproval State = "waiting-for-approval"
	Succeeded       State = "succeeded"
	Failed          State = "failed"
	TimedOut        State = "timed-out"
	Stopped         State = "stopped"
	Unknown         State = "unknown" // may have started; outcome uncertain; never retried automatically
	Missed          State = "missed"  // a due slot that did not run (asleep, partyline closed, past catch-up)
)

// Terminal reports whether the run has ended (including unknown, which only you can clear).
func (s State) Terminal() bool {
	switch s {
	case Succeeded, Failed, TimedOut, Stopped, Unknown, Missed:
		return true
	}
	return false
}

// Process is a process found after a stop, still running inside the run's worktree.
type Process struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// Result is what the agent writes to its run-scoped result file. Token ties it to this run; it
// does not prove the output is correct.
type Result struct {
	Token     string   `json:"token"`
	Status    State    `json:"status"` // succeeded | failed
	Summary   string   `json:"summary"`
	Artifacts []string `json:"artifacts,omitempty"`
}

// Validate checks a result against the run's token.
func (r Result) Validate(token string) error {
	switch {
	case token == "" || r.Token != token:
		return errors.New("result token does not match this run")
	case r.Status != Succeeded && r.Status != Failed:
		return fmt.Errorf("result status %q: want succeeded or failed", r.Status)
	case len(r.Summary) > 2000:
		return errors.New("result summary is longer than 2000 characters")
	}
	return nil
}

// Run is one managed execution.
type Run struct {
	ID             string    `json:"id"`
	JobID          string    `json:"job"`
	DefinitionHash string    `json:"definition"`
	Machine        string    `json:"machine"`
	Slot           string    `json:"slot"`                 // what started it, readable: "schedule 2026-11-28T14:00Z", "manual", "loop.accepted:<id>"
	LogPath        string    `json:"log,omitempty"`        // the run's full output, kept after the tab closes
	ResultDir      string    `json:"result_dir,omitempty"` // the only path a read-only run may write
	State          State     `json:"state"`
	Started        time.Time `json:"started"`
	Ended          time.Time `json:"ended,omitzero"`
	Result         *Result   `json:"result,omitempty"`
	Reason         string    `json:"reason,omitempty"` // why it failed, timed out or is unknown
	// StopVerified is false when a stop or timeout could not confirm every process ended. The
	// leftovers it found are listed; the UI offers Kill and Accept. It does not block the feature.
	StopVerified bool      `json:"stop_verified,omitempty"`
	Leftover     []Process `json:"leftover,omitempty"` // known remaining processes; may be incomplete
	// Accepted records your explicit release after an unverified stop. Until then the job stays
	// locked, because a run that may still be going must not overlap a new one.
	Accepted bool `json:"accepted,omitempty"`
}

// HoldsLock reports whether this run still blocks a new run of the same job.
func (r Run) HoldsLock() bool {
	if !r.State.Terminal() {
		return true
	}
	return (r.State == Stopped || r.State == TimedOut || r.State == Unknown) && !r.StopVerified && !r.Accepted
}

// Label is how the UI names the state, including an unverified stop.
func (r Run) Label() string {
	if (r.State == Stopped || r.State == TimedOut) && !r.StopVerified {
		return string(r.State) + " (unverified)"
	}
	return string(r.State)
}

// StartOptions describe one start.
type StartOptions struct {
	Slot string // readable slot; "manual" for Run now
}

// Errors runners return, so the CLI and the tab can explain refusals the same way.
var (
	ErrNotApproved = errors.New("this exact job definition is not approved on this machine")
	ErrBusy        = errors.New("this job is already running")
	ErrUnsupported = errors.New("this engine and capability combination is not supported here")
	ErrNoRun       = errors.New("no such run")
)

// Approvals stores which exact definitions this machine has approved.
type Approvals interface {
	Approved(d Definition) bool
	Approve(d Definition) error
	Revoke(jobID string) error
}

// A runner must never let a result that arrives after a stop, timeout or unknown change that run's
// state or release its lock: only Accept, or verified termination, does that.
//
// Runner executes jobs on this machine. The CLI (`ptln jobs`) and the Jobs tab both call it, so
// validation, approval, locking and stop behave the same everywhere.
type Runner interface {
	// Check reports why this machine cannot run a definition: an unsupported engine or capability,
	// or a missing tool. nil means it can.
	Check(d Definition) error
	// Start begins a run after checking approval and the per-job lock. It returns once the run is
	// queued or running; it does not wait for the result.
	Start(ctx context.Context, d Definition, o StartOptions) (Run, error)
	// Stop ends a run and reports whether termination was verified.
	Stop(ctx context.Context, runID string) (Run, error)
	// Accept is your explicit release of a run that ended unverified or unknown: the job's lock is
	// freed even though termination could not be proven.
	Accept(runID string) (Run, error)
	// Get returns one run's current record.
	Get(runID string) (Run, error)
	// List returns this machine's runs for a job, newest first (all jobs when jobID is "").
	List(jobID string) ([]Run, error)
}
