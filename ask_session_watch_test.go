package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/ptymux"
)

// fakeAskMux drives the decision logic without real PTY children — there is no way to spin those up in
// a unit test, and the parts worth pinning (refuse an unsafe target, name the reason) don't need one.
type fakeAskMux struct {
	sessions    []ptymux.LiveSession
	unsubmitted int
	known       bool
	status      string
	banners     []string
}

func (f *fakeAskMux) LiveSessions() []ptymux.LiveSession { return f.sessions }
func (f *fakeAskMux) SessionByKey(string) (sessIO, string, string, bool) {
	return nil, "", "", false
}
func (f *fakeAskMux) UnsubmittedInput(string) (int, bool) { return f.unsubmitted, f.known }
func (f *fakeAskMux) SessionStatus(string) string         { return f.status }
func (f *fakeAskMux) SetBanner(s string)                  { f.banners = append(f.banners, s) }

// unsafeToAsk is the guard between a question and someone's "Allow Bash? (y/n)" prompt. Every refusal
// must also READ as a refusal to a human, because the text becomes the reason the asking agent is
// given — "is still working" is actionable, a bare false is not.
func TestUnsafeToAsk(t *testing.T) {
	cases := []struct {
		name        string
		unsubmitted int
		known       bool
		status      string
		wantRefusal string // "" = must allow
	}{
		{"idle and clean is fine", 0, true, "waiting", ""},
		{"half-typed prompt refuses", 7, true, "waiting", "half-typed"},
		{"mid-turn refuses", 0, true, "active", "still working"},
		// An unknown state is not an idle one. Both of these mean we cannot see the target's
		// situation, and typing into a session you can't see is the whole hazard.
		{"unknown status refuses", 0, true, "", "still working"},
		{"unreachable session refuses", 0, false, "waiting", "isn't reachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mx := &fakeAskMux{unsubmitted: c.unsubmitted, known: c.known, status: c.status}
			got := unsafeToAsk(mx, "k")
			if c.wantRefusal == "" {
				if got != "" {
					t.Fatalf("refused a safe target: %q", got)
				}
				return
			}
			if !strings.Contains(got, c.wantRefusal) {
				t.Fatalf("refusal %q does not mention %q", got, c.wantRefusal)
			}
		})
	}
}

// A question that can't be delivered must end as FAILED WITH A REASON, never just stop. An ask that
// silently stalls leaves the asking agent polling until its own timeout with nothing to report, which
// is indistinguishable from partyline being broken.
func TestUndeliverableAskFailsWithAReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the store
	// The asker is a THIRD session, so both ACR tabs stay candidates. (Naming one of them as the
	// asker would make "ACR" unambiguous by self-exclusion — correct behaviour, wrong fixture, and
	// this test is about the ambiguity path.)
	mx := &fakeAskMux{sessions: []ptymux.LiveSession{
		{Key: "k-a", Label: "ACR FLEET MANAGER"},
		{Key: "k-b", Label: "ACR ODOO MCP"},
		{Key: "k-me", Label: "partyline"},
	}}
	a := sessionAsk{ID: newAskID(), From: "k-me", FromLabel: "partyline", Target: "ACR", Question: "?", Status: askOpen}
	putAsk(a)
	runSessionAsk(mx, a) // "ACR" is ambiguous — must fail, not guess

	got, ok := getAsk(a.ID)
	if !ok {
		t.Fatal("the ask vanished instead of failing")
	}
	if got.Status != askFailed {
		t.Fatalf("status = %q, want %q", got.Status, askFailed)
	}
	if !strings.Contains(got.Reason, "matches") {
		t.Fatalf("reason doesn't explain the ambiguity: %q", got.Reason)
	}
}

// The store is the whole contract between two processes: cg-mcp writes, the mux updates, cg-mcp
// polls for a terminal status. A round trip that loses the answer loses the feature.
func TestAskStoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := sessionAsk{ID: newAskID(), From: "k-a", Target: "ACR ODOO MCP", Question: "which port?", Status: askOpen}
	putAsk(a)

	if open := openAsks(); len(open) != 1 || open[0].ID != a.ID {
		t.Fatalf("open asks = %+v, want the one just written", open)
	}
	a.Status, a.Answer = askAnswered, "8069"
	putAsk(a)

	got, ok := getAsk(a.ID)
	if !ok || got.Answer != "8069" || got.Status != askAnswered {
		t.Fatalf("round trip lost the answer: %+v ok=%v", got, ok)
	}
	// A terminal ask must leave the poller's queue, or the mux would re-deliver it forever.
	if open := openAsks(); len(open) != 0 {
		t.Fatalf("an answered ask is still queued for delivery: %+v", open)
	}
}
