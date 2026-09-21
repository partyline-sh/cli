package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// seedPeerFiles plants the three LOCAL files the peer slice reads. Returns the fixed "now".
func seedPeerFiles(t *testing.T, queued int, question string) time.Time {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	qs := make([]queuedConsult, 0, queued)
	for i := 0; i < queued; i++ {
		qs = append(qs, queuedConsult{
			Event:  api.ConsultEvent{Type: "consult", ConsultID: "c-" + string(rune('a'+i)), ProjectLabel: "web", Question: question},
			SeenAt: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	writeJSON(t, pendingConsultsPath(), struct {
		Consults []queuedConsult `json:"consults"`
	}{qs})

	writeJSON(t, peerStorePath(), struct {
		Messages []peerMessage `json:"messages"`
	}{[]peerMessage{
		{ID: "o-1", Direction: dirOutbound, Status: taskCompleted, Answer: "yes", AskedAt: now},
		{ID: "o-2", Direction: dirOutbound, Status: taskSubmitted, AskedAt: now},             // still out — not a reply
		{ID: "o-3", Direction: dirOutbound, Status: taskCompleted, Read: true, AskedAt: now}, // already read
		{ID: "i-1", Direction: dirInbound, Status: taskCompleted, AskedAt: now},              // theirs, not a reply to me
	}})
	writeJSON(t, consultBudgetPath(), consultBudget{Day: budgetDay(now), Total: 3,
		Projects: map[string]int{"web": 3}, LastProject: "web", LastAt: now})
	return now
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The key must be ABSENT when there's nothing to say, so an older tray (and anything else parsing the
// snapshot) sees exactly what it saw before this field existed.
func TestPeerStateAbsentWhenNothingToSay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if ps := currentPeerState(time.Now()); ps != nil {
		t.Fatalf("expected no peer state on a clean machine, got %+v", ps)
	}
	b, err := json.Marshal(machineState{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "peers") {
		t.Fatalf("the peers key must be omitted when empty: %s", b)
	}
}

func TestPeerStateCountsAndBounds(t *testing.T) {
	long := strings.Repeat("why does this break my callers ", 40) // way past the cap
	now := seedPeerFiles(t, 6, long)

	ps := currentPeerState(now)
	if ps == nil {
		t.Fatal("expected a peer slice")
	}
	if ps.Inbound != 6 {
		t.Fatalf("Inbound = %d, want 6 (every queued question is COUNTED even if not carried)", ps.Inbound)
	}
	if len(ps.Consults) != maxStateConsults {
		t.Fatalf("carried %d rows, want the cap of %d", len(ps.Consults), maxStateConsults)
	}
	for _, c := range ps.Consults {
		if n := len([]rune(c.Question)); n > maxStateQuestion+1 { // +1 for the ellipsis
			t.Fatalf("question not truncated: %d runes", n)
		}
		if c.ID == "" || c.Project != "web" {
			t.Fatalf("row missing identity: %+v", c)
		}
	}
	// Oldest first — the order to answer them in, and the order the console lists them.
	if ps.Consults[0].WaitingSec < ps.Consults[1].WaitingSec {
		t.Fatalf("rows should be oldest first, got %ds then %ds", ps.Consults[0].WaitingSec, ps.Consults[1].WaitingSec)
	}
	// Only MY unread answered asks count as a reply I'm owed.
	if ps.Answered != 1 {
		t.Fatalf("Answered = %d, want 1 (unread + outbound + completed only)", ps.Answered)
	}
	if ps.AutoAnswered != 3 || ps.AutoProject != "web" {
		t.Fatalf("auto-answer observability wrong: %d / %q", ps.AutoAnswered, ps.AutoProject)
	}
}

// A question with newlines must collapse to one line — a menu item IS one line, and a raw newline in a
// tray title truncates the row at the break.
func TestClipQuestionCollapsesAndBounds(t *testing.T) {
	got := clipQuestion("  does this\n\nbreak\tmy callers?  ")
	if got != "does this break my callers?" {
		t.Fatalf("got %q", got)
	}
	if n := len([]rune(clipQuestion(strings.Repeat("é", 500)))); n != maxStateQuestion+1 {
		t.Fatalf("rune-safe truncation expected %d runes, got %d", maxStateQuestion+1, n)
	}
}

// `ptln state` IS A LOCAL READ. The tray polls it every 4s from a background process; a network call
// on this path would be a 4s poll of the control plane on every user's machine. This test fails the
// moment anything on the peer path dials out.
func TestPeerStateMakesNoNetworkCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("ptln state made a network call: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv("PARTYLINE_API", srv.URL)
	now := seedPeerFiles(t, 2, "anything?")
	// A token present is the interesting case: it's what would let a lazily-added client actually call.
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".partyline", "token"), "tok")
	if ps := currentPeerState(now); ps == nil || ps.Inbound != 2 {
		t.Fatalf("expected 2 queued from disk, got %+v", ps)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The poller must not WRITE the daemon's pending set: newConsultQueue prunes and rewrites on load,
// which would mean the tray rewriting the daemon's state every 4 seconds, possibly mid-save.
func TestReadPendingConsultsIsReadOnlyAndTTLFiltered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	writeJSON(t, pendingConsultsPath(), struct {
		Consults []queuedConsult `json:"consults"`
	}{[]queuedConsult{
		{Event: api.ConsultEvent{ConsultID: "fresh", ProjectLabel: "web", Question: "q"}, SeenAt: now.Add(-time.Minute)},
		{Event: api.ConsultEvent{ConsultID: "stale", ProjectLabel: "web", Question: "q"}, SeenAt: now.Add(-2 * pendingConsultTTL)},
		{Event: api.ConsultEvent{ConsultID: "", ProjectLabel: "web"}, SeenAt: now},
	}})
	before, err := os.ReadFile(pendingConsultsPath())
	if err != nil {
		t.Fatal(err)
	}
	got := readPendingConsultsAt(pendingConsultsPath(), now)
	if len(got) != 1 || got[0].Event.ConsultID != "fresh" {
		t.Fatalf("expected only the in-window entry, got %+v", got)
	}
	after, err := os.ReadFile(pendingConsultsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the read-only view rewrote the daemon's pending set")
	}
}

// Every one of these files is a cache. A corrupt one must yield no peer slice and never an error —
// `ptln state` has to keep exiting 0 with valid JSON, or the tray reads it as "CLI missing".
func TestPeerStateSurvivesCorruptFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFile(t, pendingConsultsPath(), "{{{not json")
	writeFile(t, peerStorePath(), "also not json")
	writeFile(t, consultBudgetPath(), "nope")
	if ps := currentPeerState(time.Now()); ps != nil {
		t.Fatalf("corrupt caches should yield no peer slice, got %+v", ps)
	}
	if _, err := json.Marshal(currentMachineState()); err != nil {
		t.Fatalf("the snapshot must still encode: %v", err)
	}
}
