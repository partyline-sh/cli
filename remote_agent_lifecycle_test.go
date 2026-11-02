package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

func TestDetectedAgentInspectionFailureRefusesInput(t *testing.T) {
	tmuxTestServer(t, "ptln-test-detected-unavailable")
	pane, read := remoteInputRecorder(t, true)
	old := readAgentProcesses
	t.Cleanup(func() { readAgentProcesses = old })
	readAgentProcesses = func(string) ([]agentProcess, error) { return nil, errors.New("process inspection unavailable") }
	if err := runRemoteInput(remote.Message{Pane: pane, Kind: "paste", Data: "must not arrive"}); err == nil {
		t.Fatal("inspection failure allowed input")
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("input arrived: %q", got)
	}
}

func TestDetectedAgentRealShellLifecycleAndLegacyPeer(t *testing.T) {
	tmuxTestServer(t, "ptln-test-detected-agent")
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink("/bin/sleep", bin); err != nil {
		t.Fatal(err)
	}
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "--", "bash", "--noprofile", "--norc", "-i").CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v %s", err, out)
	}
	pane := strings.TrimSpace(string(out))
	if err := tmuxCmd("send-keys", "-t", pane, "-l", "--", bin+" 300").Run(); err != nil {
		t.Fatal(err)
	}
	if err := tmuxCmd("send-keys", "-t", pane, "Enter").Run(); err != nil {
		t.Fatal(err)
	}
	row := func(legacy bool) map[string]string {
		data, err := remoteSnapshotForPeer("sessions", "focus", !legacy)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range data.([]map[string]string) {
			if row["pane"] == pane {
				return row
			}
		}
		t.Fatal("pane missing")
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for row(false)["detected"] != "true" {
		if time.Now().After(deadline) {
			screen, _ := tmuxCmd("capture-pane", "-p", "-t", pane).Output()
			meta, _ := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_pid}\t#{pane_tty}").Output()
			fields := strings.Fields(string(meta))
			var processes []agentProcess
			if len(fields) == 2 {
				processes, _ = readAgentProcesses(fields[1])
			}
			t.Fatalf("foreground agent not detected: row=%v screen=%q processes=%+v", row(false), screen, processes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	target := row(false)["agentTarget"]
	if got := row(true); got["engine"] != "" || got["agentTarget"] != "" {
		t.Fatal("legacy peer adopted an agent", got)
	}
	if _, err := bindRemoteInput(remote.Message{Pane: pane, Kind: "text", Data: "hello"}); err == nil {
		t.Fatal("unbound legacy chat allowed")
	}
	if _, err := bindRemoteInput(remote.Message{Pane: pane, Kind: "text", Target: target, Data: "hello"}); err != nil {
		t.Fatal(err)
	}
	meta, err := tmuxCmd("display-message", "-p", "-t", pane, "#{@ptln_spec}|#{@ptln_key}|#{pane_pid}\t#{pane_tty}").Output()
	if err != nil || !strings.HasPrefix(string(meta), "||") {
		t.Fatalf("detection changed tags: %q %v", meta, err)
	}
	f := strings.Fields(strings.TrimPrefix(string(meta), "||"))
	agent, err := detectPaneAgent(f[0], f[1])
	if err != nil || agent == nil {
		t.Fatal("agent missing", err)
	}
	if err := syscall.Kill(agent.process.pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for row(false)["detected"] == "true" {
		if time.Now().After(deadline) {
			t.Fatal("agent did not revert to shell")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := bindRemoteInput(remote.Message{Pane: pane, Kind: "text", Target: target, Data: "old chat"}); err == nil {
		t.Fatal("stale agent chat allowed into parent shell")
	}
}

func TestDetectedAgentExitBetweenPasteAndDeferredEnter(t *testing.T) {
	tmuxTestServer(t, "ptln-test-detected-submit")
	s, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	b, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	old := readAgentProcesses
	t.Cleanup(func() { readAgentProcesses = old })
	processes := parseAgentProcesses(strings.TrimSpace(string(b)) + " 0 1 2 Sun Nov 1 12:00:00 2026 bash\n2 " + strings.TrimSpace(string(b)) + " 2 2 Sun Nov 1 12:00:01 2026 claude\n")
	readAgentProcesses = func(string) ([]agentProcess, error) { return processes, nil }
	identity, err := remotePaneIdentity(pane)
	if err != nil || !strings.HasPrefix(identity, "agent:") {
		t.Fatal(identity, err)
	}
	entered, release := holdRemoteSubmit(t, s)
	m := remote.Message{Type: "input", ID: "agent-exit", Pane: pane, Target: identity, Kind: "text", Data: "private draft"}
	if result := s.handleAsync(route, m, time.Now()); result.Status != "pending" {
		t.Fatal(result)
	}
	<-entered
	processes = processes[:1]
	processes[0].foreground = processes[0].group
	release()
	if result := waitInputResult(t, s, route, "input_result"); result.Status != "refused" {
		t.Fatal(result)
	}
	waitRemoteBytes(t, read, []byte("private draft"))
}
