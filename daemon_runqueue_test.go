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
	queue := make(chan api.RunEvent, n)

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

	go drainRunQueue(queue, exec)
	for i := 0; i < n; i++ {
		queue <- api.RunEvent{RunID: "r" + strconv.Itoa(i)}
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
