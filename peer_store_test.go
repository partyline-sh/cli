package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/brand"
)

func TestPeerStoreRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-messages.json")
	m := peerMessage{ID: "c1", Direction: dirOutbound, Peer: "mac-studio", Project: "acr-cloud",
		Question: "does the limiter retry 429s?", Status: msgWaiting, AskedAt: time.Now()}
	if err := putPeerMessageAt(path, m); err != nil {
		t.Fatal(err)
	}
	got := loadPeerMessagesAt(path)
	if len(got) != 1 || got[0].ID != "c1" || got[0].Direction != dirOutbound || got[0].Peer != "mac-studio" {
		t.Fatalf("round trip lost the message: %+v", got)
	}
	if got[0].Resolved() {
		t.Error("a waiting message must not read as resolved")
	}

	// The upsert is by id: the answer replaces the waiting record rather than duplicating it.
	m.Status, m.Answer, m.AnsweredAt = "answered", "yes, with jitter", time.Now()
	if err := putPeerMessageAt(path, m); err != nil {
		t.Fatal(err)
	}
	got = loadPeerMessagesAt(path)
	if len(got) != 1 {
		t.Fatalf("upsert duplicated: %d messages", len(got))
	}
	if got[0].Answer != "yes, with jitter" || !got[0].Resolved() {
		t.Fatalf("answer not persisted: %+v", got[0])
	}
}

// A missing or corrupt store is an empty inbox — this is a cache of remote state, never a reason
// to fail the menu that reads it.
func TestPeerStoreToleratesMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if got := loadPeerMessagesAt(filepath.Join(dir, "nope.json")); got != nil {
		t.Fatalf("missing file = %v, want nil", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := savePeerMessagesAt(bad, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadPeerMessagesAt(bad); got != nil {
		t.Fatalf("corrupt file = %v, want nil", got)
	}
}

func TestPrunePeerMessages(t *testing.T) {
	now := time.Now()
	in := []peerMessage{
		{ID: "fresh", Status: "answered", AskedAt: now.Add(-time.Minute)},
		{ID: "read", Status: "answered", AskedAt: now.Add(-time.Minute), Read: true},
		{ID: "stale", Status: "answered", AskedAt: now.Add(-peerMsgTTL - time.Hour)},
		{ID: "stale-waiting", Status: msgWaiting, AskedAt: now.Add(-peerMsgTTL - time.Hour)},
	}
	got := prunePeerMessages(in, now)
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("prune kept %+v, want only the fresh unread one", got)
	}
}

func TestMarkPeerMessageReadDropsItFromTheInbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-messages.json")
	for _, id := range []string{"a", "b"} {
		if err := putPeerMessageAt(path, peerMessage{ID: id, Status: "answered", AskedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := markPeerMessageReadAt(path, "a"); err != nil {
		t.Fatal(err)
	}
	got := loadPeerMessagesAt(path)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("after marking a read: %+v, want only b", got)
	}
	// Marking an id that isn't there is a no-op, not an error.
	if err := markPeerMessageReadAt(path, "gone"); err != nil {
		t.Fatal(err)
	}
	if len(loadPeerMessagesAt(path)) != 1 {
		t.Fatal("marking an unknown id changed the store")
	}
}

// The inbox row must survive being clipped into a narrow modal, and its colour must not count
// toward the box width (the same rule cgRow is held to).
func TestPeerMessageRowRendersBothDirections(t *testing.T) {
	out := peerMessageRow(1, peerMessage{Direction: dirOutbound, Peer: "mac-studio",
		Project: "acr-cloud", Question: "does\nthe limiter retry 429s?", Status: "answered"})
	if strings.Contains(out, "\n") {
		t.Fatal("an inbox row must be exactly one line")
	}
	if !strings.Contains(out, "mac-studio") || !strings.Contains(out, "answered") {
		t.Fatalf("row missing peer or status: %q", out)
	}
	in := peerMessageRow(2, peerMessage{Direction: dirInbound, Peer: "air", Project: "partyline",
		Question: "ok to land?", Status: msgWaiting})
	if !strings.Contains(in, "air asks") {
		t.Fatalf("an inbound row must read as a question asked of you: %q", in)
	}
	for w := 8; w <= 60; w++ {
		if got := brand.VisWidth(brand.ClipEllipsis(out, w)); got > w {
			t.Fatalf("clipped row at %d has width %d", w, got)
		}
	}
}
