package jobs

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemApprovals is an in-memory Approvals for tests and the fake runner.
type MemApprovals struct {
	mu   sync.Mutex
	byID map[string]string // job ID → approved definition hash
}

func (a *MemApprovals) Approved(d Definition) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byID[d.ID()] == d.Hash()
}

func (a *MemApprovals) Approve(d Definition) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byID == nil {
		a.byID = map[string]string{}
	}
	a.byID[d.ID()] = d.Hash()
	return nil
}

func (a *MemApprovals) Revoke(jobID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byID, jobID)
	return nil
}

// FakeRunner is an in-memory Runner for UI and CLI tests. It enforces approval and the per-job
// lock exactly as a real runner must; Finish and the Leftover field drive the rest.
type FakeRunner struct {
	Approvals Approvals
	Machine   string
	Now       func() time.Time
	// Leftover, when set, is what a Stop "finds" still running; the stop is then unverified.
	Leftover []Process

	mu   sync.Mutex
	runs map[string]*Run
	seq  int
}

func (f *FakeRunner) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *FakeRunner) Check(d Definition) error {
	if d.Job.Engine != "claude" && d.Job.Engine != "codex" {
		return ErrUnsupported
	}
	return nil
}

func (f *FakeRunner) Start(_ context.Context, d Definition, o StartOptions) (Run, error) {
	if err := f.Check(d); err != nil {
		return Run{}, err
	}
	if f.Approvals == nil || !f.Approvals.Approved(d) {
		return Run{}, ErrNotApproved
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.runs {
		if r.JobID == d.ID() && r.HoldsLock() {
			return Run{}, fmt.Errorf("%w (run %s)", ErrBusy, r.ID)
		}
	}
	if f.runs == nil {
		f.runs = map[string]*Run{}
	}
	f.seq++
	slot := o.Slot
	if slot == "" {
		slot = "manual"
	}
	r := &Run{ID: fmt.Sprintf("run-%d", f.seq), JobID: d.ID(), DefinitionHash: d.Hash(), Machine: f.Machine, Slot: slot, State: Running, Started: f.now()}
	f.runs[r.ID] = r
	return *r, nil
}

// Finish ends a running run with the agent's result, validated against token.
func (f *FakeRunner) Finish(runID, token string, res Result) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return Run{}, ErrNoRun
	}
	if r.State.Terminal() {
		// A late result after a stop, timeout or unknown is recorded but changes nothing: the run's
		// uncertainty, and the job's lock, stay until you Accept or termination is verified.
		if res.Validate(token) == nil {
			r.Result = &res
		}
		return *r, nil
	}
	if err := res.Validate(token); err != nil {
		r.State, r.Reason = Failed, "no valid result: "+err.Error()
	} else {
		r.State, r.Result = res.Status, &res
	}
	r.Ended = f.now()
	return *r, nil
}

func (f *FakeRunner) Stop(_ context.Context, runID string) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return Run{}, ErrNoRun
	}
	if r.State.Terminal() {
		return *r, nil
	}
	r.State, r.Ended = Stopped, f.now()
	r.Leftover = append([]Process(nil), f.Leftover...)
	r.StopVerified = len(r.Leftover) == 0
	return *r, nil
}

func (f *FakeRunner) Get(runID string) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.runs[runID]; ok {
		return *r, nil
	}
	return Run{}, ErrNoRun
}

func (f *FakeRunner) List(jobID string) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Run
	for _, r := range f.runs {
		if jobID == "" || r.JobID == jobID {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Started.After(out[b].Started) || (out[a].Started.Equal(out[b].Started) && out[a].ID > out[b].ID)
	})
	return out, nil
}

func (f *FakeRunner) Accept(runID string) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return Run{}, ErrNoRun
	}
	if !r.State.Terminal() {
		return *r, fmt.Errorf("run %s is still %s; stop it first", runID, r.State)
	}
	r.Accepted = true
	return *r, nil
}
