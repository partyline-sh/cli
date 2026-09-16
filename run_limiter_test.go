package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// peakConcurrency runs n jobs through a lane at the given limit and reports the most that were ever
// in flight at once. This is the measurement JOBS PER MACHINE is a promise about.
func peakConcurrency(t *testing.T, limit, n int) int {
	t.Helper()
	q := make(chan queuedJob, n)
	go drainRunQueueLimited(q, newRunLimiter(limit))

	var mu sync.Mutex
	inFlight, peak := 0, 0
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		q <- queuedJob{run: func() {
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond) // hold the slot so overlap is observable
			mu.Lock()
			inFlight--
			mu.Unlock()
			wg.Done()
		}}
	}
	wg.Wait()
	close(q)
	mu.Lock()
	defer mu.Unlock()
	return peak
}

// The whole point: 2 must actually mean 2. Before the limiter every value behaved as 1, which is
// why the settings dialog was writing a promise the daemon didn't keep.
func TestBuildLaneRunsUpToTheLimitConcurrently(t *testing.T) {
	if got := peakConcurrency(t, 1, 4); got != 1 {
		t.Errorf("limit 1: peak %d, want 1 (serial — the old behaviour must be preserved)", got)
	}
	if got := peakConcurrency(t, 2, 6); got != 2 {
		t.Errorf("limit 2: peak %d, want 2", got)
	}
	if got := peakConcurrency(t, 4, 8); got != 4 {
		t.Errorf("limit 4: peak %d, want 4", got)
	}
}

// Nothing may exceed the cap — that's the safety half. A team setting 2 must never see 3 running.
func TestBuildLaneNeverExceedsTheLimit(t *testing.T) {
	if got := peakConcurrency(t, 3, 30); got > 3 {
		t.Fatalf("peak %d exceeded the limit of 3", got)
	}
}

// The limit is LIVE: changing the dialog takes effect without a daemon restart.
func TestRaisingTheLimitReleasesWaitersWithoutARestart(t *testing.T) {
	l := newRunLimiter(1)
	q := make(chan queuedJob, 4)
	go drainRunQueueLimited(q, l)

	release := make(chan struct{})
	var started atomic.Int32
	for i := 0; i < 3; i++ {
		q <- queuedJob{run: func() {
			started.Add(1)
			<-release // hold every slot open
		}}
	}
	// At limit 1 exactly one job may be running.
	time.Sleep(50 * time.Millisecond)
	if got := started.Load(); got != 1 {
		t.Fatalf("at limit 1: %d started, want 1", got)
	}
	l.setLimit(3) // the team raises JOBS PER MACHINE
	time.Sleep(50 * time.Millisecond)
	if got := started.Load(); got != 3 {
		t.Fatalf("after raising to 3: %d started, want 3 — waiters were not woken", got)
	}
	close(release)
	close(q)
}

// Lowering must not kill work in flight — those finish and the new limit applies as slots free.
// Losing a live build to satisfy a settings change would be a worse bug than the one being fixed.
func TestLoweringTheLimitLetsRunningWorkFinish(t *testing.T) {
	l := newRunLimiter(3)
	q := make(chan queuedJob, 3)
	go drainRunQueueLimited(q, l)

	release := make(chan struct{})
	var done atomic.Int32
	for i := 0; i < 3; i++ {
		q <- queuedJob{run: func() { <-release; done.Add(1) }}
	}
	time.Sleep(50 * time.Millisecond)
	l.setLimit(1) // shrink while all three are mid-flight
	close(release)
	deadline := time.After(2 * time.Second)
	for done.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d of 3 in-flight jobs finished after the limit was lowered", done.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := l.currentLimit(); got != 1 {
		t.Errorf("limit = %d, want 1", got)
	}
	close(q)
}

// A cap of 0 (unset), a negative, or an absurd number must not stop the machine or fork it apart.
func TestLimitIsClamped(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 1}, {-5, 1}, {1, 1}, {16, 16}, {999, 16}} {
		if got := newRunLimiter(c.in).currentLimit(); got != c.want {
			t.Errorf("newRunLimiter(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
