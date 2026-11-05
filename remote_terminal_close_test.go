package main

import (
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

func TestRemoteTerminalClose(t *testing.T) {
	tmuxTestServer(t, "ptln-test-terminal-close")
	store, phone := remoteInputFixture(t)
	pane, err := openRemoteShell()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := remoteSnapshot("sessions", "focus")
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, row := range rows.([]map[string]string) {
		if row["pane"] == pane {
			target = row["terminalTarget"]
		}
	}
	if target == "" {
		t.Fatal("shell process identity missing")
	}
	now := time.Now()
	invalid := store.handle(phone, remote.Message{Type: "terminal_close", ID: "stale", Pane: pane, Target: "old-process"}, now)
	if invalid.Status != "refused" {
		t.Fatal(invalid)
	}
	if got := store.handle("revoked", remote.Message{Type: "terminal_close", ID: "revoked", Pane: pane, Target: target}, now); got.Status != "refused" {
		t.Fatal(got)
	}
	if err = tmuxCmd("has-session", "-t", "="+tmuxSessionName).Run(); err != nil {
		t.Fatal("refused close stopped the shell")
	}
	message := remote.Message{Type: "terminal_close", ID: "close-once", Pane: pane, Target: target}
	if got := store.handle(phone, message, now); got.Status != "ok" {
		t.Fatal(got)
	}
	if got := store.handle(phone, message, now); got.Status != "ok" {
		t.Fatal(got)
	}
	if err = tmuxCmd("has-session", "-t", "="+tmuxSessionName).Run(); err == nil {
		t.Fatal("terminal still running")
	}
}
