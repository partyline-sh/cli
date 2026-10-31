package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaude is a claude on PATH for TestLoopBuilderTmux: as a builder (stream-json) it writes
// ok1.txt, waits so the test can see its chip, and reports; as a reviewer it approves.
const fakeClaude = `#!/bin/sh
cat >/dev/null
case "$*" in
*stream-json*)
  echo built > ok1.txt
  sleep 2
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"ok1.txt"}}]}}'
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"## Evidence\nok1.txt written\n## Hardest decision\nnone\n## Rejected alternatives\nnone\n## Least confident\nnone\n## What an excellent reviewer would criticise\nnothing"}'
  ;;
*)
  printf '%s\n' '{"result":"{\"findings\":[],\"verdict\":\"approve\"}","is_error":false,"usage":{}}'
  ;;
esac
`

// TestLoopBuilderTmux runs a one-step batch on a scratch tmux server with nothing in partyline
// stubbed: the baseline and rounds are `loop _round` processes, the builder a contractor window
// running `loop _build` with a fake claude. Its chip shows under the batch's session while it runs,
// and the step ends committed. Only with PARTYLINE_TMUX_E2E=1.
func TestLoopBuilderTmux(t *testing.T) {
	if os.Getenv("PARTYLINE_TMUX_E2E") != "1" {
		t.Skip("set PARTYLINE_TMUX_E2E=1 to run against a scratch tmux server")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmuxTestServer(t, "ptln-test-builder")
	if err := writeTmuxConf(); err != nil {
		t.Fatal(err)
	}
	repo, _ := loopRepo(t)
	if out, err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "-c", repo, "--", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = tmuxCmd("source-file", stateDir()+"/tmux.conf").Run()
	parent, _ := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}").Output()
	oldSpawn, oldStart, oldAdvance := spawnRound, startLoopBuilder, builderAdvance
	t.Cleanup(func() { spawnRound, startLoopBuilder, builderAdvance = oldSpawn, oldStart, oldAdvance })
	spawnRound, startLoopBuilder, builderAdvance = spawnLoopRound, realStartLoopBuilder, realBuilderAdvance

	b, r, err := createBatch(repo, []string{"pane:" + strings.TrimSpace(string(parent))}, []string{"true"}, "claude", []string{stepBrief("One", "ok1.txt")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	base, _ := gitMem(repo, "rev-parse", "HEAD")
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	tab, _ := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_index} #{window_name}").Output()
	chip := strings.TrimSpace(string(tab)) + ": "
	sawChip := false
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if row := tmuxFormat.ReplaceAllString(contractorRow(loadContractors(), contractorParentName, time.Now()), ""); strings.Contains(row, chip) && strings.Contains(row, "claude · step 1 build 1") {
			sawChip = sawChip || strings.Contains(row, "⠋") || !strings.ContainsAny(row, "✓✗")
		}
		r, _ = loadLoopRun(r.ID)
		if log, _ := gitMem(repo, "log", "--format=%an", base+"..HEAD"); r.State == loopPRReady && log != "" {
			if log != "claude via partyline" {
				t.Errorf("commit author %q", log)
			}
			break
		}
		if r.State == loopFailed || time.Now().After(deadline) {
			evs, _ := loopEvents(r.ID)
			out, _ := exec.Command("sh", "-c", "cat "+loopDir(r.ID)+"/*.log 2>/dev/null | tail -20").Output()
			t.Fatalf("the step did not end committed: %s %q\n%+v\n%s", r.State, r.Note, evs, out)
		}
	}
	if !sawChip {
		t.Error("the builder's chip never showed under the session's tab while it ran")
	}
	if c, err := loadContractor(r.Builder); err != nil || c.State != "done" || c.Parent != r.Session[0] {
		t.Errorf("the builder's record: %+v %v", c, err)
	}
	reviewed := false
	for _, c := range loadContractors() {
		reviewed = reviewed || c.Loop == r.ID && c.Role == "reviewer" && c.State == "done" && c.Parent == r.Session[0]
	}
	if !reviewed {
		t.Error("the round's reviewer did not run as a contractor under the session")
	}
}
