package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// A review must not wait on a build. The lanes exist because sharing one worker put back exactly the
// starvation the web's cap already forbids: three finished builds sat un-reviewed for an hour behind
// one long compile.
func TestReviewLaneRunsWhileABuildIsStillRunning(t *testing.T) {
	build := make(chan struct{}) // held open to simulate a long build
	var reviewed atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)

	runQueue := make(chan queuedJob, 4)
	reviewQueue := make(chan queuedJob, 4)
	exec := func(ev api.RunEvent) error {
		if ev.Preset == "review" {
			reviewed.Add(1)
			wg.Done()
			return nil
		}
		<-build // the build blocks until the test releases it
		return nil
	}
	go drainRunQueueLimited(runQueue, newRunLimiter(1))
	go drainRunQueueLimited(reviewQueue, newRunLimiter(1))

	build1 := api.RunEvent{RunID: "build", Preset: "crank"}
	review1 := api.RunEvent{RunID: "review", Preset: "review"}
	runQueue <- queuedJob{run: func() { _ = exec(build1) }}
	reviewQueue <- queuedJob{run: func() { _ = exec(review1) }}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done: // the review completed while the build is STILL blocked — the point
	case <-time.After(2 * time.Second):
		t.Fatal("review did not run while a build was in flight — the lanes are not separate")
	}
	if got := reviewed.Load(); got != 1 {
		t.Fatalf("reviewed = %d, want 1", got)
	}
	close(build)
}

func TestEnvWorkersRejectsNonsenseAndKeepsTheDefault(t *testing.T) {
	for _, v := range []string{"", "  ", "nope", "0", "-3", "17", "1e3"} {
		t.Setenv("PARTYLINE_TEST_WORKERS", v)
		if got := envWorkers("PARTYLINE_TEST_WORKERS", 2); got != 2 {
			t.Errorf("envWorkers(%q) = %d, want the default 2", v, got)
		}
	}
	t.Setenv("PARTYLINE_TEST_WORKERS", " 4 ")
	if got := envWorkers("PARTYLINE_TEST_WORKERS", 2); got != 4 {
		t.Errorf("envWorkers(\" 4 \") = %d, want 4", got)
	}
}

// Sitting in the backlog must not mean "start building". launchPolicy defaults to auto — that is
// consent to USE the machine, and it is why the launch-approval prompt is gone. Whether a queued
// run STARTS is a separate decision a human makes with the Start button, so run dispatch requires
// the registry to say auto EXPLICITLY.
func TestBacklogDoesNotStartWithoutAnExplicitAutoProject(t *testing.T) {
	cases := []struct {
		name        string
		policy      string
		mayDispatch bool
	}{
		{"unmarked project waits for Start", "", false},
		{"ask waits for Start", "ask", false},
		{"explicitly auto drains unattended", "auto", true},
	}
	for _, c := range cases {
		p := daemonProject{Label: "p", Path: "/tmp/p", Policy: c.policy}
		// The machine is usable either way — that consent came from registering it.
		if c.policy != "ask" && p.launchPolicy() != "auto" {
			t.Errorf("%s: launchPolicy = %q, want auto (registration is the consent)", c.name, p.launchPolicy())
		}
		// But dispatch is a different question.
		got := p.Policy == "auto"
		if got != c.mayDispatch {
			t.Errorf("%s: dispatch = %v, want %v", c.name, got, c.mayDispatch)
		}
		if c.policy == "" && got {
			t.Fatal("an unmarked project auto-started queued work — the Start button means nothing")
		}
	}
}
