package main

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestContractorsTmux runs a stub contractor on a scratch tmux server: the row shows above the
// tabs with the ribbon still drawn, the window and the session go when it ends, the chip stays
// until seen, and the bar is back to two rows after. Only with PARTYLINE_TMUX_E2E=1.
func TestContractorsTmux(t *testing.T) {
	if os.Getenv("PARTYLINE_TMUX_E2E") != "1" {
		t.Skip("set PARTYLINE_TMUX_E2E=1 to run against a scratch tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmuxTestServer(t, "ptln-test-contractors")
	if err := writeTmuxConf(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "-c", dir, "--", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = tmuxCmd("source-file", stateDir()+"/tmux.conf").Run()
	parent, _ := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}").Output()
	opt := func(name string) string {
		out, _ := tmuxCmd("show-options", "-gv", name).Output()
		return strings.TrimSpace(string(out))
	}
	tabsBefore := opt("status-format[0]")

	c := &contractor{Parent: "pane:" + strings.TrimSpace(string(parent)), Role: "builder", Engine: "stub:sh", Title: "count", Dir: dir,
		Argv: []string{"sh", "-c", "for i in 1 2 3; do echo line $i; sleep 0.4; done"}}
	if err := startContractor(c); err != nil {
		t.Fatal(err)
	}
	if opt("status") != "3" || !strings.Contains(opt("status-format[0]"), "--contractors-row") {
		t.Fatalf("the row is not shown: status %s, [0] %q", opt("status"), opt("status-format[0]"))
	}
	if opt("status-format[1]") != tabsBefore {
		t.Errorf("row 1 is not the tabs: %q", opt("status-format[1]"))
	}
	if !strings.Contains(opt("status-format[2]"), "--status") {
		t.Errorf("the ribbon is not drawn in row 2: %q", opt("status-format[2]"))
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := loadContractor(c.ID)
		if got != nil && got.State != "running" {
			if got.State != "done" || got.Result != "line 3" {
				t.Fatalf("ended %s with %q", got.State, got.Result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the contractor did not finish")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	// What status-format[0] runs: its #() command, run as tmux would (display-message does not run
	// #() jobs; only an attached client's status line does). The chip must be under its parent's tab.
	tab, _ := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_index} #{window_name}").Output()
	want := strings.TrimSpace(string(tab)) + ": ✓ stub · count"
	job := regexp.MustCompile(`#\((.*)\)`).FindStringSubmatch(opt("status-format[0]"))
	if job == nil {
		t.Fatalf("status-format[0] runs no command: %q", opt("status-format[0]"))
	}
	drawn, err := exec.Command("sh", "-c", job[1]).Output()
	if plain := tmuxFormat.ReplaceAllString(string(drawn), ""); err != nil || !strings.Contains(plain, want) {
		t.Errorf("the row's command (%v) prints %q, not the ✓ chip under its parent (%q)", err, plain, want)
	}
	if got, _ := loadContractor(c.ID); contractorAlive(got) {
		t.Errorf("the contractor's window %s outlived it", got.Window)
	}
	if contractorSessionLive() {
		t.Error("the contractor session outlived its last contractor")
	}
	if opt("status") != "3" {
		t.Error("an unseen chip did not keep the row")
	}
	contractorsSeen(c.ID, "")
	if opt("status") != "2" || opt("status-format[0]") != tabsBefore || !strings.Contains(opt("status-format[1]"), "--status") {
		t.Errorf("the bar is not back: status %s, [0] %q, [1] %q", opt("status"), opt("status-format[0]"), opt("status-format[1]"))
	}
}

// A killed server ends the agent with it: the runner catches the hangup, ends the agent's process
// group and records it stopped. Only with PARTYLINE_TMUX_E2E=1.
func TestContractorsTmuxHangup(t *testing.T) {
	if os.Getenv("PARTYLINE_TMUX_E2E") != "1" {
		t.Skip("set PARTYLINE_TMUX_E2E=1 to run against a scratch tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmuxTestServer(t, "ptln-test-contractor-hup")
	dir := t.TempDir()
	if out, err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "-c", dir, "--", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	// The agent, and a descendant that ignores SIGTERM and sends its output elsewhere.
	pidFile, kidFile := dir+"/pid", dir+"/kid"
	c := &contractor{Parent: "pane:%0", Role: "builder", Engine: "stub:sh", Title: "sleep", Dir: dir,
		Argv: []string{"sh", "-c", "(trap '' TERM; exec sleep 120) >/dev/null 2>&1 & echo $! > " + kidFile + "; echo $$ > " + pidFile + "; wait"}}
	if err := startContractor(c); err != nil {
		t.Fatal(err)
	}
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatal("the stub did not start")
		}
	}
	b, _ := os.ReadFile(kidFile)
	kid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	// A second agent that handles SIGTERM and exits 0: interrupted all the same.
	graceFile := dir + "/grace"
	c2 := &contractor{Parent: "pane:%0", Role: "reviewer", Engine: "stub:sh", Title: "grace", Dir: dir,
		Argv: []string{"sh", "-c", "trap 'exit 0' TERM; echo $$ > " + graceFile + "; while :; do sleep 0.1; done"}}
	if err := startContractor(c2); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !fileExists(graceFile); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the second stub did not start")
		}
	}
	_ = tmuxCmd("kill-server").Run()
	for _, p := range []int{pid, kid} {
		for deadline := time.Now().Add(10 * time.Second); syscall.Kill(p, 0) == nil; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(p, syscall.SIGKILL)
				t.Fatalf("pid %d (agent %d, descendant %d) outlived the server", p, pid, kid)
			}
		}
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		got, _ := loadContractor(c.ID)
		if got != nil && got.State == "failed" && got.Result == "stopped: its tmux session ended" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the record after a killed server: %+v", got)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		got, _ := loadContractor(c2.ID)
		if got != nil && got.State != "running" {
			if got.State != "failed" || got.Result != "stopped: its tmux session ended" {
				t.Errorf("an agent that exits 0 on SIGTERM was recorded %s: %q", got.State, got.Result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second record after a killed server: %+v", got)
		}
	}
}
