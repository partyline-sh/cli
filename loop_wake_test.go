package main

import (
	"os"
	"testing"
)

func TestLoopWakeRunEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, state, engine, paneEngine string
		want                            bool
	}{
		{"live codex", loopBuilding, "codex", "codex", true},
		// No recorded engine: a run from before codex could start loops, so a codex pane is reused.
		{"legacy codex", loopBuilding, "", "codex", false},
		{"failed", loopFailed, "codex", "codex", false},
		{"stopped", loopStopped, "codex", "codex", false},
		{"completed", loopPRReady, "codex", "codex", false},
		{"reused claude pane", loopBuilding, "claude", "codex", false},
		{"reused gemini pane", loopBuilding, "gemini", "codex", false},
		{"legacy other pane", loopBuilding, "", "gemini", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			r := testLoopRun(t, t.TempDir())
			r.State, r.SessionEngine = tc.state, tc.engine
			if err := r.save(loopEvent{}); err != nil {
				t.Fatal(err)
			}
			if err := loopPost(r.ID, "pending message"); err != nil {
				t.Fatal(err)
			}
			oldPanes, oldWake, oldEligible, oldOn := loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane
			t.Cleanup(func() {
				loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane = oldPanes, oldWake, oldEligible, oldOn
			})
			loopSessionOnPane = func(*loopRun, string) bool { return true }
			loopPanesNow = func() map[string]string { return map[string]string{"%7": tc.paneEngine} }
			loopWakeEligible = func(string) bool { return true }
			wakes := 0
			loopWakePane = func(string, string) error { wakes++; return nil }
			loopWakeIdle()
			if (wakes == 1) != tc.want {
				t.Fatalf("wakes = %d, want wake %v", wakes, tc.want)
			}
			if !tc.want {
				if _, err := os.Stat(loopFile(r.ID, "wake.json")); !os.IsNotExist(err) {
					t.Fatalf("ineligible run reserved a wake: %v", err)
				}
			}
		})
	}
}

func TestLoopWakeRechecksRunUnderLock(t *testing.T) {
	for _, change := range []string{"state", "engine"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			r := testLoopRun(t, t.TempDir())
			r.SessionEngine = "codex"
			if err := r.save(loopEvent{}); err != nil {
				t.Fatal(err)
			}
			if err := loopPost(r.ID, "pending message"); err != nil {
				t.Fatal(err)
			}
			oldPanes, oldWake, oldEligible, oldOn := loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane
			t.Cleanup(func() {
				loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane = oldPanes, oldWake, oldEligible, oldOn
			})
			loopSessionOnPane = func(*loopRun, string) bool { return true }
			loopPanesNow = func() map[string]string { return map[string]string{"%7": "codex"} }
			loopWakeEligible = func(string) bool {
				if err := withRunLock(r.ID, func(current *loopRun) error {
					if change == "state" {
						current.State = loopFailed
					} else {
						current.SessionEngine = "gemini"
					}
					return current.save(loopEvent{})
				}); err != nil {
					t.Fatal(err)
				}
				return true
			}
			loopWakePane = func(string, string) error { t.Fatal("woke run after eligibility changed"); return nil }
			loopWakeIdle()
			if _, err := os.Stat(loopFile(r.ID, "wake.json")); !os.IsNotExist(err) {
				t.Fatalf("reserved wake after eligibility changed: %v", err)
			}
		})
	}
}
