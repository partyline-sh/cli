package main

import (
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The chain the producer builds must be internally verifiable exactly the way the reader verifies
// it: seq contiguous from 0, each prev_hash == the prior hash, genesis prev_hash == "", and each
// hash reproducible from its own (seq, kind, task_idx, payload). This mirrors the web verifier and
// the (future) Go re-verifier.
func verify(t *testing.T, evs []api.RunLogEvent) {
	t.Helper()
	prev := ""
	for i, e := range evs {
		if e.Seq != i {
			t.Fatalf("event %d: seq = %d, want %d", i, e.Seq, i)
		}
		if e.PrevHash != prev {
			t.Fatalf("event %d: prev_hash = %q, want %q", i, e.PrevHash, prev)
		}
		got := eventHash(e.PrevHash, e.Seq, e.Kind, e.TaskIdx, e.Payload)
		if got != e.Hash {
			t.Fatalf("event %d: hash mismatch — payload/hash not bound (got %s want %s)", i, got, e.Hash)
		}
		prev = e.Hash
	}
}

func upd(idx int, status string) api.RunTaskUpdate {
	return api.RunTaskUpdate{Idx: idx, Task: "do the thing", Status: status}
}

// build+commit produces a contiguous, hash-linked chain.
func TestChainLinksAndVerifies(t *testing.T) {
	c := &chainState{}
	var evs []api.RunLogEvent
	for _, u := range []api.RunTaskUpdate{upd(0, "queued"), upd(0, "running"), upd(0, "done")} {
		ev := c.build(u)
		c.commit(ev)
		evs = append(evs, ev)
	}
	if len(evs) != 3 {
		t.Fatalf("got %d events", len(evs))
	}
	verify(t, evs)
}

// A dropped append (build without commit) must NOT poison the chain: the next transition reuses the
// same seq/prev, so the committed chain stays contiguous — the lost event is simply absent.
func TestDroppedAppendDoesNotPoisonChain(t *testing.T) {
	c := &chainState{}
	first := c.build(upd(0, "queued"))
	c.commit(first)

	// Simulate a failed append: build an event but DON'T commit it (as crank does on POST error).
	dropped := c.build(upd(1, "running"))
	if dropped.Seq != 1 {
		t.Fatalf("dropped event seq = %d, want 1", dropped.Seq)
	}

	// The next successful transition must take seq 1 and chain onto the first event, not the dropped one.
	next := c.build(upd(1, "done"))
	c.commit(next)
	if next.Seq != 1 || next.PrevHash != first.Hash {
		t.Fatalf("after a drop: next seq=%d prev=%s; want seq=1 prev=%s", next.Seq, next.PrevHash, first.Hash)
	}
	verify(t, []api.RunLogEvent{first, next})
}

// Re-seeding (crank --resume / relaunched worker) continues the daemon's existing chain from its
// stored head rather than colliding at seq 0.
func TestReseedContinuesChain(t *testing.T) {
	c := &chainState{}
	e0 := c.build(upd(0, "queued"))
	c.commit(e0)
	e1 := c.build(upd(0, "running"))
	c.commit(e1)

	// New process seeds from the stored head (seq resumes at head+1, prev = head hash) — mirrors
	// api.LastRunEvent's return contract.
	resumed := &chainState{seq: e1.Seq + 1, lastHash: e1.Hash}
	e2 := resumed.build(upd(0, "done"))
	resumed.commit(e2)

	verify(t, []api.RunLogEvent{e0, e1, e2})
	if e2.Seq != 2 {
		t.Fatalf("resumed event seq = %d, want 2", e2.Seq)
	}
}

// task_idx is a pointer so a run-level event (nil) hashes distinctly from task idx 0 — a reorder or
// relabel between them must change the hash.
func TestRunLevelVsTaskZeroDistinct(t *testing.T) {
	zero := 0
	runLevel := eventHash("", 0, "queued", nil, map[string]any{"status": "queued"})
	taskZero := eventHash("", 0, "queued", &zero, map[string]any{"status": "queued"})
	if runLevel == taskZero {
		t.Fatal("run-level (nil task_idx) and task idx 0 produced the same hash")
	}
}
