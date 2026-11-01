package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

func TestLiveTerminalInputAndCursor(t *testing.T) {
	tmuxTestServer(t, "ptln-test-live-terminal")
	_, route := remoteInputFixture(t)
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "-x", "80", "-y", "24", "--", "bash", "--noprofile", "--norc", "-i").CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v %s", err, out)
	}
	pane := strings.TrimSpace(string(out))
	tagTmuxPane(pane, Spec{Label: "live test", Argv: []string{"bash"}})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, err = livePaneState(pane); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	store := &liveStore{fast: true}
	opened := store.command(route, remote.Message{Type: "live_open", ID: "open-1", Pane: pane}, time.Now())
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	token := opened.Data.(map[string]any)["token"].(string)
	repeated := store.command(route, remote.Message{Type: "live_open", ID: "open-1", Pane: pane}, time.Now())
	if repeated.Data.(map[string]any)["token"] != token {
		t.Fatal("open is not idempotent")
	}
	send := func(seq uint64, kind, data string) remote.Message {
		return store.command(route, remote.Message{Type: "live_input", ID: token, Pane: pane, Revision: seq, Kind: kind, Data: data}, time.Now())
	}
	if result := send(1, "text", "echo cursor-probe"); result.Status != "ok" {
		t.Fatal(result)
	}
	if result := send(1, "text", "SHOULD NOT TYPE"); result.Status != "ok" {
		t.Fatal(result)
	}
	time.Sleep(100 * time.Millisecond)
	before, err := captureLiveScreen(pane)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(before.ANSI, "SHOULD NOT TYPE") || !strings.Contains(before.ANSI, "echo cursor-probe") {
		t.Fatal(before.ANSI)
	}
	if result := send(2, "key", "Left"); result.Status != "ok" {
		t.Fatal(result)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := captureLiveScreen(pane)
	if err != nil {
		t.Fatal(err)
	}
	if after.ANSI != before.ANSI || after.X != before.X-1 {
		t.Fatalf("cursor-only change lost: before=%+v after=%+v", before, after)
	}
	frame := store.collect(route, time.Now(), 0)
	if frame == nil || frame.Data.(liveScreen).Accepted != 2 || frame.Data.(liveScreen).Echo >= 2 {
		t.Fatal("write accepted as echo", frame)
	}
	time.Sleep(200 * time.Millisecond)
	frame = store.collect(route, time.Now(), 0)
	if frame == nil || frame.Data.(liveScreen).Echo != 2 {
		t.Fatal("settled echo missing", frame)
	}
	// Literal control bytes cannot execute a command through the text branch.
	if result := send(3, "text", "\r"); result.Status != "refused" || store.sessions[route] != nil {
		t.Fatal("control text accepted", result)
	}
	if result := send(3, "text", "lost"); result.Status != "refused" {
		t.Fatal("closed stream resumed")
	}
	b, _ := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
	if strings.Contains(string(b), "cursor-probe") || strings.Contains(string(b), "size") {
		t.Fatalf("typing leaked into audit: %s", b)
	}
}

func TestLiveTerminalScopeAndOrder(t *testing.T) {
	tmuxTestServer(t, "ptln-test-live-scope")
	_, route := remoteInputFixture(t)
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "--", "bash", "--noprofile", "--norc", "-i").Output()
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(string(out))
	time.Sleep(100 * time.Millisecond)
	tagTmuxPane(pane, Spec{Engine: "codex", Argv: []string{"codex"}})
	s := &liveStore{fast: true}
	open := func() remote.Message {
		return s.command(route, remote.Message{Type: "live_open", ID: "open", Pane: pane}, time.Now())
	}
	if open().Status != "refused" {
		t.Fatal("agent pane accepted")
	}
	tagTmuxPane(pane, Spec{Argv: []string{"bash"}})
	opened := open()
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	token := opened.Data.(map[string]any)["token"].(string)
	// A different authorized phone cannot interleave typing in the same pane.
	second := "22334455667788990011223344556677"
	b, _ := json.Marshal([]remotePhone{{ID: route}, {ID: second}})
	if err = os.WriteFile(filepath.Join(relayDir(), "phones.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if result := s.command(second, remote.Message{Type: "live_open", ID: "other", Pane: pane}, time.Now()); result.Status != "refused" {
		t.Fatal("second writer accepted")
	}
	if result := s.command(route, remote.Message{Type: "live_input", ID: token, Pane: pane, Revision: 2, Kind: "text", Data: "gap"}, time.Now()); result.Status != "refused" || s.sessions[route] != nil {
		t.Fatal("gap did not close", result)
	}
	opened = open()
	token = opened.Data.(map[string]any)["token"].(string)
	// Changing the pane's declared engine invalidates an existing token as well.
	tagTmuxPane(pane, Spec{Engine: "claude", Argv: []string{"claude"}})
	if result := s.command(route, remote.Message{Type: "live_input", ID: token, Pane: pane, Revision: 1, Kind: "text", Data: "agent"}, time.Now()); result.Status != "refused" {
		t.Fatal("scope change accepted")
	}
}

func TestLiveTerminalChildOutputAndInterrupt(t *testing.T) {
	tmuxTestServer(t, "ptln-test-live-child")
	_, route := remoteInputFixture(t)
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "--", "bash", "--noprofile", "--norc", "-i").Output()
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(string(out))
	time.Sleep(100 * time.Millisecond)
	s := &liveStore{fast: true}
	opened := s.command(route, remote.Message{Type: "live_open", ID: "child", Pane: pane}, time.Now())
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	token := opened.Data.(map[string]any)["token"].(string)
	send := func(seq uint64, kind, data string) remote.Message {
		return s.command(route, remote.Message{Type: "live_input", ID: token, Pane: pane, Revision: seq, Kind: kind, Data: data}, time.Now())
	}
	if r := send(1, "text", "sleep 20"); r.Status != "ok" {
		t.Fatal(r)
	}
	if r := send(2, "key", "Enter"); r.Status != "ok" {
		t.Fatal(r)
	}
	time.Sleep(100 * time.Millisecond)
	frame := s.collect(route, time.Now(), 0)
	if frame == nil || frame.Data.(liveScreen).Typing || frame.Data.(liveScreen).Foreground != "sleep" {
		t.Fatal("child output did not remain available", frame)
	}
	if r := send(3, "key", "C-c"); r.Status != "ok" {
		t.Fatal("cannot interrupt child", r)
	}
	time.Sleep(100 * time.Millisecond)
	frame = s.collect(route, time.Now(), 0)
	if frame == nil || !frame.Data.(liveScreen).Typing {
		t.Fatal("shell did not regain typing", frame)
	}
	// Revocation is checked for each burst, rather than only at live_open.
	if err = os.WriteFile(filepath.Join(relayDir(), "phones.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := send(4, "text", "revoked"); r.Status != "refused" || s.sessions[route] != nil {
		t.Fatal("revoked live writer survived", r)
	}
}

func TestLiveCadenceRequiresPrivateRelay(t *testing.T) {
	now := time.Now()
	s := liveStore{sessions: map[string]*liveTerminal{"phone": {until: now.Add(time.Minute), written: now}}}
	if got := s.interval(now, 0); got != 500*time.Millisecond {
		t.Fatalf("hosted cadence %v", got)
	}
	s.fast = true
	if got := s.interval(now, 0); got != 75*time.Millisecond {
		t.Fatalf("private cadence %v", got)
	}
	if got := s.interval(now, 80000); got != 5*time.Second {
		t.Fatalf("budget cadence %v", got)
	}
}
