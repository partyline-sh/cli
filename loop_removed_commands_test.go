package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLoopRemovedCommands(t *testing.T) {
	if command := os.Getenv("PARTYLINE_TEST_REMOVED_LOOP_COMMAND"); command != "" {
		loopMain([]string{command, "unused"})
		return
	}
	for _, command := range []string{"approve", "decide", "stop"} {
		t.Run(command, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoopRemovedCommands$")
			cmd.Env = append(os.Environ(), "PARTYLINE_TEST_REMOVED_LOOP_COMMAND="+command)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), `ctrl-\ then a`) {
				t.Fatalf("exit: %v; output: %s", err, out)
			}
		})
	}
}

func TestGateStandaloneStop(t *testing.T) {
	for _, state := range []string{loopBuilding, loopChecking} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			r := waitingRun(t, state)
			r.Session = nil
			if err := r.save(loopEvent{Type: "test"}); err != nil {
				t.Fatal(err)
			}
			m, calls := fakeGate(t, nil, nil)
			m.act.list = gateRuns
			m.act.stop = func(r *loopRun) error { return loopStopRun(r.ID) }
			m = m.reload()
			if len(m.runs) != 1 || m.runs[0].ID != r.ID {
				t.Fatalf("runs: %+v", m.runs)
			}
			m = gateKeys(m, "a", "r", "f", "d")
			if len(calls.approved)+len(calls.rejected)+len(calls.decided) != 0 {
				t.Fatal("active run offered a decision")
			}
			m = gateKeys(m, "s", "4", "2")
			saved, err := loadLoopRun(r.ID)
			if err != nil || saved.State != loopStopped {
				t.Fatalf("run: %+v, %v; popup: %s", saved, err, m.result)
			}
			events, err := loopEvents(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == "stopped" {
					return
				}
			}
			t.Fatal("no stopped event")
		})
	}
}
