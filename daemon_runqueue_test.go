package main

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The serial run-queue worker (concurrency 1) is what keeps a batch Started from the web from
// spawning N cranks at once. drainRunQueue is the extracted worker; here we feed it a fake exec that
// records its own high-water concurrency, proving runs execute strictly one-at-a-time — and that a
// "failing" run does NOT stall the queue, so every run still drains in FIFO order.
func TestDrainRunQueueSerial(t *testing.T) {
	const n = 8
	queue := make(chan queuedJob, n)

	var mu sync.Mutex
	var inFlight, maxConc int
	var order []string
	var wg sync.WaitGroup
	wg.Add(n)

	exec := func(ev api.RunEvent) error {
		mu.Lock()
		inFlight++
		if inFlight > maxConc {
			maxConc = inFlight
		}
		mu.Unlock()

		time.Sleep(2 * time.Millisecond) // widen the window so any overlap would be observed

		mu.Lock()
		order = append(order, ev.RunID)
		inFlight--
		mu.Unlock()

		wg.Done()
		if ev.RunID == "r3" {
			return errors.New("boom") // a failure must not stop the queue
		}
		return nil
	}

	// Limit 1 IS the old serial worker — this test still pins that contract, it just now says
	// explicitly which setting produces it.
	go drainRunQueueLimited(queue, newRunLimiter(1))
	for i := 0; i < n; i++ {
		ev := api.RunEvent{RunID: "r" + strconv.Itoa(i)}
		queue <- queuedJob{run: func() { _ = exec(ev) }}
	}
	wg.Wait()
	close(queue)

	if maxConc != 1 {
		t.Fatalf("expected serial execution (max concurrency 1), got %d", maxConc)
	}
	if len(order) != n {
		t.Fatalf("expected all %d runs drained, got %d (%v)", n, len(order), order)
	}
	for i := 0; i < n; i++ {
		if want := "r" + strconv.Itoa(i); order[i] != want {
			t.Fatalf("expected FIFO drain order, got %v", order)
		}
	}
}

// The control plane re-pushes every still-pending run on each stream reconnect. announcePendingRun
// must announce a given run only ONCE — a reconnect re-push (already pending) and a run the queue
// already took (already started) must both be silent. Without this the console re-printed the
// approve-run prompt on every reconnect: 30 stuck runs → ~25k prompt lines.
func TestAnnouncePendingRunDedupsReconnectRepushes(t *testing.T) {
	var mu sync.Mutex
	pendingRuns := map[string]api.RunEvent{}
	startedRuns := map[string]bool{}
	ev := api.RunEvent{RunID: "run-1", ProjectLabel: "partyline"}

	if !announcePendingRun(&mu, pendingRuns, startedRuns, ev) {
		t.Fatal("first sighting must announce")
	}
	for i := 0; i < 50; i++ { // simulate 50 reconnect re-pushes of the same pending run
		if announcePendingRun(&mu, pendingRuns, startedRuns, ev) {
			t.Fatalf("reconnect re-push %d must be silent", i)
		}
	}

	// A run the serial queue already took (startedRuns) must never re-announce as pending either.
	startedRuns["run-2"] = true
	if announcePendingRun(&mu, pendingRuns, startedRuns, api.RunEvent{RunID: "run-2"}) {
		t.Fatal("an already-started run must not announce as pending")
	}

	// A genuinely new run still announces.
	if !announcePendingRun(&mu, pendingRuns, startedRuns, api.RunEvent{RunID: "run-3"}) {
		t.Fatal("a new run must announce")
	}
}

// Same guarantee for the launch path.
func TestAnnouncePendingLaunchDedupsReconnectRepushes(t *testing.T) {
	var mu sync.Mutex
	pending := map[string]pendingLaunch{}
	ev := api.LaunchEvent{RequestID: "req-1", ProjectLabel: "partyline"}

	if !announcePendingLaunch(&mu, pending, ev) {
		t.Fatal("first sighting must announce")
	}
	for i := 0; i < 50; i++ {
		if announcePendingLaunch(&mu, pending, ev) {
			t.Fatalf("reconnect re-push %d must be silent", i)
		}
	}
	if !announcePendingLaunch(&mu, pending, api.LaunchEvent{RequestID: "req-2"}) {
		t.Fatal("a new launch must announce")
	}
}
