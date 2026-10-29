package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/clispec"
)

func TestLoopAnyEngine(t *testing.T) {
	t.Run("start_check", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\nprintf '1 %s\\n' \"$LOOP_TEST_ARGV\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
		oldAncestor := chatAncestorPane
		t.Cleanup(func() { chatAncestorPane = oldAncestor })
		for _, c := range []struct {
			argv []string
			pane string
			ok   bool
		}{
			{[]string{"claude", "--settings", `{"hooks":{"UserPromptSubmit":"ptln memory-hook"}}`}, "", true},
			{[]string{"/opt/bin/codex", "--model", "gpt-5", "--sandbox", "workspace-write"}, "%7", true},
			{[]string{"node", "/opt/bin/gemini.js", "--resume"}, "%7", true},
			{[]string{"codex", "--resume"}, "", false},
			{[]string{"gemini"}, "", false},
			{[]string{"claude"}, "%7", false},
			{[]string{"other", "claude"}, "%7", false},
		} {
			if err := loopHooksCheck(engineProgram(c.argv), strings.Join(c.argv, " "), c.pane); (err == nil) != c.ok {
				t.Errorf("%v: %v", c.argv, err)
			}
			t.Setenv("LOOP_TEST_ARGV", strings.Join(c.argv, " "))
			chatAncestorPane = func() string { return c.pane }
			if err := requireLoopHooks(); (err == nil) != c.ok {
				t.Errorf("real start check %v: %v", c.argv, err)
			}
		}
		if loopSingleCheck("gemini") == nil || loopSingleCheck("codex") != nil || loopSingleCheck("claude") != nil {
			t.Fatal("single-run policy")
		}
	})
	t.Run("inbox", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		r := testLoopRun(t, t.TempDir())
		if _, err := loadLoopRun("../../escape"); err == nil {
			t.Fatal("accepted path traversal")
		}
		if got := loopInboxText(r); strings.Contains(got, "came with your prompt") {
			t.Fatal(got)
		}
		if err := loopPost(r.ID, "a result"); err != nil {
			t.Fatal(err)
		}
		got := loopInboxText(r)
		if !strings.Contains(got, "state: building") || !strings.Contains(got, "a result") || loopPending(r.ID) {
			t.Fatal(got)
		}
		before, _ := os.ReadFile(loopFile(r.ID, "inbox-read"))
		if got := loopInboxText(r); !strings.Contains(got, "nothing waiting\n") || strings.Contains(got, "came with your prompt") {
			t.Fatal(got)
		}
		after, _ := os.ReadFile(loopFile(r.ID, "inbox-read"))
		if string(before) == string(after) {
			t.Fatal("empty inbox did not count as read")
		}
		_ = loopPost(r.ID, "prompt delivery")
		if got := loopPromptContext(r.Session); !strings.Contains(got, "prompt delivery") {
			t.Fatal(got)
		}
		if got := loopInboxText(r); !strings.Contains(got, "came with your prompt") {
			t.Fatal(got)
		}
	})
	t.Run("codex_wake", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		r := testLoopRun(t, t.TempDir()) // never joins chat
		r.SessionEngine = "codex"
		if err := r.save(loopEvent{}); err != nil {
			t.Fatal(err)
		}
		oldPanes, oldWake, oldEligible, oldOn := loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane
		t.Cleanup(func() {
			loopPanesNow, loopWakePane, loopWakeEligible, loopSessionOnPane = oldPanes, oldWake, oldEligible, oldOn
		})
		loopSessionOnPane = func(*loopRun, string) bool { return true }

		// Exercise the real safety checks before replacing tmux submission with a counter.
		dir := t.TempDir()
		script := `#!/bin/sh
case "$*" in
 *list-clients*) printf '%s' "$LOOP_TEST_WATCHED" ;;
 *capture-pane*) printf '%s' "$LOOP_TEST_SCREEN" ;;
 *send-keys*) echo sent >> "$LOOP_TEST_SENT" ;;
esac
`
		if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
		t.Setenv("LOOP_TEST_SENT", filepath.Join(dir, "sent"))
		loopPanesNow = func() map[string]string { return map[string]string{"%7": "codex"} }
		loopWakePane = wakePane
		_ = loopPost(r.ID, "safety check")
		t.Setenv("LOOP_TEST_WATCHED", "%7\n")
		t.Setenv("LOOP_TEST_SCREEN", "› ")
		loopWakeIdle()
		t.Setenv("LOOP_TEST_WATCHED", "")
		t.Setenv("LOOP_TEST_SCREEN", "• Working (1m 12s • esc to interrupt)\n› ")
		loopWakeIdle()
		t.Setenv("LOOP_TEST_SCREEN", "› typed by the person")
		loopWakeIdle()
		if _, err := os.Stat(filepath.Join(dir, "sent")); !os.IsNotExist(err) {
			t.Fatal("real wake typed into a blocked pane")
		}
		loopMarkRead(r.ID)
		engine, blocked, wakes := "codex", "", 0
		loopWakeEligible = func(string) bool { return blocked == "" }
		loopPanesNow = func() map[string]string { return map[string]string{"%7": engine} }
		loopWakePane = func(pane, line string) error {
			if blocked != "" {
				return fmt.Errorf("%s", blocked)
			}
			want := fmt.Sprintf("[partyline loop %s] You have a loop message. Run `ptln loop inbox %s` and follow it.", r.ID, r.ID)
			if pane != "%7" || line != want {
				t.Fatalf("%s: %s", pane, line)
			}
			wakes++
			return nil
		}
		_ = loopPost(r.ID, "one")
		for _, why := range []string{"working", "watched", "typed input"} {
			blocked = why
			loopWakeIdle()
		}
		if wakes != 0 {
			t.Fatal("woke blocked pane")
		}
		blocked = ""
		loopMarkRead(r.ID)
		engine = "claude"
		loopWakeIdle()
		if wakes != 0 {
			t.Fatal("typed into claude")
		}
		engine = "gemini"
		loopWakeIdle()
		if wakes != 0 {
			t.Fatal("typed into gemini")
		}
		engine = "codex"
		loopWakeIdle()
		loopWakeIdle()
		if wakes != 1 {
			t.Fatalf("same batch woke %d times", wakes)
		}
		for i := 0; i < 5; i++ {
			_ = loopPost(r.ID, fmt.Sprint(i))
			loopWakeIdle()
		}
		if wakes != 3 {
			t.Fatalf("without reads: %d wakes", wakes)
		}
		// Prompt delivery and an explicit empty inbox both reset the persisted cap.
		loopPromptContext(r.Session)
		_ = loopPost(r.ID, "after read")
		loopWakeIdle()
		if wakes != 4 {
			t.Fatalf("after read: %d wakes", wakes)
		}
		loopInboxText(r)
		loopInboxText(r)
		_ = loopPost(r.ID, "after empty inbox read")
		loopWakeIdle()
		if wakes != 5 {
			t.Fatalf("after empty read: %d", wakes)
		}
		loopMarkRead(r.ID)
		attempts := 0
		loopWakePane = func(pane, line string) error {
			// Acquiring the same lock here proves tmux runs outside the lock.
			if err := withRunLock(r.ID, func(*loopRun) error { return nil }); err != nil {
				t.Fatal(err)
			}
			attempts++
			return fmt.Errorf("typed but failed to submit")
		}
		for i := 0; i < 10; i++ {
			_ = loopPost(r.ID, fmt.Sprint("failure ", i))
			loopWakeIdle()
			loopWakeIdle()
		}
		if attempts != 3 {
			t.Fatalf("failed attempts: %d", attempts)
		}
		loopMarkRead(r.ID)
		r.SessionEngine = "gemini"
		_ = r.save(loopEvent{})
		loopWakeIdle()
		r.SessionEngine = "codex"
		r.State = loopStopped
		_ = r.save(loopEvent{})
		loopWakeIdle()
		if attempts != 3 {
			t.Fatal("woke stopped run or non-codex parent")
		}
		r.State = loopFailed
		_ = r.save(loopEvent{})
		loopWakeIdle()
		if attempts != 3 {
			t.Fatal("woke failed run")
		}

	})
	t.Run("hook_shapes", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PARTYLINE_MEMORY_REFRESH", "off")
		oldEngine, oldServer := memoryHookEngine, tmuxServerUp
		t.Cleanup(func() { memoryHookEngine, tmuxServerUp = oldEngine, oldServer })
		tmuxServerUp = func() bool { return false }
		r := testLoopRun(t, t.TempDir())
		r.Session = []string{"hook-session"}
		r.SessionEngine = "gemini"
		r.Batch = "missing-test-batch"
		r.State = loopFailed
		if err := r.save(loopEvent{}); err != nil {
			t.Fatal(err)
		}
		for _, engine := range []string{"claude", "codex", "gemini"} {
			_ = loopPost(r.ID, "terminal builder failure")
			memoryHookEngine = func() (int, string) { return 1, engine }
			in, _ := os.CreateTemp(t.TempDir(), "stdin")
			_, _ = in.WriteString(`{"session_id":"hook-session"}`)
			_, _ = in.Seek(0, 0)
			out, _ := os.CreateTemp(t.TempDir(), "stdout")
			oldIn, oldOut := os.Stdin, os.Stdout
			func() {
				defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
				os.Stdin, os.Stdout = in, out
				memoryHookMain()
			}()
			_ = in.Close()
			_, _ = out.Seek(0, 0)
			b, _ := io.ReadAll(out)
			_ = out.Close()
			context := string(b)
			if engine != "claude" {
				var payload struct {
					Hook struct {
						Context string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(b, &payload); err != nil {
					t.Fatal(err)
				}
				context = payload.Hook.Context
			} else if strings.HasPrefix(context, "{") {
				t.Fatal(context)
			}
			if !strings.Contains(context, "state: failed") || !strings.Contains(context, "terminal builder failure") {
				t.Fatal(context)
			}
			if loopPending(r.ID) {
				t.Fatal("failure not consumed")
			}
		}

		for _, engine := range []string{"claude", "codex", "gemini"} {
			got := memoryHookOutput(engine, "state and messages\n")
			if engine == "claude" {
				if got != "state and messages\n" {
					t.Fatal(got)
				}
				continue
			}
			var out struct {
				Hook struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(got), &out); err != nil || out.Hook.Context != "state and messages\n" {
				t.Fatalf("%s: %s", engine, got)
			}
		}
	})
	t.Run("advice", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		out, err := os.CreateTemp(t.TempDir(), "help")
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdout
		func() {
			defer func() { os.Stdout = saved }()
			os.Stdout = out
			printLoopHelp()
			loopMain([]string{"--help"})
		}()
		_ = out.Close()
		help, err := os.ReadFile(out.Name())
		if err != nil || strings.Count(string(help), "inbox <id>") != 2 {
			t.Fatalf("usage and --help: %s (%v)", help, err)
		}

		for _, engine := range []string{"claude", "codex", "gemini"} {
			got := loopAdvice(&loopRun{ID: "example", SessionEngine: engine})
			switch engine {
			case "claude":
				if !strings.Contains(got, "loop listen example") || !strings.Contains(got, "background and end your turn") {
					t.Fatal(got)
				}
			case "codex":
				if strings.Contains(got, "loop listen") || !strings.Contains(got, "loop inbox example") {
					t.Fatal(got)
				}
			case "gemini":
				if strings.Contains(got, "loop listen") || !strings.Contains(got, "next prompt") {
					t.Fatal(got)
				}
			}
		}
		// Every former advice site goes through the same engine-aware formatter.
		for file, count := range map[string]int{"loop_start.go": 2, "loop_batch_cmd.go": 4, "loop_round.go": 1, "loop_batch_review.go": 1, "loop_dispute.go": 2} {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "`ptln loop listen") || strings.Count(string(b), "loopAdvice(") != count {
				t.Fatalf("unconverted advice in %s", file)
			}
		}
		spec, ok := clispec.Lookup("loop")
		if !ok || !strings.Contains(strings.Join(spec.Usage, "\n"), "inbox <id>") {
			t.Fatal("inbox missing from loop usage")
		}
		for _, file := range []string{"docs/partyline.1", "web/src/app/(marketing)/docs/sessions/page.mdx"} {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "inbox <id>") {
				t.Fatalf("inbox missing from %s", file)
			}
		}
	})
}
