package main

import (
	"os"
	"strings"
	"testing"
)

// A RUN STARTS BECAUSE A HUMAN STARTED IT.
//
// A project marked Auto in this machine's local registry used to drain QUEUED runs the moment they
// arrived. That inverted the backlog: a task dropped in began building immediately, so there was
// nowhere to put work you had not decided to start yet. And it put the decision on the wrong side of
// the wire — the machine executing the work chose which work executed, out of local state the person
// filling the backlog could not see or change.
//
// The gate is now `ev.Go` and nothing else: the owner pressed Start on the board, the server flipped
// the run to `accepted`, and the stream re-pushed it as the execution trigger. Registration says "my
// machine may be used". It never said "run whatever shows up".
//
// This is a ratchet. Re-introducing a local flag that dispatches queued runs is not a feature toggle;
// it is handing the decision back to the machine.
func TestNoLocalFlagCanDispatchAQueuedRun(t *testing.T) {
	for _, f := range []string{"daemon.go", "llms_daemon.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		if strings.Contains(body, "autoRunProject") {
			t.Errorf("%s still references autoRunProject — a queued run can start without anyone pressing Start", f)
		}
	}
}

// The run handlers must gate on the human's Start and nothing else. Checked structurally rather than
// by name so a rename cannot quietly reopen the path.
func TestRunDispatchIsGatedOnTheHumansStart(t *testing.T) {
	src, err := os.ReadFile("llms_daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	h := body[strings.Index(body, "func(ev api.RunEvent)"):]
	if i := strings.Index(h, "\n\t\t\t},"); i > 0 {
		h = h[:i]
	}
	if !strings.Contains(h, "ev.Go") {
		t.Error("the run handler no longer gates on ev.Go — something other than a human Start can dispatch work")
	}
	if strings.Contains(h, "Policy") || strings.Contains(h, "registry") {
		t.Error("the run handler consults local registry state — which machine holds the work must not decide whether it runs")
	}
}
