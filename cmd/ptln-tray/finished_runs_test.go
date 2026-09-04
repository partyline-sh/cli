//go:build darwin && tray

package main

import "testing"

func rows(ids ...string) []finishedRun {
	out := make([]finishedRun, 0, len(ids))
	for _, id := range ids {
		out = append(out, finishedRun{RunID: id, Project: "proj", Status: "done", At: "2026-08-26T00:00:00Z"})
	}
	return out
}

// A tray that launches and immediately fires six notifications about runs that finished while it was
// closed is noise, not news. The first poll seeds silently.
func TestTheFirstPollIsSilent(t *testing.T) {
	w := newFinishedWatch()
	if n := w.notices(rows("a", "b", "c")); len(n) != 0 {
		t.Errorf("the seeding poll posted %d notification(s): %v", len(n), n)
	}
}

// `ptln state` republishes the same rows for up to two hours. Notifying on PRESENCE would re-fire
// every poll — which is how a notification feature becomes the first thing someone turns off.
func TestARunIsAnnouncedExactlyOnce(t *testing.T) {
	w := newFinishedWatch()
	w.notices(rows("a")) // seed

	first := w.notices(rows("b", "a"))
	if len(first) != 1 {
		t.Fatalf("a new run produced %d notification(s), want 1: %v", len(first), first)
	}
	for i := 0; i < 5; i++ {
		if n := w.notices(rows("b", "a")); len(n) != 0 {
			t.Fatalf("poll %d re-announced a run it had already reported: %v", i, n)
		}
	}
}

// The status is the whole point: "a run finished" is not worth interrupting someone for if they
// cannot tell from the banner whether it worked or needs them.
func TestTheBodySaysWhatHappened(t *testing.T) {
	for _, tc := range []struct {
		status string
		tasks  int
		want   string
	}{
		{"done", 1, "proj — done"},
		{"done", 4, "proj — 4 tasks done"},
		{"failed", 1, "proj — failed"},
		{"needs_approval", 1, "proj — needs you"},
		{"killed", 1, "proj — stopped"},
	} {
		got := finishedBody(finishedRun{Project: "proj", Status: tc.status, Tasks: tc.tasks})
		if got != tc.want {
			t.Errorf("status %q → %q, want %q", tc.status, got, tc.want)
		}
	}
}

// An older `ptln` omits the key entirely, so the tray must treat "no field" and "nothing happened"
// as the same quiet — the same degrade-to-hidden contract Peers and Invites already have.
func TestAnOlderCLIIsQuietNotBroken(t *testing.T) {
	w := newFinishedWatch()
	w.notices(nil)
	if n := w.notices(nil); len(n) != 0 {
		t.Errorf("a CLI with no finished field produced %v", n)
	}
}

// A row with no id cannot be de-duplicated, so announcing it would repeat forever.
func TestARowWithNoIdIsIgnored(t *testing.T) {
	w := newFinishedWatch()
	w.notices(rows("seed"))
	if n := w.notices([]finishedRun{{Status: "done", Project: "p"}}); len(n) != 0 {
		t.Errorf("an id-less row was announced: %v", n)
	}
}
