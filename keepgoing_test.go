package main

import "testing"

func TestKeepGoingDecide(t *testing.T) {
	// Normal turn: decrement, force continuation with the goal in the reason.
	cont, reason, next := keepGoingDecide(keepGoingState{Remaining: 3, Goal: "drain the backlog"}, "still working...")
	if !cont {
		t.Fatal("should continue while turns remain")
	}
	if next.Remaining != 2 {
		t.Fatalf("remaining: got %d want 2", next.Remaining)
	}
	if !contains(reason, "drain the backlog") || !contains(reason, keepGoingDone) {
		t.Fatalf("reason must carry the goal + the done token: %q", reason)
	}

	// Hard cap: 0 remaining → stop, no matter what.
	if cont, _, _ := keepGoingDecide(keepGoingState{Remaining: 0, Goal: "x"}, "keep going!"); cont {
		t.Fatal("must stop when the cap is exhausted")
	}

	// Done sentinel in the last message → stop and clear, even with turns left.
	cont, _, next = keepGoingDecide(keepGoingState{Remaining: 9, Goal: "x"}, "all set. "+keepGoingDone)
	if cont {
		t.Fatal("must stop when the agent emits the done sentinel")
	}
	if next.Remaining != 0 {
		t.Fatalf("state should be cleared on done, got remaining=%d", next.Remaining)
	}

	// Last turn: 1 → continue once more, then next call (0) stops. No off-by-one.
	if cont, _, next := keepGoingDecide(keepGoingState{Remaining: 1, Goal: "x"}, ""); !cont || next.Remaining != 0 {
		t.Fatalf("last turn: cont=%v remaining=%d", cont, next.Remaining)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
