package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"partyline.sh/partyline/internal/remote"
)

func TestRemoteTerminalHistory(t *testing.T) {
	tmuxTestServer(t, "ptln-test-terminal-history")
	store, phone := remoteInputFixture(t)
	pane, err := openRemoteShell()
	if err != nil {
		t.Fatal(err)
	}
	cmd := "for n in $(seq 1 300); do echo history-$n; done"
	if err = tmuxCmd("send-keys", "-t", pane, "-l", "--", cmd).Run(); err != nil {
		t.Fatal(err)
	}
	if err = tmuxCmd("send-keys", "-t", pane, "Enter").Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, _ := tmuxCmd("capture-pane", "-p", "-t", pane).Output()
		if strings.Contains(string(out), "history-300") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell never produced test history")
		}
		time.Sleep(10 * time.Millisecond)
	}
	result := store.handle(phone, remote.Message{Type: "terminal_history", ID: "history", Pane: pane}, time.Now())
	if result.Status != "ok" {
		t.Fatal(result)
	}
	data := result.Data.(map[string]any)
	if !strings.Contains(data["ansi"].(string), "history-1\n") || !strings.Contains(data["ansi"].(string), "history-300") {
		t.Fatal("older shell output missing")
	}
	mode, _ := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_in_mode}").Output()
	if strings.TrimSpace(string(mode)) != "0" {
		t.Fatal("history request entered copy mode")
	}
	if _, cached := store.phones[phone].results["history"]; cached {
		t.Fatal("read-only history response persisted")
	}
	if r := store.handle(phone, remote.Message{Type: "terminal_history", ID: "bad-target", Pane: "ptln:0"}, time.Now()); r.Status != "refused" {
		t.Fatal(r)
	}
	if r := store.handle("revoked", remote.Message{Type: "terminal_history", ID: "revoked", Pane: pane}, time.Now()); r.Status != "refused" {
		t.Fatal(r)
	}
}
func TestBoundTerminalHistory(t *testing.T) {
	source := strings.Repeat("long 世界 output\n", 100)
	text, limited := boundTerminalHistory(source, 120)
	if !limited || len(text) > 120 || !utf8.ValidString(text) || !strings.HasSuffix(source, text) {
		t.Fatal("invalid history truncation")
	}
	if text, limited = boundTerminalHistory("short", 120); text != "short" || limited {
		t.Fatal(text, limited)
	}
}

func TestTerminalHistoryUsesExistingViewInventory(t *testing.T) {
	tmuxTestServer(t, "ptln-test-history-inventory")
	store, phone := remoteInputFixture(t)
	for _, session := range []string{contractorSession, "unrelated-private-session"} {
		out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session, "--", "/bin/sh", "-c", "sleep 60").Output()
		if err != nil {
			t.Fatal(err)
		}
		pane := strings.TrimSpace(string(out))
		_, viewErr := remoteSnapshot(pane, "focus")
		history := store.handle(phone, remote.Message{Type: "terminal_history", ID: session, Pane: pane}, time.Now())
		if session == contractorSession {
			if viewErr != nil || history.Status != "ok" {
				t.Fatalf("existing contractor view and history differ: %v %+v", viewErr, history)
			}
		} else if viewErr == nil || history.Status != "refused" {
			t.Fatal("unrelated tmux session exposed")
		}
	}
}
