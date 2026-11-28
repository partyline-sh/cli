package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The hook output carries the event each engine's schema requires; claude's stays plain text.
func TestLoopHookEventName(t *testing.T) {
	if got := memoryHookOutput("claude", "ctx\n"); got != "ctx\n" {
		t.Fatalf("claude: %q", got)
	}
	for engine, event := range map[string]string{"codex": "UserPromptSubmit", "gemini": "BeforeAgent"} {
		var out map[string]map[string]any
		if err := json.Unmarshal([]byte(memoryHookOutput(engine, "ctx\n")), &out); err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		h := out["hookSpecificOutput"]
		if len(out) != 1 || len(h) != 2 || h["hookEventName"] != event || h["additionalContext"] != "ctx\n" {
			t.Errorf("%s: %v", engine, out)
		}
	}
}

// A run's messages reach only the session that started it: by its process, alive, the same one
// (start time), on the pane's terminal.
func TestLoopWakeSessionIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pid := os.Getpid()
	out, _ := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	tty := "/dev/" + strings.TrimSpace(string(out))
	r := &loopRun{SessionPID: pid, SessionStarted: processStarted(pid)}
	if r.SessionStarted == "" || !runSessionOn(r, tty) {
		t.Fatalf("this process on its own tty %q was not recognised (%q)", tty, r.SessionStarted)
	}
	if runSessionOn(r, "/dev/ttys999") {
		t.Error("matched another terminal")
	}
	if runSessionOn(&loopRun{SessionPID: pid, SessionStarted: "Mon Jan  1 00:00:00 2001"}, tty) {
		t.Error("matched a different process with the same pid")
	}
	if runSessionOn(&loopRun{}, tty) {
		t.Error("matched a run with no recorded process")
	}

	// The waker: a codex run whose session is not on the pane is neither woken nor charged a wake.
	run := testLoopRun(t, t.TempDir())
	run.SessionEngine = "codex"
	_ = run.save(loopEvent{})
	_ = loopPost(run.ID, "pending")
	oldPanes, oldWake, oldEligible, oldOn := loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane
	t.Cleanup(func() {
		loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane = oldPanes, oldWake, oldEligible, oldOn
	})
	loopPanesNow = func() map[string]string { return map[string]string{"%7": "codex"} }
	loopWakeEligible = func(string) bool { return true }
	wakes, on := 0, false
	loopSessionOnPane = func(*loopRun, string) bool { return on }
	loopWakePane = func(string, string) error { wakes++; return nil }
	loopWakeIdle()
	if wakes != 0 {
		t.Fatal("woke a reused pane")
	}
	if _, err := os.Stat(loopFile(run.ID, "wake.json")); !os.IsNotExist(err) {
		t.Fatal("a reused pane used up a wake")
	}
	on = true
	loopWakeIdle()
	if wakes != 1 {
		t.Fatalf("its own session: %d wakes", wakes)
	}

	// The prompt hook: a finished run found only by pane keeps its messages from another session.
	done := testLoopRun(t, t.TempDir())
	_ = done.set(loopFailed, "failed", "failed", nil)
	_ = loopPost(done.ID, "terminal failure")
	oldIs := loopPromptIsSession
	t.Cleanup(func() { loopPromptIsSession = oldIs })
	loopPromptIsSession = func(*loopRun) bool { return false }
	done.SessionPID, done.SessionStarted = 4242, "x"
	_ = done.save(loopEvent{})
	if ctx := loopPromptContext([]string{"pane:%7"}); strings.Contains(ctx, done.ID) || !loopPending(done.ID) {
		t.Fatalf("another session on the pane got the run: %q", ctx)
	}
	loopPromptIsSession = func(*loopRun) bool { return true }
	if ctx := loopPromptContext([]string{"pane:%7"}); !strings.Contains(ctx, "terminal failure") || loopPending(done.ID) {
		t.Fatalf("its own session did not: %q", ctx)
	}
}
