package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// fakeSink records what the feed ships, and can fail or stall on demand — the two ways telemetry
// could plausibly cost an agent its turn.
type fakeSink struct {
	mu    sync.Mutex
	lines []api.PartyActivityLine
	err   error
	delay time.Duration
}

func (f *fakeSink) AppendActivity(name string, lines []api.PartyActivityLine) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, lines...)
	return f.err
}

func (f *fakeSink) bodies(stream string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.lines {
		if l.Stream == stream {
			out = append(out, l.Body)
		}
	}
	return out
}

// A heartbeat is its OWN stream, never step output — the web filters on exactly this, and a
// heartbeat that arrived as a "step" would render in the activity feed as the bare word "alive".
func TestHeartbeatIsNotStepOutput(t *testing.T) {
	sink := &fakeSink{}
	feed := &activityFeed{pc: sink, name: "scout"}
	feed.beat(beatStart)
	feed.add("→ Read(main.go)")
	feed.beat(beatEnd)
	feed.flush()

	if got := sink.bodies("heartbeat"); len(got) != 2 || got[0] != beatStart || got[1] != beatEnd {
		t.Fatalf("heartbeat stream = %v, want [start end]", got)
	}
	if got := sink.bodies("step"); len(got) != 1 || got[0] != "→ Read(main.go)" {
		t.Fatalf("step stream = %v, want the one tool line", got)
	}
}

// Seq keeps rising across heartbeats and steps alike: the feed is one ordered channel, and a
// heartbeat that reused a seq would let the web mistake it for a replay of an earlier line.
func TestHeartbeatSeqIsMonotonic(t *testing.T) {
	sink := &fakeSink{}
	feed := &activityFeed{pc: sink, name: "scout"}
	feed.beat(beatStart)
	feed.add("→ Grep(foo)")
	feed.setUsage(10, 20)
	feed.beat(beatAlive)
	feed.flush()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.lines) != 4 {
		t.Fatalf("shipped %d lines, want 4", len(sink.lines))
	}
	for i, l := range sink.lines {
		if l.Seq != int64(i+1) {
			t.Fatalf("line %d has seq %d, want %d", i, l.Seq, i+1)
		}
	}
}

// The load-bearing promise: a heartbeat write that FAILS must not fail, delay or abort the turn.
func TestHeartbeatWriteFailureDoesNotBreakTheTurn(t *testing.T) {
	sink := &fakeSink{err: errors.New("502 from the control plane")}
	feed := &activityFeed{pc: sink, name: "scout"}

	feed.beat(beatStart)
	feed.flush() // must not panic, must not surface the error to the caller
	feed.add("→ Bash(go test ./...)")
	feed.beat(beatEnd)
	feed.flush()

	if got := sink.bodies("step"); len(got) != 1 {
		t.Fatalf("a failed heartbeat batch must not stop later lines being attempted, got %v", got)
	}
}

// beat() buffers; it never waits on the network. A sink that stalls is the runner's worst case (the
// control plane wedged), and the agent must keep generating straight through it.
func TestHeartbeatDoesNotBlockOnASlowSink(t *testing.T) {
	sink := &fakeSink{delay: 2 * time.Second}
	feed := &activityFeed{pc: sink, name: "scout"}

	feed.beat(beatStart)
	go feed.flush() // occupies the sink for two seconds

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			feed.beat(beatAlive)
			feed.add("→ still working")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("beat/add blocked behind an in-flight flush — telemetry must never gate the turn")
	}
}

// Batches must reach the wire in the order they were buffered. The web decides "did this turn finish"
// from row order, and the DB stamps created_at per INSERT — so a `end` batch that overtakes an earlier
// `alive` batch makes a cleanly finished turn look like a runner that died mid-thought.
func TestFlushesShipInBufferOrder(t *testing.T) {
	// The first flush stalls on the wire; a second one starts while it is still in flight, exactly as
	// the 600ms ticker and the turn's final drain do.
	sink := &fakeSink{delay: 200 * time.Millisecond}
	feed := &activityFeed{pc: sink, name: "scout"}

	feed.beat(beatAlive)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		feed.flush()
	}()
	time.Sleep(20 * time.Millisecond) // let the first flush get onto the wire
	feed.beat(beatEnd)
	feed.flush()
	wg.Wait()

	got := sink.bodies("heartbeat")
	if len(got) != 2 || got[0] != beatAlive || got[1] != beatEnd {
		t.Fatalf("shipped %v, want [alive end] — the end beat must never overtake an earlier batch", got)
	}
}

// A feed with no sink is telemetry that simply isn't wired. It must be inert, not a nil dereference
// that takes the agent's process down — the field is an interface now, and a nil interface panics on
// call rather than inside the method.
func TestFlushWithNoSinkIsInert(t *testing.T) {
	feed := &activityFeed{name: "scout"} // pc left nil
	feed.beat(beatStart)
	feed.add("→ Read(main.go)")
	feed.setUsage(1, 2)
	feed.flush() // must not panic
	feed.beat(beatEnd)
	feed.flush()
}

// The teardown race. The flusher's select has `<-stop` and `<-heartbeat tick` both ready at the end of
// a turn, and Go picks a ready case at RANDOM — so without `end` being terminal, an `alive` gets
// appended after it, in the same batch, with a higher seq. Same created_at + higher seq is exactly
// what the web's tie-break reads, so a cleanly finished turn renders as "its runner may have stopped".
// Hammered with a 1ms heartbeat so the two are ready together on essentially every iteration.
func TestEndIsAlwaysTheLastBeatOfATurn(t *testing.T) {
	for i := 0; i < 200; i++ {
		sink := &fakeSink{}
		feed := &activityFeed{pc: sink, name: "scout", beatEvery: time.Millisecond, flushEvery: time.Millisecond}
		end := feed.startTurn()
		time.Sleep(3 * time.Millisecond) // let the heartbeat ticker get going
		end()

		got := sink.bodies("heartbeat")
		if len(got) < 2 {
			t.Fatalf("iteration %d: shipped %v, want at least start+end", i, got)
		}
		if got[0] != beatStart {
			t.Fatalf("iteration %d: first beat is %q, want start", i, got[0])
		}
		if last := got[len(got)-1]; last != beatEnd {
			t.Fatalf("iteration %d: last beat is %q, want end (beats: %v)", i, last, got)
		}
		for _, b := range got[1 : len(got)-1] {
			if b == beatEnd {
				t.Fatalf("iteration %d: end appears before the turn's last beat: %v", i, got)
			}
		}
	}
}

// endTurn is idempotent — wake defers it, and a second call must not emit a second `end` (which would
// be a heartbeat after the terminal one) or re-close the stop channel.
func TestEndTurnIsIdempotent(t *testing.T) {
	sink := &fakeSink{}
	feed := &activityFeed{pc: sink, name: "scout", beatEvery: time.Hour, flushEvery: time.Hour}
	end := feed.startTurn()
	end()
	end()
	if got := sink.bodies("heartbeat"); len(got) != 2 || got[0] != beatStart || got[1] != beatEnd {
		t.Fatalf("shipped %v, want exactly [start end]", got)
	}
}

// The cost of the turn's exit path. Ordered delivery means the final drain can queue behind ONE
// in-flight flush — that is the price of `end` never overtaking an earlier batch, and it is paid after
// the agent's reply has already been posted. What must NOT happen is it scaling with the turn's
// backlog, so this pins it at roughly one POST rather than one per buffered batch.
func TestTurnExitWaitsForAtMostOneInFlightFlush(t *testing.T) {
	const post = 150 * time.Millisecond
	sink := &fakeSink{delay: post}
	feed := &activityFeed{pc: sink, name: "scout", beatEvery: time.Hour, flushEvery: 5 * time.Millisecond}
	end := feed.startTurn()
	for i := 0; i < 50; i++ {
		feed.add("→ Bash(go test ./...)")
	}
	time.Sleep(10 * time.Millisecond) // a flush is now on the wire

	started := time.Now()
	end()
	if waited := time.Since(started); waited > 3*post {
		t.Fatalf("turn exit waited %v for a %v POST — the final drain must not queue behind the backlog", waited, post)
	}
}

// An empty flush is a no-op rather than an empty POST — heartbeats tick on their own schedule and
// would otherwise turn every idle 600ms tick into a request.
func TestFlushSkipsEmptyBatches(t *testing.T) {
	sink := &fakeSink{}
	feed := &activityFeed{pc: sink, name: "scout"}
	feed.flush()
	if len(sink.bodies("heartbeat"))+len(sink.bodies("step")) != 0 {
		t.Fatal("empty flush shipped something")
	}
}
