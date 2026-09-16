package main

import (
	"fmt"
	"sync"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// fakeClaimer hands out n tasks (idx 0..n-1) exactly once across any number of concurrent callers,
// then nil (drained) — a stand-in for the server-side atomic claim, so runClaimPass can be driven
// without a network. Thread-safe: the mutex is what proves the LOOP never asks for a task twice.
func fakeClaimer(n int) claimFn {
	var mu sync.Mutex
	next := 0
	return func() (*api.ClaimedTask, error) {
		mu.Lock()
		defer mu.Unlock()
		if next >= n {
			return nil, nil
		}
		i := next
		next++
		return &api.ClaimedTask{Idx: i, Task: fmt.Sprintf("task-%d", i)}, nil
	}
}

// recordingExec + recordingReporter capture, under a mutex, what ran and what was reported.
type recorder struct {
	mu      sync.Mutex
	ran     map[int]int
	reports map[int]string
}

func newRecorder() *recorder { return &recorder{ran: map[int]int{}, reports: map[int]string{}} }

func (r *recorder) exec(ok bool, tokens int) taskExec {
	return func(i int, task, prompt string) crankResult {
		r.mu.Lock()
		r.ran[i]++
		r.mu.Unlock()
		return crankResult{task: task, ok: ok, tokens: tokens}
	}
}

func (r *recorder) reporter() runReporter {
	return runReporter{post: func(tr api.RunTaskUpdate) {
		r.mu.Lock()
		r.reports[tr.Idx] = tr.Status
		r.mu.Unlock()
	}}
}

// The core correctness guarantee of claim mode: with N concurrent workers, every task runs EXACTLY
// once (no two workers take the same one) and each is reported done.
func TestClaimPassNoDoubleClaim(t *testing.T) {
	const n = 50
	rec := newRecorder()
	o := crankOpts{workers: 8}

	ceiling, results := runClaimPass(fakeClaimer(n), o, rec.exec(true, 0), rec.reporter(), new(int64))

	if ceiling {
		t.Errorf("ceiling hit with no token budget set")
	}
	if len(results) != n {
		t.Fatalf("ran %d tasks, want %d", len(results), n)
	}
	for i := 0; i < n; i++ {
		if rec.ran[i] != 1 {
			t.Errorf("task %d ran %d times, want exactly 1 (double-claim or dropped)", i, rec.ran[i])
		}
		if rec.reports[i] != "done" {
			t.Errorf("task %d reported %q, want done", i, rec.reports[i])
		}
	}
}

// halt-on-fail stops the pass after K consecutive failures (deterministic with one worker).
func TestClaimPassHaltOnFail(t *testing.T) {
	rec := newRecorder()
	o := crankOpts{workers: 1, haltOnFail: 2}

	_, results := runClaimPass(fakeClaimer(10), o, rec.exec(false, 0), rec.reporter(), new(int64))

	if len(results) != 2 {
		t.Errorf("ran %d tasks, want 2 (halt after 2 consecutive fails)", len(results))
	}
}

// The soft token ceiling stops taking new work once the shared total reaches the budget.
func TestClaimPassTokenCeiling(t *testing.T) {
	rec := newRecorder()
	o := crankOpts{workers: 1, maxTokens: 10}
	var used int64

	ceiling, results := runClaimPass(fakeClaimer(10), o, rec.exec(true, 6), rec.reporter(), &used)

	if !ceiling {
		t.Errorf("ceiling not reported hit (used %d, budget %d)", used, o.maxTokens)
	}
	// task0 → used 6 (<10, continue); task1 → used 12 (>=10, stop before task2).
	if len(results) != 2 {
		t.Errorf("ran %d tasks, want 2 before the ceiling tripped", len(results))
	}
}
