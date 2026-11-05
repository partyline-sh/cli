package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/flynn/noise"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

func TestRemoteSessionManager(t *testing.T) {
	tmuxTestServer(t, "ptln-test-session-manager")
	store, phone := remoteInputFixture(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "conversation.jsonl")
	if err := os.WriteFile(transcript, []byte("saved history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var sessions []aiSession
	for i := 0; i < 35; i++ {
		sessions = append(sessions, aiSession{ID: fmt.Sprintf("session-%02d", i), Tool: "claude", Title: fmt.Sprintf("Project %02d", i), Cwd: dir, resumeDir: dir, storePath: transcript, resumeArgv: []string{"/bin/sh", "-c", "sleep 60"}, LastActive: time.Now()})
	}
	saveLauncherCache(sessions)
	now := time.Now()
	seq := 0
	send := func(kind, pane string, data any) remote.Message {
		seq++
		return store.handle(phone, remote.Message{Type: kind, ID: fmt.Sprint(seq), Pane: pane, Data: data}, now.Add(time.Duration(seq)*time.Second))
	}
	page := send("session_list", "", map[string]any{"offset": 0})
	if page.Status != "ok" {
		t.Fatal(page)
	}
	rows := page.Data.(remoteSavedPage)
	if len(rows.Sessions) != 30 || rows.Total != 35 || rows.NextOffset == nil || *rows.NextOffset != 30 {
		t.Fatal(rows)
	}
	next := send("session_list", "", map[string]any{"offset": 30}).Data.(remoteSavedPage)
	if len(next.Sessions) != 5 || next.NextOffset != nil {
		t.Fatal(next)
	}
	filtered := send("session_list", "", map[string]any{"query": "project 03"}).Data.(remoteSavedPage)
	if len(filtered.Sessions) != 1 || filtered.Sessions[0].Key != "session-03" {
		t.Fatal(filtered)
	}
	if r := send("session_list", "", map[string]any{"offset": -1}); r.Status != "refused" {
		t.Fatal(r)
	}
	if r := send("session_open", "", map[string]string{"engine": "claude", "key": "injected; touch nope"}); r.Status != "refused" {
		t.Fatal(r)
	}
	open := remote.Message{Type: "session_open", ID: "open-once", Data: map[string]string{"engine": "claude", "key": "session-03"}}
	opened := store.handle(phone, open, now)
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	pane := opened.Data.(map[string]string)["pane"]
	if !remoteSessionPane.MatchString(pane) {
		t.Fatal(pane)
	}
	if again := store.handle(phone, open, now); again.Data.(map[string]string)["pane"] != pane {
		t.Fatal(again)
	}
	if again := send("session_open", "", map[string]string{"engine": "claude", "key": "session-03"}); again.Data.(map[string]string)["pane"] != pane {
		t.Fatal(again)
	}
	active := send("session_list", "", map[string]string{"query": "project 03"}).Data.(remoteSavedPage)
	if active.Sessions[0].Pane != pane {
		t.Fatal(active)
	}
	if r := send("session_close", pane, map[string]string{"key": "wrong-session"}); r.Status != "refused" {
		t.Fatal(r)
	}
	if r := send("session_close", "ptln:0", map[string]string{"key": "session-03"}); r.Status != "refused" {
		t.Fatal(r)
	}
	// A split neighbour must survive closing just the requested LLM pane.
	siblingRaw, err := tmuxCmd("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", pane, "--", "/bin/sh", "-c", "sleep 60").Output()
	if err != nil {
		t.Fatal(err)
	}
	sibling := strings.TrimSpace(string(siblingRaw))
	close := remote.Message{Type: "session_close", ID: "close-once", Pane: pane, Data: map[string]string{"key": "session-03"}}
	if r := store.handle(phone, close, now); r.Status != "ok" {
		t.Fatal(r)
	}
	if r := store.handle(phone, close, now); r.Status != "ok" {
		t.Fatal(r)
	}
	if err := tmuxCmd("display-message", "-p", "-t", sibling, "#{pane_id}").Run(); err != nil {
		t.Fatal("closing the LLM also removed its neighbour", err)
	}
	if got, err := os.ReadFile(transcript); err != nil || string(got) != "saved history\n" {
		t.Fatalf("history changed: %q %v", got, err)
	}
	if r := send("session_close", pane, map[string]string{"key": "session-03"}); r.Status != "refused" {
		t.Fatal(r)
	}
	if r := store.handle("revoked", open, now); r.Status != "refused" || !strings.Contains(r.Reason, "revoked") {
		t.Fatal(r)
	}
	// Closing removes the process, not its resumable row.
	after := send("session_list", "", map[string]string{"query": "project 03"}).Data.(remoteSavedPage)
	if len(after.Sessions) != 1 || after.Sessions[0].Pane != "" {
		t.Fatal(after)
	}
}

func TestRemoteSessionManagerEncryptedDispatch(t *testing.T) {
	tmuxTestServer(t, "ptln-test-session-manager-wire")
	store, route := remoteInputFixture(t)
	dir := t.TempDir()
	saveLauncherCache([]aiSession{{ID: "wire-session", Tool: "claude", Title: "Saved task", Cwd: dir, resumeDir: dir, resumeArgv: []string{"/bin/sh", "-c", "sleep 60"}}})
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]remotePhone{{ID: route, Static: phone.Public}})
	if err = os.WriteFile(filepath.Join(relayDir(), "phones.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	channel := newRemoteChannel()
	channel.inputs = store
	now := time.Now()
	h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
	if err != nil {
		t.Fatal(err)
	}
	hello, _, _, _ := h.WriteMessage(nil, nil)
	if err = channel.receive(route, hello, now); err != nil {
		t.Fatal(err)
	}
	_, send, receive, err := h.ReadMessage(nil, channel.pending[route])
	if err != nil {
		t.Fatal(err)
	}
	call := func(m remote.Message) remote.Message {
		t.Helper()
		raw, _ := json.Marshal([]remote.Message{m})
		wire, e := send.Encrypt(nil, nil, raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = channel.receive(route, wire, now); e != nil {
			t.Fatal(e)
		}
		wire, e = channel.peers[route].Flush()
		if e != nil {
			t.Fatal(e)
		}
		raw, e = receive.Decrypt(nil, nil, wire)
		if e != nil {
			t.Fatal(e)
		}
		var results []remote.Message
		if e = json.Unmarshal(raw, &results); e != nil {
			t.Fatal(e)
		}
		for _, r := range results {
			if r.Type == "input_result" && r.ID == m.ID {
				return r
			}
		}
		t.Fatal("missing session control result")
		return remote.Message{}
	}
	listed := call(remote.Message{Type: "session_list", ID: "wire-list"})
	if listed.Status != "ok" {
		t.Fatal(listed)
	}
	opened := call(remote.Message{Type: "session_open", ID: "wire-open", Data: map[string]string{"engine": "claude", "key": "wire-session"}})
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	pane := opened.Data.(map[string]any)["pane"].(string)
	history := call(remote.Message{Type: "terminal_history", ID: "wire-history", Pane: pane})
	if history.Status != "ok" {
		t.Fatal(history)
	}
	closed := call(remote.Message{Type: "session_close", ID: "wire-close", Pane: pane, Data: map[string]string{"key": "wire-session"}})
	if closed.Status != "ok" {
		t.Fatal(closed)
	}
}
