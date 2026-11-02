package main

import (
	"os"
	"partyline.sh/partyline/internal/remote"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteSubmitRevokedBeforeEnter(t *testing.T) {
	tmuxTestServer(t, "ptln-test-submit-revoke")
	s, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	entered, release := holdRemoteSubmit(t, s)
	if r := s.handleAsync(route, remote.Message{Type: "input", ID: "revoke-me", Pane: pane, Kind: "text", Data: "private draft"}, time.Now()); r.Status != "pending" {
		t.Fatal(r)
	}
	<-entered
	if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	release()
	result := waitInputResult(t, s, route, "input_result")
	if result.Status != "refused" || !strings.Contains(result.Reason, "phone revoked") {
		t.Fatal(result)
	}
	waitRemoteBytes(t, read, []byte("private draft"))
	audit, _ := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
	if !strings.Contains(string(audit), "input_outcome") || !strings.Contains(string(audit), "failed") || strings.Contains(string(audit), "private draft") {
		t.Fatal("bad outcome audit")
	}
}

func TestRemoteSubmitPaneMovedBeforeEnter(t *testing.T) {
	tmuxTestServer(t, "ptln-test-submit-moved")
	s, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	entered, release := holdRemoteSubmit(t, s)
	if r := s.handleAsync(route, remote.Message{Type: "input", ID: "moved", Pane: pane, Kind: "text", Data: "draft"}, time.Now()); r.Status != "pending" {
		t.Fatal(r)
	}
	<-entered
	if out, err := tmuxCmd("new-session", "-d", "-s", "outside", "--", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("outside: %v %s", err, out)
	}
	if out, err := tmuxCmd("join-pane", "-s", pane, "-t", "=outside:").CombinedOutput(); err != nil {
		t.Fatalf("move: %v %s", err, out)
	}
	release()
	if result := waitInputResult(t, s, route, "input_result"); result.Status != "refused" {
		t.Fatal(result)
	}
	waitRemoteBytes(t, read, []byte("draft"))
}
