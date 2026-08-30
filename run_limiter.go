package main

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// run_limiter.go — how many builds this machine runs at once, and who decides.
//
// The board's Concurrency dialog has a JOBS PER MACHINE field. Until now it was a promise the
// daemon didn't keep: the web withheld runs beyond the cap (correct, and deterministic across the
// fleet), but the daemon then fed everything through ONE worker, so setting 2 changed nothing —
// the web dispatched two and the daemon ran them one after the other. A number in a settings
// dialog that silently does nothing is worse than no setting at all.
//
// THE WEB REMAINS THE AUTHORITY. It computes the cap from org-wide rows every daemon can see, which
// is what stops a fleet collectively exceeding it (reference-not-command: the daemon runs what it's
// handed). This limiter is the machine-local half — it stops the daemon being a tighter, invisible
// second cap, and it bounds concurrency so a bad number can't fork a laptop into the ground.
//
// The limit is LIVE: it arrives on run events, so changing the dialog takes effect on the next
// dispatch rather than needing a daemon restart.
type runLimiter struct {
	mu     sync.Mutex
	cond   *sync.Cond
	limit  int
	active int
}

// maxInt: the build limit floor. Go 1.21's builtin `max` exists, but this file is read by anyone
// debugging concurrency and an explicit name beats a builtin here.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// envWorkers reads a worker-count override, clamped to a sane range. Anything unset, unparseable or
// out of range falls back to the default rather than failing a daemon start over a typo'd env var.
func envWorkers(key string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || n < 1 || n > 16 {
		return def
	}
	return n
}

func newRunLimiter(limit int) *runLimiter {
	l := &runLimiter{limit: clampWorkers(limit)}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// clampWorkers keeps a limit sane whatever it came from — an unset cap, a typo'd env var, or a
// server value. 1 is the floor (zero workers would silently stop the machine, which reads exactly
// like a hang) and 16 the ceiling (past it you're thrashing, not parallelising).
func clampWorkers(n int) int {
	if n < 1 {
		return 1
	}
	if n > 16 {
		return 16
	}
	return n
}

// setLimit raises or lowers the ceiling. RAISING wakes waiters immediately; LOWERING never
// interrupts work already running — those finish, and the new limit applies as slots free. Killing
// a live build to satisfy a settings change would lose real work for a number.
func (l *runLimiter) setLimit(n int) {
	n = clampWorkers(n)
	l.mu.Lock()
	defer l.mu.Unlock()
	if n == l.limit {
		return
	}
	l.limit = n
	l.cond.Broadcast()
}

func (l *runLimiter) currentLimit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

// acquire blocks until a slot is free.
func (l *runLimiter) acquire() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.active >= l.limit {
		l.cond.Wait()
	}
	l.active++
}

func (l *runLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active > 0 {
		l.active--
	}
	l.cond.Broadcast()
}

// drainRunQueueLimited is the build lane: take a run, wait for a slot, run it CONCURRENTLY with
// whatever else is in flight, up to the limit. Replaces the strictly-serial worker, which was the
// reason JOBS PER MACHINE could never mean anything above 1.
//
// A run that fails does not stop the lane (same contract as before — chain ordering is gated
// server-side, not here). The goroutine per run is what makes the limit the only bound.
func drainRunQueueLimited(queue <-chan queuedJob, l *runLimiter) {
	for job := range queue {
		l.acquire()
		go func(j queuedJob) {
			defer l.release()
			j.run()
		}(job)
	}
}

// queuedJob is one unit of queued work. A closure rather than an event + func pair so the lane
// stays ignorant of what it's running — which is what lets the review lane share this code.
// (Named queuedJob, not runJob: `runJob` is already the daemon's job-record helper.)
type queuedJob struct{ run func() }
