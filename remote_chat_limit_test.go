package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// A paired phone may raise or lower the room's agent reply limit, as the person can from the
// chat panel. It may not turn the limit off: the limit is what stops agents answering each
// other unattended, and the phone is the remote surface. A manual /stop survives a change.
func TestRemoteChatLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, route := remoteInputFixture(t)
	if err := withChatLock(func(r *chatRoom) error { r.AgentRun, r.Stopped = 16, true; return nil }); err != nil {
		t.Fatal(err)
	}
	set := func(id string, limit any) remote.Message {
		return store.handle(route, remote.Message{Type: "chat_limit", ID: id, Data: map[string]any{"limit": limit}}, time.Now())
	}
	first := set("limit-20", 20)
	if first.Status != "ok" {
		t.Fatal(first)
	}
	room := loadChatRoom()
	if room.cap() != 20 || !room.Stopped || room.AgentRun != 16 {
		t.Fatalf("room after raise: cap=%d stopped=%v run=%d", room.cap(), room.Stopped, room.AgentRun)
	}
	if got := set("limit-20", 20); got.Status != first.Status || got.ID != first.ID {
		t.Fatal("a retry with the same id was not answered from the first result", got)
	}
	for i, bad := range []any{-1, 0, 3, 1000001, "off", 4.5, nil} {
		if got := set(fmt.Sprintf("bad-%d", i), bad); got.Status == "ok" {
			t.Errorf("limit %v accepted", bad)
		}
	}
	if after := loadChatRoom(); after.cap() != 20 {
		t.Fatal("a refused change altered the limit")
	}
	if got := store.handle(strings.Repeat("99", 16), remote.Message{Type: "chat_limit", ID: "stranger", Data: map[string]any{"limit": 40}}, time.Now()); got.Status == "ok" {
		t.Fatal("an unpaired phone changed the limit")
	}
	audit, err := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
	if err != nil || strings.Count(string(audit), `"action":"chat_limit"`) != 1 || !strings.Contains(string(audit), `"limit":20`) {
		t.Fatalf("want exactly one audited change across the retry: %v %s", err, audit)
	}
	// Once the phone is revoked, a new change from it is refused and the limit stays put.
	phones, err := remotePhones()
	if err != nil || len(phones) != 1 {
		t.Fatal(phones, err)
	}
	if err := writePhones(nil, "revoked", phones[0]); err != nil {
		t.Fatal(err)
	}
	if got := set("after-revoke", 40); got.Status == "ok" {
		t.Fatal("a revoked phone changed the limit")
	}
	if after := loadChatRoom(); after.cap() != 20 {
		t.Fatalf("a revoked phone moved the limit to %d", after.cap())
	}
}
